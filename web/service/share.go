package service

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"

	"nexcore-x-ui/database/model"
)

// ValidateShareHostSyntactic enforces format constraints on the host
// component of a share link. It rejects:
//   - empty / >253 chars
//   - whitespace, CRLF, '#', '?', '/', '\\' (link / header injection)
//   - invalid IP literal in [..]
//   - hostname labels with empty / >63 chars / non-[a-zA-Z0-9-_] / leading-trailing '-'
//
// Returns (cleaned_host, "") on success or ("", reason) on rejection.
// No whitelist check — callers that want to enforce settings.subAllowedHosts
// do that themselves on top of this helper. Both the panel-session path
// (api_panel.go::inboundLinks, where Host is c.Request.Host) and the
// API-token path (api/v1.go::validateShareHost, where host is the
// caller-supplied query) funnel through this function so the syntactic
// invariants stay in one place.
func ValidateShareHostSyntactic(host string) (string, string) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", "host parameter is required"
	}
	if len(host) > 253 {
		return "", "host too long"
	}
	for _, r := range host {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' || r == '#' || r == '?' || r == '/' || r == '\\' {
			return "", "host contains forbidden characters"
		}
	}
	candidate := host
	if strings.HasPrefix(candidate, "[") && strings.HasSuffix(candidate, "]") {
		if ip := net.ParseIP(candidate[1 : len(candidate)-1]); ip == nil || ip.To16() == nil {
			return "", "invalid IPv6 literal"
		}
	} else if ip := net.ParseIP(candidate); ip == nil {
		for _, label := range strings.Split(candidate, ".") {
			if label == "" || len(label) > 63 {
				return "", "invalid hostname label"
			}
			for i, r := range label {
				ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
				if !ok {
					return "", "invalid character in hostname"
				}
				if (i == 0 || i == len(label)-1) && r == '-' {
					return "", "hostname label cannot start/end with hyphen"
				}
			}
		}
	}
	return host, ""
}

// ShareService renders xray inbound rows into client-share URIs
// (vmess://, vless://, trojan://, ss://). The server address used in the
// link must be supplied by the caller — the panel cannot reliably guess it.
type ShareService struct {
	inboundService InboundService
	clientService  ClientService
}

// LinksForInbound returns one link per client inside the inbound, plus a
// flat list of plain text the caller can stuff into a subscription file.
// Returns ErrUnsupportedProtocol for protocols we cannot render (Dokodemo,
// HTTP relay etc.).
func (s *ShareService) LinksForInbound(inboundID int, host string) ([]string, error) {
	in, err := s.inboundService.GetInbound(inboundID)
	if err != nil {
		return nil, err
	}
	return s.linksForLoadedInbound(in, host)
}

// linksForLoadedInbound is the actual link generator. SubscriptionForAll
// uses it to skip the redundant per-inbound GetInbound() call that
// otherwise turns one /subscription request into 2*N+1 sqlite reads.
func (s *ShareService) linksForLoadedInbound(in *model.Inbound, host string) ([]string, error) {
	clients, _, err := readClients(in)
	if err != nil {
		return nil, err
	}

	stream := map[string]any{}
	if in.StreamSettings != "" {
		_ = json.Unmarshal([]byte(in.StreamSettings), &stream)
	}

	out := make([]string, 0, len(clients))
	switch in.Protocol {
	case model.VMess:
		for _, c := range clients {
			if link := buildVMessLink(in, host, c, stream); link != "" {
				out = append(out, link)
			}
		}
	case model.VLESS:
		for _, c := range clients {
			if link := buildVLESSLink(in, host, c, stream); link != "" {
				out = append(out, link)
			}
		}
	case model.Trojan:
		for _, c := range clients {
			if link := buildTrojanLink(in, host, c, stream); link != "" {
				out = append(out, link)
			}
		}
	case model.Shadowsocks:
		// Two shapes share this protocol:
		//   1) legacy / SS-2022 single-user: settings 顶层 method+password,
		//      clients[] 不存在或为空,产物只有 1 条 inbound 级别的链接。
		//   2) SS-2022 multi-user (method 以 2022-blake3- 开头,clients[] 非空):
		//      每个 client 有自己的 password,链接的 userinfo 是
		//      method:server_psk:user_psk —— 早先这里只发 1 条服务器侧 PSK
		//      链接,客户端拿去根本无法鉴权,等于把订阅废了。
		settings := map[string]any{}
		_ = json.Unmarshal([]byte(in.Settings), &settings)
		method, _ := settings["method"].(string)
		serverPSK, _ := settings["password"].(string)
		switch {
		case strings.HasPrefix(method, "2022-blake3-") && len(clients) > 0:
			for _, c := range clients {
				email, _ := c["email"].(string)
				userPSK, _ := c["password"].(string)
				if email == "" || userPSK == "" {
					continue
				}
				out = append(out, buildSS2022UserLink(in, host, method, serverPSK, userPSK, email))
			}
		case method != "" && serverPSK != "":
			out = append(out, buildSSLink(in, host, method, serverPSK))
		}
	default:
		return nil, ErrUnsupportedProtocol
	}
	return out, nil
}

