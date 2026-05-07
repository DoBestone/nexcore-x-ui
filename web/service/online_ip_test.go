package service

// access.log 解析跨协议巡检。xray 内部所有 inbound 都通过同一份
// `app/log/log.go::AccessMessage.String()` 写日志,基本骨架是:
//
//   <FromIP>:<port> accepted <To>[ [<Detour>]][ [<Reason>]][ email: <Email>]
//
// 其中 Detour 和 Email 字段取决于协议 handler 是否填充:
//
//   VMess / VLESS / Trojan         : Detour 有,Email 有(多用户)。
//   Shadowsocks-2022 multi-user    : Detour 空,Email 有(实测!)。
//   Shadowsocks legacy (single PSK): Detour 有,Email 空(单用户)。
//   SOCKS / HTTP with `accounts[]` : Detour 有,Email = 用户名。
//   SOCKS / HTTP no auth           : Detour 有,Email 空。
//   Dokodemo-door                  : Detour 有("api -> api" 也走这条),Email 空。
//   Wireguard                      : 连接级日志稀疏(包级流量,不写 accept 行),
//                                    走流量 stats 路径,这里跳过。
//
// 设计 invariant:解析三个字段(IP / tag / email)互相独立,缺哪个都
// 不阻断另外两个。本测试覆盖每族协议格式,锁住这条契约 —— v2.0.10 之前
// 的实现把 IP+tag 一捆要求,SS-2022 整行被丢,客户端流量 modal 全员
// 显示"离线"。

import (
	"reflect"
	"sort"
	"testing"
	"time"
)

func newSvc() *OnlineIPService {
	return &OnlineIPService{
		state:      make(map[string]map[string]time.Time),
		byEmail:    make(map[string]map[string]time.Time),
		emailToTag: make(map[string]string),
		resetCh:    make(chan struct{}, 1),
	}
}

// 一族协议一组样本,字段是基于实测 + xray-core AccessMessage.String()
// 推导:Detour 段是不是有 / Email 段是不是有 / network prefix(tcp: / udp:)
// 是不是有,三种独立。每个用例描述目标协议、期望 byEmail / byTag 落点。
type protoSample struct {
	name      string
	line      string
	wantIP    string
	wantTag   string // "" 表示这条行不期望进 inbound-tag map
	wantEmail string // "" 表示这条行不期望进 byEmail map
}

