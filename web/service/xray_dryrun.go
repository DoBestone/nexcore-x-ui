package service

// xray_dryrun.go provides "candidate config validation" used to prevent the
// "one bad inbound takes down all protocols" failure mode that 3x-ui exhibits.
//
// Background: xray-core runs as a single process holding every inbound. If
// any inbound's settings/streamSettings JSON is invalid, xray refuses to
// start — which means the unrelated, perfectly valid inbounds also stop
// working. The fix is to run `xray test -c -` against the full candidate
// config *before* committing the change to the database, so we never let a
// broken config become the running config.
//
// Hooks live in InboundService / ClientService write paths — both /api/v1/*
// and the panel UI funnel through those, so there is no way to bypass dry-run
// short of editing the sqlite file by hand (out of scope).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"nexcore-x-ui/database/model"
	"nexcore-x-ui/util/json_util"
	"nexcore-x-ui/xray"
)

// ErrXrayConfigInvalid is returned when `xray test` rejects the candidate
// config. The wrapped error carries xray's stderr so callers can surface
// the exact field that failed.
var ErrXrayConfigInvalid = errors.New("xray_config_invalid")

// dryRunTimeout caps a single xray test invocation. Real validation is
// sub-second; this is for runaway / hung binaries.
const dryRunTimeout = 8 * time.Second

// dryRunGlobalXraySvc is the package-level XrayService that InboundService /
// ClientService reach through. We can't take a *XrayService field on those
// structs without creating an init-order cycle (XrayService already embeds
// InboundService). A shared singleton is the smallest hammer.
var dryRunGlobalXraySvc XrayService

// GetXrayServiceForDryRun returns the package-shared XrayService instance
// used to build candidate configs and run dry-runs. Exported so test code
// or alternative wirings can swap it.
func GetXrayServiceForDryRun() *XrayService { return &dryRunGlobalXraySvc }

// BuildCandidateConfig produces what xray *would* be running if `inbounds`
// replaced the persisted set. It mirrors GetXrayConfig but parameterizes
// the inbound list — so the caller can simulate "what if I add this one"
// or "what if I flip enable on these" without touching the DB first.
func (s *XrayService) BuildCandidateConfig(inbounds []*model.Inbound) (*xray.Config, error) {
	templateConfig, err := s.settingService.GetXrayConfigTemplate()
	if err != nil {
		return nil, err
	}

	xrayConfig := &xray.Config{}
	if err := json.Unmarshal([]byte(templateConfig), xrayConfig); err != nil {
		return nil, err
	}

	for _, inbound := range inbounds {
		if !inbound.Enable {
			continue
		}
		inboundConfig := inbound.GenXrayInboundConfig()
		xrayConfig.InboundConfigs = append(xrayConfig.InboundConfigs, *inboundConfig)
	}

	logCfg := map[string]interface{}{}
	if len(xrayConfig.LogConfig) > 0 {
		_ = json.Unmarshal(xrayConfig.LogConfig, &logCfg)
	}
	logCfg["access"] = xray.GetAccessLogPath()
	if _, ok := logCfg["loglevel"]; !ok {
		logCfg["loglevel"] = "warning"
	}
	if logBytes, err := json.Marshal(logCfg); err == nil {
		xrayConfig.LogConfig = json_util.RawMessage(logBytes)
	}

	if err := injectBlockRules(xrayConfig, &s.blockRuleService); err != nil {
		// Match GetXrayConfig: block rules are best-effort.
	}

	return xrayConfig, nil
}

// DryRun marshals the candidate config and runs `xray test -c -` with it on
// stdin. Returns nil on success; ErrXrayConfigInvalid wrapped with xray's
// own stderr on failure.
func (s *XrayService) DryRun(cfg *xray.Config) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal candidate config: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), dryRunTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, xray.GetBinaryPath(), "test", "-c", "stdin:")
	cmd.Stdin = bytes.NewReader(data)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr // xray test prints to either; capture both

	if err := cmd.Run(); err != nil {
		// Surface xray's own diagnostic — it's specific (e.g. "infra/conf:
		// failed to parse private key — invalid base64 length") which the
		// API caller / panel user needs to fix the field.
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		// xray test exits with status 23 for invalid config, 0 for ok;
		// don't bother distinguishing here — non-zero == reject.
		return fmt.Errorf("%w: %s", ErrXrayConfigInvalid, msg)
	}
	return nil
}

// DryRunInbounds is the convenience entry point InboundService calls: build
// candidate config from a hypothetical inbound list and run xray test on it.
func (s *XrayService) DryRunInbounds(inbounds []*model.Inbound) error {
	cfg, err := s.BuildCandidateConfig(inbounds)
	if err != nil {
		return fmt.Errorf("build candidate config: %w", err)
	}
	return s.DryRun(cfg)
}