// LinksByEmail returns email → share-link, one entry per email-bearing client.
// 客户端流量 modal 行内"二维码"按钮调它:面板根据当前 email 行从 map
// 取一条链接喂给 qrModal,不需要额外 query 参数。SS-legacy 这种没 email
// 的协议天然不出现在 map 里(本来 modal 也不展示)。
//
// DB 入口 + 纯计算分开:测试和 SubscriptionForAll-style 已加载场景都能
// 直接调 linksByEmailFromLoadedInbound 而不需要 mock InboundService。
func (s *ShareService) LinksByEmail(inboundID int, host string) (map[string]string, error) {
	in, err := s.inboundService.GetInbound(inboundID)
	if err != nil {
		return nil, err
	}
	return s.linksByEmailFromLoadedInbound(in, host)
}

func (s *ShareService) linksByEmailFromLoadedInbound(in *model.Inbound, host string) (map[string]string, error) {
	clients, _, err := readClients(in)
	if err != nil {
		return nil, err
	}
	stream := map[string]any{}
	if in.StreamSettings != "" {
		_ = json.Unmarshal([]byte(in.StreamSettings), &stream)
	}
	// SS-2022 multi-user 走 settings 顶层 method + server psk;只在 SS 入站
	// 才需要解一次,提到循环外避免每个 client 重复 parse。
	ssSettings := map[string]any{}
	if in.Protocol == model.Shadowsocks && in.Settings != "" {
		_ = json.Unmarshal([]byte(in.Settings), &ssSettings)
	}
	out := make(map[string]string, len(clients))
	for _, c := range clients {
		email, _ := c["email"].(string)
		if email == "" {
			continue
		}
		var link string
		switch in.Protocol {
		case model.VMess:
			link = buildVMessLink(in, host, c, stream)
		case model.VLESS:
			link = buildVLESSLink(in, host, c, stream)
		case model.Trojan:
			link = buildTrojanLink(in, host, c, stream)
		case model.Shadowsocks:
			// Only SS-2022 multi-user has per-email clients with their own
			// passwords. Legacy SS doesn't carry clients[]/email at all,
			// so it never reaches here (the loop body skips empty email).
			method, _ := ssSettings["method"].(string)
			if !strings.HasPrefix(method, "2022-blake3-") {
				break
			}
			serverPSK, _ := ssSettings["password"].(string)
			userPSK, _ := c["password"].(string)
			if userPSK == "" {
				break
			}
			link = buildSS2022UserLink(in, host, method, serverPSK, userPSK, email)
		}
		if link != "" {
			out[email] = link
		}
	}
	return out, nil
}

// SubscriptionForInbound returns a base64-encoded blob of all share links —
// the format consumed by every standard proxy client (V2RayN, Shadowrocket).
func (s *ShareService) SubscriptionForInbound(inboundID int, host string) (string, error) {
	links, err := s.LinksForInbound(inboundID, host)
	if err != nil {
		return "", err
	}
	joined := strings.Join(links, "\n")
	return base64.StdEncoding.EncodeToString([]byte(joined)), nil
}

// SubscriptionForAll merges links from every inbound into one subscription.
// Disabled inbounds are skipped so revoked nodes drop out of clients on poll.
//
// Single-DB-query path: GetAllInbounds gives us already-hydrated rows;
// linksForLoadedInbound consumes them directly instead of re-querying
// per row. For a panel with N enabled inbounds this collapses 2N+1 DB
// calls into 1 — measurable on a 50-inbound deployment.
func (s *ShareService) SubscriptionForAll(host string) (string, error) {
	items, err := s.inboundService.GetAllInbounds()
	if err != nil {
		return "", err
	}
	all := make([]string, 0, len(items))
	for _, in := range items {
		if !in.Enable {
			continue
		}
		links, err := s.linksForLoadedInbound(in, host)
		if err != nil {
			continue
		}
		all = append(all, links...)
	}
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(all, "\n"))), nil
}

// ---------- per-protocol builders ----------

func buildVMessLink(in *model.Inbound, host string, c map[string]any, stream map[string]any) string {
	id, _ := c["id"].(string)
	if id == "" {
		return ""
	}
	remark := vmessRemark(in, c)
	obj := map[string]any{
		"v":    "2",
		"ps":   remark,
		"add":  host,
		"port": in.Port,
		"id":   id,
		"aid":  intOrZero(c["alterId"]),
		"net":  strOr(stream["network"], "tcp"),
		"type": "none",
		"host": "",
		"path": "",
		"tls":  "",
	}
	mergeStreamFields(stream, obj)
	if security, _ := stream["security"].(string); security != "" {
		obj["tls"] = security
	}
	raw, _ := json.Marshal(obj)
	return "vmess://" + base64.StdEncoding.EncodeToString(raw)
}

