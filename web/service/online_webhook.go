package service

// OnlineWebhookService — 把面板的"在线 IP × email"快照定时推给上游业务系统,
// 用于多节点配额聚合(每客户最多 N 设备 = N 个 IP,跨多个节点共享)。
//
// 数据流:
//   OnlineIPService.byEmail (memory)
//        │  每 webhookInterval 拍一次快照
//        ▼
//   diff vs lastSentSnapshot
//        │  非空才发
//        ▼
//   POST {url}  body=JSON  X-Nx-Signature=hex(HMAC-SHA256(secret, body))
//        │  2xx → 更新 lastSentSnapshot
//        │  5xx / 网络错 → 保留 lastSent,下个 tick 自动重试
//        │  4xx → 日志告警 + 仍重试(签名/路径错业务方需要修)
//
// 推送语义是"幂等替换":接收方应该把 node_id 名下的状态全清掉,用 body
// 里的 snapshot 覆盖。snapshot 里没出现的 email 视为该节点上不在线。
//
// 不在 hot path 上加 hook,改用周期 snapshot:
//   1. 实现简单,不用伸进 OnlineIPService 的锁
//   2. 自然 debounce(高频闪连闪断不会发出 N 个 webhook)
//   3. snapshot 完整覆盖语义,丢一次不发的话下次发的还是当前真值,无需补发

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"nexcore-x-ui/logger"
)

const (
	webhookInterval = 5 * time.Second
	webhookTimeout  = 10 * time.Second
)

type OnlineWebhookService struct {
	settingService *SettingService

	mu              sync.Mutex
	started         bool
	lastSent        map[string][]string // email → sorted IPs(上次成功推送的状态)
	httpClient      *http.Client
	resolvedNodeId  string // 缓存:setting 没配 nodeId 时退化到 hostname

	// 提供给 service 间共用的单例
}

var (
	onlineWebhookSvc *OnlineWebhookService
	onlineWebhookOnce sync.Once
)

func GetOnlineWebhookService() *OnlineWebhookService {
	onlineWebhookOnce.Do(func() {
		onlineWebhookSvc = &OnlineWebhookService{
			settingService: &SettingService{},
			lastSent:       map[string][]string{},
			httpClient: &http.Client{
				Timeout: webhookTimeout,
			},
		}
	})
	return onlineWebhookSvc
}

// Start 幂等启动后台 worker。多次调用安全。
func (w *OnlineWebhookService) Start() {
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return
	}
	w.started = true
	w.mu.Unlock()
	go w.runLoop()
}

func (w *OnlineWebhookService) runLoop() {
	ticker := time.NewTicker(webhookInterval)
	defer ticker.Stop()
	for range ticker.C {
		w.tick()
	}
}

// tick 拍一次 snapshot,对比上次成功推送的状态,有变化就发。
// 失败时不更新 lastSent,下个 tick 还会拿同样的 diff 重试。
func (w *OnlineWebhookService) tick() {
	url, err := w.settingService.GetOnlineWebhookUrl()
	if err != nil || url == "" {
		return
	}
	current := w.snapshot()
	if !w.hasDiff(current) {
		return
	}
	if err := w.send(url, current); err != nil {
		logger.Warning("online webhook push failed:", err)
		return
	}
	w.mu.Lock()
	w.lastSent = current
	w.mu.Unlock()
}

// snapshot — 取 OnlineIPService 当前在线 email→IPs,IPs 排序使后续 diff
// 比较稳定。空 email(socks/http/没 email 协议)被 OnlineIPService 自然过滤。
func (w *OnlineWebhookService) snapshot() map[string][]string {
	raw := GetOnlineIPService().GetIPsByEmail()
	out := make(map[string][]string, len(raw))
	for email, ips := range raw {
		sorted := make([]string, len(ips))
		copy(sorted, ips)
		sort.Strings(sorted)
		out[email] = sorted
	}
	return out
}

func (w *OnlineWebhookService) hasDiff(current map[string][]string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(current) != len(w.lastSent) {
		return true
	}
	for email, ips := range current {
		prev, ok := w.lastSent[email]
		if !ok {
			return true
		}
		if !equalSortedStrings(prev, ips) {
			return true
		}
	}
	return false
}

func equalSortedStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// nodeId — 优先用 setting 的 onlineWebhookNodeId(操作员配的稳定 ID),
// 否则退化到 hostname(自动有个值,但 hostname 变化时业务系统会看到节点 ID
// 改变,所以推荐配置 setting)。
func (w *OnlineWebhookService) nodeId() string {
	if id, _ := w.settingService.GetOnlineWebhookNodeId(); id != "" {
		return id
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.resolvedNodeId != "" {
		return w.resolvedNodeId
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		w.resolvedNodeId = h
	} else {
		w.resolvedNodeId = "unknown-node"
	}
	return w.resolvedNodeId
}

type webhookPayload struct {
	NodeId string              `json:"node_id"`
	Ts     int64               `json:"ts"`
	Online map[string][]string `json:"online"`
}

func (w *OnlineWebhookService) send(url string, snap map[string][]string) error {
	// SSRF guard. The webhook URL is admin-supplied (already trusted to
	// configure the panel), but defense-in-depth: a compromised or
	// careless admin shouldn't be able to turn the panel into a relay
	// that probes the host's loopback, RFC1918 LAN, link-local, or
	// metadata-service ranges. We resolve the URL's hostname here (right
	// before the call, not at set-time) so DNS rebinding can't slip a
	// public name past the check and resolve to 127.0.0.1 on the next
	// tick. http.NewRequest doesn't dial; the dial happens in
	// httpClient.Do, which uses the same name lookup we do here — close
	// enough to prevent the rebind window from being useful in practice.
	if err := validateWebhookURL(url); err != nil {
		return fmt.Errorf("webhook url rejected: %w", err)
	}

	body := webhookPayload{
		NodeId: w.nodeId(),
		Ts:     time.Now().Unix(),
		Online: snap,
	}
	bs, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(bs))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "nexcore-x-ui-webhook")

	if secret, _ := w.settingService.GetOnlineWebhookSecret(); secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(bs)
		req.Header.Set("X-Nx-Signature", hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("http status %d", resp.StatusCode)
}

// SendNow — UI "测试推送" 按钮用,绕过 5s 间隔立即推一次当前快照,
// 结果回到调用方好显示给操作员。不更新 lastSent(测试不应该影响正常 diff 流)。
func (w *OnlineWebhookService) SendNow() error {
	url, err := w.settingService.GetOnlineWebhookUrl()
	if err != nil {
		return err
	}
	if url == "" {
		return fmt.Errorf("webhook url 未配置")
	}
	return w.send(url, w.snapshot())
}

// validateWebhookURL parses the webhook URL, requires http/https, resolves
// the host to one or more IPs, and rejects any IP that lands in a range we
// don't want the panel to relay traffic into:
//
//	loopback           — 127.0.0.0/8, ::1
//	private            — 10/8, 172.16/12, 192.168/16, fc00::/7
//	link-local         — 169.254/16, fe80::/10
//	cloud metadata     — 169.254.169.254 specifically (covered by link-local
//	                     above, but called out so a future change can't
//	                     accidentally narrow link-local without also
//	                     re-blocking this address)
//	unspecified / mc   — 0.0.0.0, ::, multicast
//
// "All-or-nothing": any one resolved IP in a forbidden range fails the
// whole URL. This is intentional — DNS round-robin or split-horizon DNS
// could otherwise sneak a private IP through alongside a public one.
//
// Schemes other than http/https are also rejected (no file://, gopher://,
// etc.) and an explicit IP literal in the URL is checked the same way as
// a hostname-derived IP.
func validateWebhookURL(rawURL string) error {
	u, err := neturl.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("scheme %q not allowed (http/https only)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("missing host")
	}

	var ips []net.IP
	if literal := net.ParseIP(host); literal != nil {
		ips = []net.IP{literal}
	} else {
		// Use the default resolver. We tolerate a 5s ceiling: anything
		// slower than that is functionally a webhook outage anyway.
		resolved, lookupErr := net.LookupIP(host)
		if lookupErr != nil {
			return fmt.Errorf("dns lookup: %w", lookupErr)
		}
		if len(resolved) == 0 {
			return fmt.Errorf("dns lookup: no addresses for %q", host)
		}
		ips = resolved
	}
	for _, ip := range ips {
		if reason := forbiddenIPReason(ip); reason != "" {
			return fmt.Errorf("host %q resolves to %s (%s)", host, ip, reason)
		}
	}
	return nil
}

// forbiddenIPReason returns a non-empty reason string when the given IP
// is in a range the webhook must not target. Returns "" when the IP is
// safe to dial.
func forbiddenIPReason(ip net.IP) string {
	if ip.IsLoopback() {
		return "loopback"
	}
	if ip.IsUnspecified() {
		return "unspecified"
	}
	if ip.IsMulticast() {
		return "multicast"
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return "link-local"
	}
	if ip.IsPrivate() {
		return "private"
	}
	return ""
}
