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
	"net/http"
	"os"
	"sort"
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