func TestHandleLine_AllXrayInboundProtocols(t *testing.T) {
	cases := []protoSample{
		{
			name:      "VMess (full format with tcp prefix + tag + email)",
			line:      "2026/05/07 14:00:00 from 1.2.3.4:11111 accepted tcp:example.com:443 [inbound-10000 -> direct] email: alice\n",
			wantIP:    "1.2.3.4",
			wantTag:   "inbound-10000",
			wantEmail: "alice",
		},
		{
			name:      "VLESS REALITY (same shape as VMess)",
			line:      "2026/05/07 14:00:01 from 1.2.3.4:11112 accepted tcp:www.cloudflare.com:443 [inbound-443 -> direct] email: bob\n",
			wantIP:    "1.2.3.4",
			wantTag:   "inbound-443",
			wantEmail: "bob",
		},
		{
			name:      "Trojan TLS (same shape)",
			line:      "2026/05/07 14:00:02 from 5.6.7.8:22222 accepted tcp:google.com:443 [inbound-444 -> direct] email: carol\n",
			wantIP:    "5.6.7.8",
			wantTag:   "inbound-444",
			wantEmail: "carol",
		},
		{
			name:      "Shadowsocks-2022 multi-user (no tcp prefix, no tag, with email)",
			line:      "2026/05/07 14:00:03 from 39.144.187.172:61117 accepted 194.221.250.50:80 email: user-645802\n",
			wantIP:    "39.144.187.172",
			wantTag:   "", // 没 tag → 入站维度不靠这行(由 emailToTag 反查兜底,见下面专测)
			wantEmail: "user-645802",
		},
		{
			name:      "Shadowsocks legacy single-user (Detour 有,Email 空)",
			line:      "2026/05/07 14:00:04 from 8.8.8.8:33333 accepted tcp:dest.example.com:80 [inbound-8388 -> direct]\n",
			wantIP:    "8.8.8.8",
			wantTag:   "inbound-8388",
			wantEmail: "", // 单用户 SS 没 email
		},
		{
			name:      "SOCKS with accounts (有 auth → 有 email)",
			line:      "2026/05/07 14:00:05 from 9.9.9.9:44444 accepted tcp:dest.example.com:443 [inbound-1080 -> direct] email: socks-user-1\n",
			wantIP:    "9.9.9.9",
			wantTag:   "inbound-1080",
			wantEmail: "socks-user-1",
		},
		{
			name:      "SOCKS without auth (无 email)",
			line:      "2026/05/07 14:00:06 from 10.10.10.10:55555 accepted tcp:dest.example.com:443 [inbound-1081 -> direct]\n",
			wantIP:    "10.10.10.10",
			wantTag:   "inbound-1081",
			wantEmail: "",
		},
		{
			name:      "HTTP with accounts",
			line:      "2026/05/07 14:00:07 from 11.11.11.11:55556 accepted tcp:dest.example.com:443 [inbound-3128 -> direct] email: http-user-1\n",
			wantIP:    "11.11.11.11",
			wantTag:   "inbound-3128",
			wantEmail: "http-user-1",
		},
		{
			name:      "Dokodemo-door (transparent forward, no email)",
			line:      "2026/05/07 14:00:08 from 12.12.12.12:55557 accepted tcp:dest.example.com:80 [inbound-7777 -> direct]\n",
			wantIP:    "12.12.12.12",
			wantTag:   "inbound-7777",
			wantEmail: "",
		},
		{
			name:      "IPv6 source",
			line:      "2026/05/07 14:00:09 from [2001:db8::1]:11118 accepted tcp:example.com:443 [inbound-10000 -> direct] email: alice\n",
			wantIP:    "2001:db8::1",
			wantTag:   "inbound-10000",
			wantEmail: "alice",
		},
		{
			name:      "UDP destination (network prefix udp:)",
			line:      "2026/05/07 14:00:10 from 13.13.13.13:55559 accepted udp:8.8.8.8:53 [inbound-10001 -> direct] email: alice\n",
			wantIP:    "13.13.13.13",
			wantTag:   "inbound-10001",
			wantEmail: "alice",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSvc()
			s.handleLine(tc.line)

			// IP 维度:tag 期望非空时才该有 inbound-level 落点
			if tc.wantTag != "" {
				ips := s.GetIPs(tc.wantTag)
				sort.Strings(ips)
				if !reflect.DeepEqual(ips, []string{tc.wantIP}) {
					t.Errorf("GetIPs(%s) = %v, want [%s]", tc.wantTag, ips, tc.wantIP)
				}
			} else if all := s.GetAllIPs(); len(all) != 0 {
				t.Errorf("expected no inbound-tag entries (line has no [tag ->] block), got %v", all)
			}

			// Email 维度
			if tc.wantEmail != "" {
				ips := s.GetIPsByEmail()[tc.wantEmail]
				sort.Strings(ips)
				if !reflect.DeepEqual(ips, []string{tc.wantIP}) {
					t.Errorf("GetIPsByEmail()[%s] = %v, want [%s]", tc.wantEmail, ips, tc.wantIP)
				}
			} else if by := s.GetIPsByEmail(); len(by) != 0 {
				t.Errorf("expected no byEmail entries (line has no `email:` field), got %v", by)
			}
		})
	}
}