func buildVLESSLink(in *model.Inbound, host string, c map[string]any, stream map[string]any) string {
	id, _ := c["id"].(string)
	if id == "" {
		return ""
	}
	q := url.Values{}
	if v, ok := c["flow"].(string); ok && v != "" {
		q.Set("flow", v)
	}
	q.Set("type", strOr(stream["network"], "tcp"))
	if security, _ := stream["security"].(string); security != "" {
		q.Set("security", security)
	}
	mergeStreamQuery(stream, q)
	remark := url.PathEscape(strDefault(c["email"], in.Remark))
	return fmt.Sprintf("vless://%s@%s:%d?%s#%s", id, host, in.Port, q.Encode(), remark)
}

func buildTrojanLink(in *model.Inbound, host string, c map[string]any, stream map[string]any) string {
	password, _ := c["password"].(string)
	if password == "" {
		return ""
	}
	q := url.Values{}
	q.Set("type", strOr(stream["network"], "tcp"))
	if security, _ := stream["security"].(string); security != "" {
		q.Set("security", security)
	}
	mergeStreamQuery(stream, q)
	remark := url.PathEscape(strDefault(c["email"], in.Remark))
	return fmt.Sprintf("trojan://%s@%s:%d?%s#%s",
		url.QueryEscape(password), host, in.Port, q.Encode(), remark)
}

func buildSSLink(in *model.Inbound, host string, method, password string) string {
	userInfo := base64.URLEncoding.EncodeToString([]byte(method + ":" + password))
	remark := url.PathEscape(in.Remark)
	return fmt.Sprintf("ss://%s@%s:%d#%s", userInfo, host, in.Port, remark)
}

// buildSS2022UserLink 给 SS-2022 multi-user 模式生成单个 email 客户端的
// 分享链接。userinfo 段是 method:server_psk:user_psk(冒号分隔三段后整体
// base64-url),tag 用 email 方便客户端识别。
//
// 这是与 buildSSLink 的关键区别:legacy / 单用户 SS 只有 method:password
// 两段;多用户协议必须把服务端 psk 和用户 psk 都带上,客户端才能 derive
// 出正确的会话密钥。
func buildSS2022UserLink(in *model.Inbound, host string, method, serverPSK, userPSK, email string) string {
	userInfo := base64.URLEncoding.EncodeToString([]byte(method + ":" + serverPSK + ":" + userPSK))
	return fmt.Sprintf("ss://%s@%s:%d#%s", userInfo, host, in.Port, url.PathEscape(email))
}

// ---------- helpers ----------

func vmessRemark(in *model.Inbound, c map[string]any) string {
	if r, _ := c["email"].(string); r != "" {
		return r
	}
	return in.Remark
}

func mergeStreamFields(stream map[string]any, vmess map[string]any) {
	net, _ := stream["network"].(string)
	switch net {
	case "ws":
		if ws, ok := stream["wsSettings"].(map[string]any); ok {
			if h, ok := ws["host"].(string); ok {
				vmess["host"] = h
			}
			if p, ok := ws["path"].(string); ok {
				vmess["path"] = p
			}
		}
	case "grpc":
		if g, ok := stream["grpcSettings"].(map[string]any); ok {
			if name, ok := g["serviceName"].(string); ok {
				vmess["path"] = name
			}
		}
	case "h2":
		if h, ok := stream["httpSettings"].(map[string]any); ok {
			if hosts, ok := h["host"].([]any); ok && len(hosts) > 0 {
				if hh, ok := hosts[0].(string); ok {
					vmess["host"] = hh
				}
			}
			if p, ok := h["path"].(string); ok {
				vmess["path"] = p
			}
		}
	}
}

func mergeStreamQuery(stream map[string]any, q url.Values) {
	net, _ := stream["network"].(string)
	switch net {
	case "ws":
		if ws, ok := stream["wsSettings"].(map[string]any); ok {
			if h, ok := ws["host"].(string); ok && h != "" {
				q.Set("host", h)
			}
			if p, ok := ws["path"].(string); ok && p != "" {
				q.Set("path", p)
			}
		}
	case "grpc":
		if g, ok := stream["grpcSettings"].(map[string]any); ok {
			if name, ok := g["serviceName"].(string); ok && name != "" {
				q.Set("serviceName", name)
			}
		}
	}
	if reality, ok := stream["realitySettings"].(map[string]any); ok {
		if pbk, _ := reality["publicKey"].(string); pbk != "" {
			q.Set("pbk", pbk)
		}
		if sids, ok := reality["shortIds"].([]any); ok && len(sids) > 0 {
			if s, ok := sids[0].(string); ok {
				q.Set("sid", s)
			}
		}
		if names, ok := reality["serverNames"].([]any); ok && len(names) > 0 {
			if s, ok := names[0].(string); ok {
				q.Set("sni", s)
			}
		}
		if fp, _ := reality["fingerprint"].(string); fp != "" {
			q.Set("fp", fp)
		}
	}
	if tls, ok := stream["tlsSettings"].(map[string]any); ok {
		if sni, _ := tls["serverName"].(string); sni != "" {
			q.Set("sni", sni)
		}
	}
}

func intOrZero(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	}
	return 0
}

func strOr(v any, fallback string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return fallback
}

func strDefault(v any, fallback string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return fallback
}
