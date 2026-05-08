package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"nexcore-x-ui/config"
	"nexcore-x-ui/database"
	"nexcore-x-ui/util/random"
	"nexcore-x-ui/web/service"
)

// Reporter payload schema. Versioned via SchemaVersion so receivers can
// switch on it instead of guessing from binary version. v1 is what we
// ship in v2.3.x; bump only on breaking field changes.
type reportPayload struct {
	SchemaVersion string       `json:"schemaVersion"`
	PanelVersion  string       `json:"panelVersion"`
	Timestamp     int64        `json:"timestamp"`
	Nonce         string       `json:"nonce"`
	Panel         reportPanel  `json:"panel"`
	Host          reportHost   `json:"host"`
	Admin         *reportAdmin `json:"admin,omitempty"`
	API           *reportAPI   `json:"api,omitempty"`
}

type reportPanel struct {
	Port              int    `json:"port"`
	Scheme            string `json:"scheme"`
	BasePath          string `json:"basePath"`
	SecureEntryEnable bool   `json:"secureEntryEnabled"`
	SecureEntryPath   string `json:"secureEntryPath,omitempty"`
	URL               string `json:"url"`
	APIBaseURL        string `json:"apiBaseURL"`
	TLS               bool   `json:"tls"`
}

type reportHost struct {
	PublicIP  string `json:"publicIP,omitempty"`
	PrivateIP string `json:"privateIP,omitempty"`
	Hostname  string `json:"hostname,omitempty"`
	Arch      string `json:"arch,omitempty"`
}

type reportAdmin struct {
	Username string `json:"username"`
	Password string `json:"password,omitempty"` // plaintext, only when caller passed via env
}

type reportAPI struct {
	Token string `json:"token,omitempty"`
	Scope string `json:"scope"`
}

