package service

// 单测覆盖 stripDisabledClientsFromSettings —— xray 配置构建时把 disabled
// client 从 settings.clients[] 剥掉。这是修 v2.0.x "client toggle 关了
// 但实际还能用" 的关键链路,回归点要 pin 死。

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

func TestStripDisabled_RemovesNamedEmail(t *testing.T) {
	// VLESS 形态:clients 里多个 email,只剥被禁那一个,其它字段(decryption /
	// fallbacks 等顶层字段)原样保留。
	settings := `{"clients":[
        {"id":"u1","email":"alice","flow":""},
        {"id":"u2","email":"bob","flow":""},
        {"id":"u3","email":"charlie","flow":""}
    ],"decryption":"none","fallbacks":[]}`

	disabled := map[string]struct{}{"bob": {}}
	out := stripDisabledClientsFromSettings(settings, disabled)

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output not valid json: %v", err)
	}
	clients, _ := parsed["clients"].([]interface{})
	emails := []string{}
	for _, c := range clients {
		if m, ok := c.(map[string]interface{}); ok {
			emails = append(emails, m["email"].(string))
		}
	}
	sort.Strings(emails)
	want := []string{"alice", "charlie"}
	if !reflect.DeepEqual(emails, want) {
		t.Errorf("emails = %v, want %v", emails, want)
	}
	// 顶层字段保留
	if _, ok := parsed["decryption"]; !ok {
		t.Error("decryption stripped — should preserve all non-clients keys")
	}
}

func TestStripDisabled_NoChangeWhenNoneMatch(t *testing.T) {
	// disabled 列表跟 inbound 里 email 没交集 → 返回原 string(不重新序列化,
	// 避免每次 reload xray 看到 "settings 变了" 触发不必要的重启)。
	settings := `{"clients":[{"id":"u1","email":"alice"}],"decryption":"none"}`
	disabled := map[string]struct{}{"bob": {}}
	out := stripDisabledClientsFromSettings(settings, disabled)
	if out != settings {
		t.Errorf("output should equal input when nothing matched, got: %s", out)
	}
}

func TestStripDisabled_EmptyInput(t *testing.T) {
	// 空 settings(socks/http/dokodemo 这些非客户端模型)/ 空 disabled 都应当
	// 直接返回原值,函数零成本快路径。
	if got := stripDisabledClientsFromSettings("", map[string]struct{}{"x": {}}); got != "" {
		t.Errorf("empty settings should pass through, got %q", got)
	}
	settings := `{"clients":[{"id":"u1","email":"alice"}]}`
	if got := stripDisabledClientsFromSettings(settings, map[string]struct{}{}); got != settings {
		t.Errorf("empty disabled set should pass through")
	}
}

func TestStripDisabled_NonClientProtocols(t *testing.T) {
	// SS-legacy / Socks / HTTP / Dokodemo 没 clients[] 结构,过滤跑空,
	// 原样返回 — 保证非订阅协议不被这条逻辑误伤。
	cases := []string{
		`{"method":"chacha20-ietf-poly1305","password":"abc"}`,
		`{"auth":"password","accounts":[{"user":"u","pass":"p"}],"udp":true}`,
		`{"address":"127.0.0.1","port":53,"network":"udp"}`,
	}
	disabled := map[string]struct{}{"alice": {}}
	for _, s := range cases {
		got := stripDisabledClientsFromSettings(s, disabled)
		if got != s {
			t.Errorf("non-client settings should pass through, got %q for input %q", got, s)
		}
	}
}

func TestStripDisabled_SS2022MultiUser(t *testing.T) {
	// SS-2022 multi-user 的 settings 形如:
	//   {"method":"2022-blake3-...","password":"server-psk","clients":[
	//     {"password":"alice-psk","email":"alice"}, ...]}
	// 顶层 method/password 是服务端 PSK,不能动;只过滤 clients 数组。
	settings := `{"method":"2022-blake3-aes-128-gcm","password":"SERVER==","clients":[
        {"password":"alicepsk==","email":"alice"},
        {"password":"bobpsk==","email":"bob"}
    ]}`
	disabled := map[string]struct{}{"alice": {}}
	out := stripDisabledClientsFromSettings(settings, disabled)

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed["password"] != "SERVER==" {
		t.Errorf("server psk got rewritten: %v", parsed["password"])
	}
	clients := parsed["clients"].([]interface{})
	if len(clients) != 1 {
		t.Fatalf("expected 1 client, got %d", len(clients))
	}
	if email := clients[0].(map[string]interface{})["email"]; email != "bob" {
		t.Errorf("wrong client kept: %v", email)
	}
}
