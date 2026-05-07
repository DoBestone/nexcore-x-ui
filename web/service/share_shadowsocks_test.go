package service

// 回归测试:SS-2022 multi-user 模式下,LinksByEmail / linksForLoadedInbound
// 必须给每个 email 客户端各发一条三段式 ss:// 链接(method:server_psk:user_psk
// 整体 base64-url 进 userinfo)。
//
// v2.0.6 之前的实现 LinksByEmail switch 漏写 case model.Shadowsocks 整支,
// 行内"二维码"按钮永远拿不到链接,前端报"该客户端没有可生成的链接";
// linksForLoadedInbound SS 分支只取 settings 顶层 password,等于只发了一条
// 服务端 PSK,客户端拿去握手鉴权失败,表现为软件超时。
//
// 这两条都是同一个 buildSS2022UserLink 路径修的,所以放在一起回归。

import (
	"encoding/base64"
	"strings"
	"testing"

	"nexcore-x-ui/database/model"
)

func ss2022Inbound() *model.Inbound {
	return &model.Inbound{
		Id:       2,
		Port:     10001,
		Protocol: model.Shadowsocks,
		Tag:      "inbound-10001",
		Enable:   true,
		Remark:   "node-ss",
		// 真实 panel 里 settings 长这样:method + server PSK 在顶层,clients[]
		// 每个客户端各自带一份 user PSK + email。
		Settings: `{
			"method": "2022-blake3-aes-128-gcm",
			"password": "SERVER_PSK_BASE64",
			"clients": [
				{"email": "alice", "password": "ALICE_PSK_BASE64"},
				{"email": "bob",   "password": "BOB_PSK_BASE64"}
			]
		}`,
		StreamSettings: `{"network":"tcp"}`,
	}
}

// 把 ss://<userinfo>@host:port#tag 里的 userinfo 段解出来,断言三段式。
func decodeSSUserinfo(t *testing.T, link string) string {
	t.Helper()
	if !strings.HasPrefix(link, "ss://") {
		t.Fatalf("expected ss:// scheme, got: %s", link)
	}
	rest := strings.TrimPrefix(link, "ss://")
	at := strings.IndexByte(rest, '@')
	if at < 0 {
		t.Fatalf("no @ in ss link: %s", link)
	}
	enc := rest[:at]
	raw, err := base64.URLEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("userinfo not valid base64-url (%v): %s", err, enc)
	}
	return string(raw)
}

// SS-2022 多用户:LinksByEmail 必须每个客户端各发一条,链接 userinfo 是
// method:server_psk:user_psk 三段。这是修复的核心契约。
func TestShareLink_SS2022_LinksByEmail(t *testing.T) {
	in := ss2022Inbound()
	links, err := (&ShareService{}).linksByEmailFromLoadedInbound(in, "node.example.com")
	if err != nil {
		t.Fatalf("linksByEmailFromLoadedInbound: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("want 2 entries (alice + bob), got %d: %v", len(links), links)
	}

	cases := map[string]string{
		"alice": "2022-blake3-aes-128-gcm:SERVER_PSK_BASE64:ALICE_PSK_BASE64",
		"bob":   "2022-blake3-aes-128-gcm:SERVER_PSK_BASE64:BOB_PSK_BASE64",
	}
	for email, wantUI := range cases {
		link, ok := links[email]
		if !ok {
			t.Fatalf("missing link for %s", email)
		}
		gotUI := decodeSSUserinfo(t, link)
		if gotUI != wantUI {
			t.Errorf("%s userinfo = %q, want %q", email, gotUI, wantUI)
		}
		// tag 必须是 email,客户端列表里才能看出是谁。
		if !strings.HasSuffix(link, "#"+email) {
			t.Errorf("%s link should end with #%s, got: %s", email, email, link)
		}
	}
}

// linksForLoadedInbound 在 SS-2022 multi-user 模式下也必须每个 client 各
// 发一条;早先只发服务端 PSK 那一条会让订阅同样不可用。
func TestShareLink_SS2022_LinksForLoadedInbound_MultiUser(t *testing.T) {
	in := ss2022Inbound()
	links, err := (&ShareService{}).linksForLoadedInbound(in, "node.example.com")
	if err != nil {
		t.Fatalf("linksForLoadedInbound: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("want 2 SS-2022 multi-user links, got %d: %v", len(links), links)
	}
	// 每条都得是合法的三段式;具体哪条对应哪个 user 顺序由 map 决定不固定,
	// 这里集合断言够用。
	wantUIs := map[string]bool{
		"2022-blake3-aes-128-gcm:SERVER_PSK_BASE64:ALICE_PSK_BASE64": false,
		"2022-blake3-aes-128-gcm:SERVER_PSK_BASE64:BOB_PSK_BASE64":   false,
	}
	for _, link := range links {
		ui := decodeSSUserinfo(t, link)
		if _, ok := wantUIs[ui]; !ok {
			t.Errorf("unexpected userinfo: %q", ui)
		}
		wantUIs[ui] = true
	}
	for ui, seen := range wantUIs {
		if !seen {
			t.Errorf("missing userinfo: %q", ui)
		}
	}
}

// SS-2022 单用户(没 clients[])仍然走 buildSSLink 那条老路径:userinfo 只
// 有 method:password 两段,不应该误报多用户路径。
func TestShareLink_SS2022_SingleUser(t *testing.T) {
	in := &model.Inbound{
		Id:       3,
		Port:     10002,
		Protocol: model.Shadowsocks,
		Tag:      "inbound-10002",
		Enable:   true,
		Remark:   "single",
		Settings: `{"method":"2022-blake3-aes-256-gcm","password":"ONLY_PSK_BASE64"}`,
	}
	links, err := (&ShareService{}).linksForLoadedInbound(in, "node.example.com")
	if err != nil {
		t.Fatalf("linksForLoadedInbound: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("want 1 link for single-user SS-2022, got %d", len(links))
	}
	ui := decodeSSUserinfo(t, links[0])
	want := "2022-blake3-aes-256-gcm:ONLY_PSK_BASE64"
	if ui != want {
		t.Errorf("single-user SS userinfo = %q, want %q", ui, want)
	}
}
