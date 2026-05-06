package service

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"x-ui/database/model"
)

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
		// Single-key SS: settings has password+method directly.
		settings := map[string]any{}
		_ = json.Unmarshal([]byte(in.Settings), &settings)
		method, _ := settings["method"].(string)
		password, _ := settings["password"].(string)
		if method != "" && password != "" {
			out = append(out, buildSSLink(in, host, method, password))
		}
	default:
		return nil, ErrUnsupportedProtocol
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
		links, err := s.LinksForInbound(in.Id, host)
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
