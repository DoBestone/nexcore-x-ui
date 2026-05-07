package service

// 解析层回归测试 + cache 行为锁。`ufw status` / `firewall-cmd --list-ports`
// 文本格式跨发行版基本固定,直接 hardcode 真实输出片段做 fixture,
// 不 fork 子进程,这样跑 CI / 没装 ufw 的容器也能 catch 解析回归。

import (
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestUFWAllowLineMatchesIPv4AndIPv6(t *testing.T) {
	cases := []struct {
		line string
		want string // captured port group, "" 表示 expected no match
	}{
		{"10001/tcp                  ALLOW       Anywhere", "10001"},
		{"10001/tcp (v6)             ALLOW       Anywhere (v6)", "10001"},
		{"443/tcp                    ALLOW IN    Anywhere", "443"},
		{"22/tcp                     ALLOW       Anywhere", "22"},
		// 不匹配:DENY / LIMIT 行
		{"22/tcp                     DENY        Anywhere", ""},
		// 不匹配:UDP-only 行(只放 TCP 进列表 — 翻墙协议入站基本都跑 TCP,
		// UDP 走 SS-2022 / hysteria 等场景后续再加)
		{"53/udp                     ALLOW       Anywhere", ""},
		// 不匹配:端口范围(目前不支持,留给后续)
		{"6881:6889/tcp              ALLOW       Anywhere", ""},
		// 不匹配:其它 noise
		{"Status: active", ""},
		{"To                         Action      From", ""},
	}
	for _, tc := range cases {
		m := ufwAllowLine.FindStringSubmatch(tc.line)
		got := ""
		if len(m) == 2 {
			got = m[1]
		}
		if got != tc.want {
			t.Errorf("ufwAllowLine(%q) → %q, want %q", tc.line, got, tc.want)
		}
	}
}

// cache 只看 expires:TTL 内 Status() 直接返 cached,不二次探测。这条
// 锁住前端高频轮询的成本上限 —— inbound 列表每打开都打一次,如果不
// cache 就是每次 fork ufw status 子进程。
func TestFirewallService_CacheReturnsFixtureWithoutReprobing(t *testing.T) {
	fs := &FirewallService{}
	fs.cached = &FirewallStatus{Active: true, Tool: "ufw", OpenPorts: []int{443, 10001}}
	fs.expires = time.Now().Add(firewallCacheTTL)

	got := fs.Status()
	if !got.Active || got.Tool != "ufw" {
		t.Fatalf("Status returned wrong fixture: %+v", got)
	}
	wantPorts := []int{443, 10001}
	sort.Ints(got.OpenPorts)
	if !reflect.DeepEqual(got.OpenPorts, wantPorts) {
		t.Errorf("OpenPorts = %v, want %v", got.OpenPorts, wantPorts)
	}
}

// 过期后必须重探。这里把 expires 故意设到过去,fs.cached 仍非 nil,
// Status() 应该忽略 cached 走 detectFirewall,然后写回新 cache。
// 测试机不一定装了 ufw,允许结果是 zero-value FirewallStatus。
func TestFirewallService_RefreshesAfterTTL(t *testing.T) {
	fs := &FirewallService{}
	fs.cached = &FirewallStatus{Active: true, Tool: "ufw", OpenPorts: []int{99}}
	fs.expires = time.Now().Add(-1 * time.Hour) // 已过期

	_ = fs.Status() // 不关心具体值,关心是否触发了 detect

	// detect 跑过一遍后 expires 必然推到未来,且 cached 被替换(不再是
	// 我们塞的那个 99 端口 fixture,因为没装 ufw 也得不到这个端口)。
	if !fs.expires.After(time.Now()) {
		t.Errorf("expires should be pushed into future after refresh, got %v", fs.expires)
	}
	if fs.cached != nil && len(fs.cached.OpenPorts) == 1 && fs.cached.OpenPorts[0] == 99 {
		t.Errorf("cache should have been replaced, still has fixture: %+v", fs.cached)
	}
}
