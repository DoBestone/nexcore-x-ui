package service

// 回归测试:OnlineIPService.handleLine 必须同时识别两族 xray access.log
// 行格式 ——
//   A) vmess / vless / trojan / SS-legacy 等带 [tag -> outbound] 的"完整"行
//   B) shadowsocks-2022 multi-user 不带 tag 段的"精简"行
//
// v2.0.10 之前的实现拿一条强制 [tag -> ...] 的正则做总闸,B 族整行不
// 匹配 → handleLine 提前 return → byEmail 永远空 → SS-2022 客户端流量
// modal 显示离线,但流量在涨。

import (
	"reflect"
	"sort"
	"testing"
	"time"
)

// newSvc 复用上线服务结构,但绕开 sync.Once 单例 —— 测试要相互独立、
// 不污染全局。Start() 不调,handleLine 是纯函数操作内部 map。
func newSvc() *OnlineIPService {
	return &OnlineIPService{
		state:   make(map[string]map[string]time.Time),
		byEmail: make(map[string]map[string]time.Time),
		resetCh: make(chan struct{}, 1),
	}
}

func TestHandleLine_VLESSWithTagAndEmail(t *testing.T) {
	s := newSvc()
	// A 族完整格式 —— inbound-tag 维度 + email 维度都要记到。
	s.handleLine("2026/05/07 14:00:00 from 1.2.3.4:12345 accepted tcp:example.com:443 [inbound-10000 -> direct] email: alice\n")

	if got := s.GetIPs("inbound-10000"); !reflect.DeepEqual(got, []string{"1.2.3.4"}) {
		t.Errorf("GetIPs(inbound-10000) = %v, want [1.2.3.4]", got)
	}
	got := s.GetIPsByEmail()["alice"]
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"1.2.3.4"}) {
		t.Errorf("GetIPsByEmail()[alice] = %v, want [1.2.3.4]", got)
	}
}

// 关键回归 —— SS-2022 那条精简行必须把 email 维度记下,即使 inbound
// 维度因为没有 tag 段没法记。客户端流量 modal 走 GetIPsByEmail,这条
// 一通,modal 就不会再"全员离线"。
func TestHandleLine_SS2022WithEmailButNoTag(t *testing.T) {
	s := newSvc()
	s.handleLine("2026/05/07 14:12:07.584524 from 39.144.187.172:61117 accepted 194.221.250.50:80 email: user-645802\n")

	got := s.GetIPsByEmail()["user-645802"]
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"39.144.187.172"}) {
		t.Fatalf("GetIPsByEmail()[user-645802] = %v, want [39.144.187.172] (SS-2022 行必须能进 byEmail)", got)
	}
	// 没有 tag 段 → state map 应该是空的(不要拿 dest IP 当 tag 误录)。
	if all := s.GetAllIPs(); len(all) != 0 {
		t.Errorf("GetAllIPs() = %v, want empty (SS-2022 行没 tag)", all)
	}
}

// 多 client 同 inbound,modal 要分别看到各自 IP,不能串到同一个 email
// 上去。这条同时验 SS-2022 路径下多用户场景没串 key。
func TestHandleLine_SS2022_MultipleClientsKeepsEmailsSeparate(t *testing.T) {
	s := newSvc()
	s.handleLine("2026/05/07 14:00:00 from 1.1.1.1:11111 accepted 8.8.8.8:443 email: alice\n")
	s.handleLine("2026/05/07 14:00:01 from 2.2.2.2:22222 accepted 8.8.8.8:443 email: bob\n")
	s.handleLine("2026/05/07 14:00:02 from 1.1.1.1:33333 accepted 8.8.8.8:443 email: alice\n")

	by := s.GetIPsByEmail()
	for k, v := range by {
		sort.Strings(v)
		by[k] = v
	}
	want := map[string][]string{
		"alice": {"1.1.1.1"},
		"bob":   {"2.2.2.2"},
	}
	if !reflect.DeepEqual(by, want) {
		t.Errorf("byEmail = %v, want %v", by, want)
	}
}

// API 入站(dokodemo-door,tag = "api")永远不该计入在线 IP —— 那是面板
// 自己跟 xray stats 通信的内部连接,不是真用户。这条防 v2.0.9 之后误把
// `api` tag 也走 record 路径。
func TestHandleLine_IgnoresAPIInbound(t *testing.T) {
	s := newSvc()
	s.handleLine("2026/05/07 14:00:00 from 127.0.0.1:49548 accepted tcp:127.0.0.1:62789 [api -> api]\n")

	if all := s.GetAllIPs(); len(all) != 0 {
		t.Errorf("GetAllIPs() = %v, want empty (api 入站不计)", all)
	}
	if by := s.GetIPsByEmail(); len(by) != 0 {
		t.Errorf("GetIPsByEmail() = %v, want empty (api 入站没 email,也不该出现)", by)
	}
}

// 非 accept 行(Warning / Info / 启动 banner)必须直接丢,不挑出 IP。
// 防止 logger 起步那一行的副作用污染。
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
