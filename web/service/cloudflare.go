package service

// Cloudflare API 控制层 —— 让面板能读 / 改某个域名记录的 proxied 字段
// (橙云 / 灰云)。
//
// 用同一把 CF API token(Zone:DNS:Edit 权限),DNS-01 取证书也用它。token
// 加密存在 settings 表,接口前从 settingService 现取,不暂存在内存里。
//
// API 路径走 v4:
//   GET    /zones?name=<domain>          列 token 范围内匹配的 zone
//   GET    /zones/<id>/dns_records?name= 找具体记录
//   PATCH  /zones/<id>/dns_records/<rid> 改 proxied / content / ttl
//
// 失败统一返回 ErrCfXxx 给上层映射成 jsonMsg 中文友好提示。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const cfBaseURL = "https://api.cloudflare.com/client/v4"

var (
	ErrCfTokenMissing = errors.New("未配置 CF API token,请先去面板设置填好")
	ErrCfZoneNotFound = errors.New("未找到该域名所属的 zone(token 没覆盖,或域名拼错)")
	ErrCfRecordNotFound = errors.New("zone 里没找到该 A/AAAA/CNAME 记录")
)

type CloudflareService struct {
	settingService SettingService
}

// CFRecordState 是给前端的扁平状态。Type 通常 A/AAAA/CNAME。Proxied 是
// 我们关心的核心字段(true=橙云,false=灰云/DNS-only)。
type CFRecordState struct {
	Domain    string `json:"domain"`
	ZoneID    string `json:"zoneId"`
	ZoneName  string `json:"zoneName"`
	RecordID  string `json:"recordId"`
	Type      string `json:"type"`
	Content   string `json:"content"`
	Proxied   bool   `json:"proxied"`
}

// GetRecordState 抓单条 A/AAAA/CNAME 记录的当前状态。Domain 可以是 zone
// apex(example.com)也可以是子域(node1.example.com)。逻辑:
//   1) 取 token
//   2) 调 /zones?name=domain;命中 → 用它的 zoneId,记录是 zone apex
//      未命中 → 拿 domain 的父级后缀 example.com 当 zone name 重试
//   3) 在 zone 里查 /dns_records?name=domain,挑一条 A/AAAA/CNAME
func (s *CloudflareService) GetRecordState(domain string) (*CFRecordState, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return nil, errors.New("域名不能为空")
	}
	token := s.settingService.GetCfApiToken()
	if token == "" {
		return nil, ErrCfTokenMissing
	}

	zoneID, zoneName, err := s.findZone(token, domain)
	if err != nil {
		return nil, err
	}

	// 找 dns_record。type 不限,前端理论上只关心 A/AAAA/CNAME(代理生效
	// 在这三种上),其它(MX/TXT/...)不暴露。
	type recItem struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Name    string `json:"name"`
		Content string `json:"content"`
		Proxied bool   `json:"proxied"`
	}
	var lr struct {
		Success bool      `json:"success"`
		Errors  []cfError `json:"errors"`
		Result  []recItem `json:"result"`
	}
	if err := s.cfGet(token,
		fmt.Sprintf("/zones/%s/dns_records?name=%s", zoneID, domain), &lr); err != nil {
		return nil, err
	}
	if !lr.Success {
		return nil, fmt.Errorf("CF API: %s", joinCfErrors(lr.Errors))
	}
	for _, r := range lr.Result {
		if r.Type == "A" || r.Type == "AAAA" || r.Type == "CNAME" {
			return &CFRecordState{
				Domain:   domain,
				ZoneID:   zoneID,
				ZoneName: zoneName,
				RecordID: r.ID,
				Type:     r.Type,
				Content:  r.Content,
				Proxied:  r.Proxied,
			}, nil
		}
	}
	return nil, ErrCfRecordNotFound
}

// SetProxied 切换 proxied 字段。需要先 GetRecordState 拿到 record id 再
// PATCH;调用方一般把当前态展示给用户、用户点 toggle 后调这条。返回更新
// 后的状态,用于前端立刻刷新 UI(不必再 GET 一遍)。
func (s *CloudflareService) SetProxied(domain string, proxied bool) (*CFRecordState, error) {
	cur, err := s.GetRecordState(domain)
	if err != nil {
		return nil, err
	}
	if cur.Proxied == proxied {
		// no-op，不发 PATCH 浪费
		return cur, nil
	}
	token := s.settingService.GetCfApiToken()
	body, _ := json.Marshal(map[string]interface{}{"proxied": proxied})
	var pr struct {
		Success bool      `json:"success"`
		Errors  []cfError `json:"errors"`
		Result  struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Name    string `json:"name"`
			Content string `json:"content"`
			Proxied bool   `json:"proxied"`
		} `json:"result"`
	}
	if err := s.cfPatch(token,
		fmt.Sprintf("/zones/%s/dns_records/%s", cur.ZoneID, cur.RecordID),
		body, &pr); err != nil {
		return nil, err
	}
	if !pr.Success {
		return nil, fmt.Errorf("CF API: %s", joinCfErrors(pr.Errors))
	}
	cur.Proxied = pr.Result.Proxied
	cur.Content = pr.Result.Content
	cur.Type = pr.Result.Type
	return cur, nil
}

// findZone 从 domain 找它所属的 zone。先尝试 domain 本身做 zone(适合
// "example.com" 直接绑根域),不命中就剥一层后缀往上试一次("a.b.example.com"
// → 试 "b.example.com" → "example.com")。绝大多数场景两轮内能命中。
func (s *CloudflareService) findZone(token, domain string) (string, string, error) {
	tries := []string{domain}
	parts := strings.Split(domain, ".")
	for i := 1; i < len(parts)-1; i++ {
		tries = append(tries, strings.Join(parts[i:], "."))
	}
	for _, name := range tries {
		var zr struct {
			Success bool      `json:"success"`
			Errors  []cfError `json:"errors"`
			Result  []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"result"`
		}
		if err := s.cfGet(token, "/zones?name="+name, &zr); err != nil {
			return "", "", err
		}
		if !zr.Success {
			return "", "", fmt.Errorf("CF API: %s", joinCfErrors(zr.Errors))
		}
		if len(zr.Result) > 0 {
			return zr.Result[0].ID, zr.Result[0].Name, nil
		}
	}
	return "", "", ErrCfZoneNotFound
}

// ---- HTTP helpers ----

type cfError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func joinCfErrors(errs []cfError) string {
	if len(errs) == 0 {
		return "unknown error"
	}
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		parts = append(parts, fmt.Sprintf("%d:%s", e.Code, e.Message))
	}
	return strings.Join(parts, "; ")
}

func (s *CloudflareService) cfDo(method, token, path string, body []byte, out interface{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, cfBaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("CF API 网络错误: %w", err)
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	// 401 / 403 直说"token 不对",省得回 generic API: 9109
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return fmt.Errorf("CF token 鉴权失败 (HTTP %d)", resp.StatusCode)
	}
	if err := json.Unmarshal(bodyBytes, out); err != nil {
		return fmt.Errorf("CF API 响应解析失败: %w (body=%s)", err, string(bodyBytes))
	}
	return nil
}

func (s *CloudflareService) cfGet(token, path string, out interface{}) error {
	return s.cfDo(http.MethodGet, token, path, nil, out)
}

func (s *CloudflareService) cfPatch(token, path string, body []byte, out interface{}) error {
	return s.cfDo(http.MethodPatch, token, path, body, out)
}