// runReport composes a one-shot HMAC-signed POST describing the panel
// install state to an operator-supplied callback URL. Used by cloud-
// provider one-click deploy scripts to receive panel creds + URL right
// after install completes, instead of requiring the operator to SSH in
// and grep journalctl manually.
//
// Security model:
//   - HTTPS only by default. `-allow-http` opens up http:// for local
//     dev / private-network testing. The flag exists so we never
//     silently downgrade — operator must explicitly accept the risk.
//   - HMAC-SHA256 over the raw JSON body, sent as
//       X-NexCore-Signature: sha256=<hex>
//     plus X-NexCore-Timestamp for replay protection (the body's
//     `timestamp` field is the canonical value; the header is a copy
//     for receivers that pre-validate before parsing the body).
//   - REPORT_KEY mandatory (env-only). Without it this command refuses
//     to run — sending plaintext creds with no auth header is a
//     foot-gun, no graceful fallback.
//   - Single attempt, configurable timeout (default 10s), no retry. If
//     the receiver isn't ready the install itself still succeeded —
//     creds are in journal too.
//   - REPORT_KEY / NEXCORE_REPORT_PASSWORD / NEXCORE_REPORT_API_TOKEN
//     are env-only (never CLI flags) — visible CLI args leak via
//     /proc/<pid>/cmdline and `ps`. The URL CAN go on the CLI; it's
//     not a secret in itself.
//   - When creds env vars are empty we OMIT the admin/api blocks
//     rather than failing — receivers that only want URL/IP/port
//     still get a usable payload.
func runReport(reportURL string, allowHTTP bool, timeout time.Duration) {
	key := os.Getenv("REPORT_KEY")
	if key == "" {
		reportDie("REPORT_KEY env var is required (empty key would mean unsigned plaintext POST — refused)")
	}
	u := strings.TrimSpace(reportURL)
	if u == "" {
		reportDie("-url is required")
	}
	switch {
	case strings.HasPrefix(u, "https://"):
		// ok
	case strings.HasPrefix(u, "http://"):
		if !allowHTTP {
			reportDie("report URL must use https:// (pass -allow-http to override for testing)")
		}
	default:
		reportDie("report URL must start with https:// (or http:// with -allow-http)")
	}

	if database.GetDB() == nil {
		if err := database.InitDB(config.GetDBPath()); err != nil {
			reportDie("open db failed: " + err.Error())
		}
	}

	ss := service.SettingService{}
	us := service.UserService{}
	port, _ := ss.GetPort()
	listen, _ := ss.GetListen()
	basePath, _ := ss.GetBasePath()
	seEnabled := ss.GetSecureEntryEnabled()
	sePath := strings.TrimSpace(ss.GetSecureEntryPath())
	certFile, _ := ss.GetCertFile()
	keyFile, _ := ss.GetKeyFile()
	tlsEnabled := certFile != "" && keyFile != ""

	if !strings.HasSuffix(basePath, "/") {
		basePath += "/"
	}
	if seEnabled && sePath != "" {
		basePath = basePath + sePath + "/"
	}

	scheme := "http"
	if tlsEnabled {
		scheme = "https"
	}

	// IP discovery: listen 字段优先(operator 显式选过的);否则 firstNonLoopbackIP。
	// 公网 IP 只在 (listen 空 OR listen 是私网) 时才 race 探测 — 已经
	// 是公网的话不浪费一次出网。云厂商场景下 firstNonLoopbackIP 拿到的
	// 几乎总是 VPC 内网,所以 publicIP 探测基本必触发。
	privateIP := strings.TrimSpace(listen)
	if privateIP == "" {
		privateIP = firstNonLoopbackIP()
	}
	publicIP := ""
	if privateIP == "" || isPrivateIPv4(privateIP) {
		publicIP = detectPublicIPv4(1500 * time.Millisecond)
	} else {
		// listen 已经是公网 IP — 复用,免一次出网
		publicIP = privateIP
		privateIP = firstNonLoopbackIP()
	}
	pickIP := publicIP
	if pickIP == "" {
		pickIP = privateIP
	}
	if pickIP == "" {
		pickIP = "<host>"
	}
	hostPort := pickIP
	if port > 0 {
		hostPort = fmt.Sprintf("%s:%d", pickIP, port)
	}

	panelURL := fmt.Sprintf("%s://%s%s", scheme, hostPort, basePath)
	apiBaseURL := fmt.Sprintf("%s://%s/api/v1", scheme, hostPort)

	hostname, _ := os.Hostname()

	payload := reportPayload{
		SchemaVersion: "1",
		PanelVersion:  config.GetVersion(),
		Timestamp:     time.Now().Unix(),
		Nonce:         random.Seq(16),
		Panel: reportPanel{
			Port:              port,
			Scheme:            scheme,
			BasePath:          basePath,
			SecureEntryEnable: seEnabled,
			SecureEntryPath:   sePath,
			URL:               panelURL,
			APIBaseURL:        apiBaseURL,
			TLS:               tlsEnabled,
		},
		Host: reportHost{
			PublicIP:  publicIP,
			PrivateIP: privateIP,
			Hostname:  hostname,
			Arch:      runtime.GOARCH,
		},
	}

	// 用户名总是发(非敏感);密码只在 install.sh 抓到 banner 才有,
	// 没抓到 NEXCORE_REPORT_PASSWORD 就空 → 接收方拿到 username 但
	// password 字段缺省。日常重启走 `nexcore-x-ui report` 就是这种
	// "username only" 场景,符合预期(没人会想 reload 一下就把当前
	// 改过的密码再 broadcast 一次,密码都已 bcrypt 找不回了)。
	if user, err := us.GetFirstUser(); err == nil && user != nil && user.Username != "" {
		payload.Admin = &reportAdmin{
			Username: user.Username,
			Password: os.Getenv("NEXCORE_REPORT_PASSWORD"),
		}
	}
	if tok := os.Getenv("NEXCORE_REPORT_API_TOKEN"); tok != "" {
		payload.API = &reportAPI{
			Token: tok,
			Scope: service.ScopeAdmin,
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		reportDie("marshal payload failed: " + err.Error())
	}

	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		reportDie("build request failed: " + err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-NexCore-Signature", "sha256="+sig)
	req.Header.Set("X-NexCore-Timestamp", fmt.Sprint(payload.Timestamp))
	req.Header.Set("User-Agent", "nexcore-x-ui-installer/"+config.GetVersion())

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		reportDie("POST failed: " + err.Error())
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		reportDie(fmt.Sprintf("receiver responded HTTP %d", resp.StatusCode))
	}
	fmt.Printf("report posted to %s (HTTP %d)\n", redactURL(u), resp.StatusCode)

	// Best-effort scrub of secret env vars in this process. Children inherit
	// from parent shell, not from us — so this only matters if anything else
	// in this binary's lifetime were to read them, which it doesn't today.
	// Cheap defense in depth.
	_ = os.Unsetenv("REPORT_KEY")
	_ = os.Unsetenv("NEXCORE_REPORT_PASSWORD")
	_ = os.Unsetenv("NEXCORE_REPORT_API_TOKEN")
}

func reportDie(msg string) {
	fmt.Fprintln(os.Stderr, "report:", msg)
	os.Exit(1)
}

// redactURL trims a URL down to scheme://host for use in human-facing
// log lines. Path / query may carry tokens the operator embedded in the
// URL itself; printing the full URL to journal would leak them.
func redactURL(raw string) string {
	if i := strings.Index(raw, "://"); i >= 0 {
		end := strings.IndexAny(raw[i+3:], "/?#")
		if end < 0 {
			return raw
		}
		return raw[:i+3+end]
	}
	return raw
}
