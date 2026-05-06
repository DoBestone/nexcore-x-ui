package service

import (
	"encoding/json"
	"errors"
	"sync"
	"nexcore-x-ui/logger"
	"nexcore-x-ui/util/json_util"
	"nexcore-x-ui/xray"

	"go.uber.org/atomic"
)

var p *xray.Process
var lock sync.Mutex
var isNeedXrayRestart atomic.Bool
var result string

type XrayService struct {
	inboundService   InboundService
	settingService   SettingService
	blockRuleService BlockRuleService
}

func (s *XrayService) IsXrayRunning() bool {
	return p != nil && p.IsRunning()
}

func (s *XrayService) GetXrayErr() error {
	if p == nil {
		return nil
	}
	return p.GetErr()
}

func (s *XrayService) GetXrayResult() string {
	if result != "" {
		return result
	}
	if s.IsXrayRunning() {
		return ""
	}
	if p == nil {
		return ""
	}
	result = p.GetResult()
	return result
}

func (s *XrayService) GetXrayVersion() string {
	if p == nil {
		return "Unknown"
	}
	return p.GetVersion()
}

func (s *XrayService) GetXrayConfig() (*xray.Config, error) {
	templateConfig, err := s.settingService.GetXrayConfigTemplate()
	if err != nil {
		return nil, err
	}

	xrayConfig := &xray.Config{}
	err = json.Unmarshal([]byte(templateConfig), xrayConfig)
	if err != nil {
		return nil, err
	}

	inbounds, err := s.inboundService.GetAllInbounds()
	if err != nil {
		return nil, err
	}
	for _, inbound := range inbounds {
		if !inbound.Enable {
			continue
		}
		inboundConfig := inbound.GenXrayInboundConfig()
		xrayConfig.InboundConfigs = append(xrayConfig.InboundConfigs, *inboundConfig)
	}

	// 强制注入 access log 路径，保证在线 IP 统计可用。模板里如果配了
	// loglevel/error 等字段会保留，仅覆盖 access。
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

	// 注入用户在面板配置的"屏蔽规则"。注入到 routing.rules 数组**最前面**，
	// 保证黑名单优先级高于模板中的其它规则。模板里已有的 api / blocked
	// 路由原样保留在后面。失败时静默跳过，不阻塞 xray 启动。
	if err := injectBlockRules(xrayConfig, &s.blockRuleService); err != nil {
		logger.Warning("注入屏蔽规则失败，跳过:", err)
	}

	return xrayConfig, nil
}

// injectBlockRules 把 BlockRuleService 生成的规则插入到 xrayConfig.RouterConfig
// 的 rules 数组开头。RouterConfig 是 raw JSON，unmarshal → 改 → marshal。
func injectBlockRules(xrayConfig *xray.Config, svc *BlockRuleService) error {
	blockRules, err := svc.GenXrayRoutingRules()
	if err != nil {
		return err
	}
	if len(blockRules) == 0 {
		return nil
	}

	router := map[string]interface{}{}
	if len(xrayConfig.RouterConfig) > 0 {
		if err := json.Unmarshal(xrayConfig.RouterConfig, &router); err != nil {
			return err
		}
	}

	existing, _ := router["rules"].([]interface{})
	merged := make([]interface{}, 0, len(blockRules)+len(existing))
	for _, br := range blockRules {
		merged = append(merged, br)
	}
	merged = append(merged, existing...)
	router["rules"] = merged

	out, err := json.Marshal(router)
	if err != nil {
		return err
	}
	xrayConfig.RouterConfig = json_util.RawMessage(out)
	return nil
}

func (s *XrayService) GetXrayTraffic() ([]*xray.Traffic, error) {
	if !s.IsXrayRunning() {
		return nil, errors.New("xray is not running")
	}
	return p.GetTraffic(true)
}

func (s *XrayService) RestartXray(isForce bool) error {
	lock.Lock()
	defer lock.Unlock()
	logger.Debug("restart xray, force:", isForce)

	xrayConfig, err := s.GetXrayConfig()
	if err != nil {
		return err
	}

	if p != nil && p.IsRunning() {
		if !isForce && p.GetConfig().Equals(xrayConfig) {
			logger.Debug("not need to restart xray")
			return nil
		}
		p.Stop()
	}

	p = xray.NewProcess(xrayConfig)
	result = ""
	if err := p.Start(); err != nil {
		return err
	}
	// 启动 / 重置在线 IP 统计：首次幂等启动后台 tail，重启时清空旧状态
	// 并通知 tailer 重新打开新的 access.log。
	onlineSvc := GetOnlineIPService()
	onlineSvc.Start()
	onlineSvc.OnXrayRestart()
	return nil
}

func (s *XrayService) StopXray() error {
	lock.Lock()
	defer lock.Unlock()
	logger.Debug("stop xray")
	if s.IsXrayRunning() {
		return p.Stop()
	}
	return errors.New("xray is not running")
}

func (s *XrayService) SetToNeedRestart() {
	isNeedXrayRestart.Store(true)
}

func (s *XrayService) IsNeedRestartAndSetFalse() bool {
	return isNeedXrayRestart.CAS(true, false)
}

// GetEffectiveConfig returns the JSON config that would be (or was) handed to
// the xray subprocess. Use it to diagnose what xray actually sees vs what is
// in the database.
func (s *XrayService) GetEffectiveConfig() (string, error) {
	cfg, err := s.GetXrayConfig()
	if err != nil {
		return "", err
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	return string(out), err
}

// GetRecentLogs returns up to 100 most recent stdout/stderr lines from xray.
// The buffer is kept in memory by the running process; lines older than the
// last restart are gone.
func (s *XrayService) GetRecentLogs() string {
	if p == nil {
		return ""
	}
	return p.GetResult()
}