// emailToTag 反查兜底:SS-2022 行没写 tag,但 emailToTag map 里登记了
// "user-645802 → inbound-10001",handleLine 应该补一条 inbound-tag 维度
// 记录,这样入站卡角标也能涵盖 SS-2022 客户端。
func TestHandleLine_SS2022_FallbackTagFromEmailToTagMap(t *testing.T) {
	s := newSvc()
	s.emailToTag = map[string]string{
		"user-645802": "inbound-10001",
	}
	s.handleLine("2026/05/07 14:12:07.584524 from 39.144.187.172:61117 accepted 194.221.250.50:80 email: user-645802\n")

	got := s.GetIPs("inbound-10001")
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"39.144.187.172"}) {
		t.Errorf("GetIPs(inbound-10001) = %v, want [39.144.187.172] (emailToTag 应该兜底补 inbound 维度)", got)
	}
	by := s.GetIPsByEmail()["user-645802"]
	sort.Strings(by)
	if !reflect.DeepEqual(by, []string{"39.144.187.172"}) {
		t.Errorf("GetIPsByEmail()[user-645802] = %v, want [39.144.187.172]", by)
	}
}

// 反查表里没这个 email 时不能误塞到任何 tag。空 map / 不存在的 email
// 都应该走原路径(没 tag → 不进 state map)。
func TestHandleLine_SS2022_NoEmailToTagEntry(t *testing.T) {
	s := newSvc()
	// emailToTag 空,模拟 boot 早期 / 没 inbound 匹配
	s.handleLine("2026/05/07 14:12:07.584524 from 39.144.187.172:61117 accepted 194.221.250.50:80 email: user-645802\n")

	if all := s.GetAllIPs(); len(all) != 0 {
		t.Errorf("GetAllIPs() = %v, want empty (没 emailToTag 不该硬塞 tag)", all)
	}
	// byEmail 仍然要进
	if got := s.GetIPsByEmail()["user-645802"]; len(got) != 1 {
		t.Errorf("byEmail should still have user-645802, got %v", got)
	}
}

// 行里 tag 是权威 — 即使 emailToTag map 里有 stale 映射,不应该再写一条。
func TestHandleLine_TagFromLineTakesPriorityOverEmailToTag(t *testing.T) {
	s := newSvc()
	s.emailToTag = map[string]string{
		"alice": "inbound-STALE",
	}
	s.handleLine("2026/05/07 14:00:00 from 1.2.3.4:11111 accepted tcp:example.com:443 [inbound-10000 -> direct] email: alice\n")

	// 行里写的 inbound-10000 必须有
	if got := s.GetIPs("inbound-10000"); !reflect.DeepEqual(got, []string{"1.2.3.4"}) {
		t.Errorf("GetIPs(inbound-10000) = %v, want [1.2.3.4]", got)
	}
	// stale 映射不应该被写入(否则 modal 会显示一条幽灵记录)
	if got := s.GetIPs("inbound-STALE"); len(got) != 0 {
		t.Errorf("GetIPs(inbound-STALE) = %v, emailToTag fallback 不该在行里有 tag 时还触发", got)
	}
}

// API 入站(127.0.0.1 的 dokodemo,tag = "api")永远不计入 — 那是面板
// 自身和 xray stats 之间的循环,不是用户连接。
func TestHandleLine_IgnoresAPIInbound(t *testing.T) {
	s := newSvc()
	s.handleLine("2026/05/07 14:00:00 from 127.0.0.1:49548 accepted tcp:127.0.0.1:62789 [api -> api]\n")

	if all := s.GetAllIPs(); len(all) != 0 {
		t.Errorf("GetAllIPs() = %v, want empty (api 入站不计)", all)
	}
	if by := s.GetIPsByEmail(); len(by) != 0 {
		t.Errorf("GetIPsByEmail() = %v, want empty", by)
	}
}

// 非 accept 行(Warning / Info / 启动 banner / 噪音)必须直接丢。
func TestHandleLine_IgnoresNonAcceptLines(t *testing.T) {
	s := newSvc()
	for _, line := range []string{
		"2026/05/07 14:00:00 [Warning] some thing went wrong\n",
		"2026/05/07 14:00:00 [Info] xray started\n",
		"random garbage\n",
		"\n",
	} {
		s.handleLine(line)
	}
	if len(s.GetAllIPs()) != 0 || len(s.GetIPsByEmail()) != 0 {
		t.Errorf("non-accept lines should be ignored")
	}
}
