package service

// FirewallService — 系统层级防火墙(UFW / firewalld)的只读探测器。
//
// 设计目标:挡掉 v2.0.7 / v2.0.8 暴露出来的反复出现的客户问题 —— 用户
// 在面板里加完 inbound 拿了链接,客户端连不上以为是协议或链接 bug,实
// 际是 OS 层 UFW / firewalld 默认 INPUT DROP 没放行该端口。面板没法替
// 用户改防火墙(权责边界 + 不同发行版规则文件位置不一,出错代价高),
// 但起码可以把"端口被防火墙挡了"这条原因显式告诉用户,前端在 inbound
// 列表 / Dashboard 上挂个警告,操作员就能直接 `ufw allow <port>/tcp` 解
// 决,不用排查半小时。
//
// 边界:
//   - 只识别 UFW / firewalld 这两个 distro 默认主流方案;裸 nftables /
//     iptables / 云厂商 security group 探测不到。所以 Active=false 不等
//     于"没有防火墙挡",前端文案要写"未检测到 UFW / firewalld 启用"。
//   - 全部 read-only:绝不调 ufw allow / firewall-cmd --add-port,免得
//     反向给运维制造惊喜。

import (
	"bufio"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FirewallStatus 给前端的扁平数据。
//
// Active=true 仅当我们识别到一个启用中的防火墙;Tool 标识是哪个工具,
// 让前端文案能写得具体("UFW 当前阻挡 10000/tcp")。OpenPorts 是当前
// 系统层放行的入向 TCP 端口列表(不区分 IPv4 / IPv6 — 任一面放行就算
// 通,前端不用做交集)。
type FirewallStatus struct {
	Active    bool   `json:"active"`
	Tool      string `json:"tool"`
	OpenPorts []int  `json:"openPorts"`
}

// FirewallService 是 stateful 单例:status 探测要 fork 子进程,30s 缓存
// 摊掉后续 inbound 列表 / Dashboard 轮询的开销。线程安全(mu 守 cache)。
type FirewallService struct {
	mu      sync.Mutex
	cached  *FirewallStatus
	expires time.Time
}

const firewallCacheTTL = 30 * time.Second

// Status 返回最近 30s 内探测到的防火墙状态,缓存过期时同步重探。Frontend
// 期望这个调用 < 100ms,所以执行 ufw status / firewall-cmd 都用上了
// CombinedOutput + 自带 timeout(子进程一般 50ms 内返回)。
func (s *FirewallService) Status() FirewallStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil && time.Now().Before(s.expires) {
		return *s.cached
	}
	st := detectFirewall()
	s.cached = &st
	s.expires = time.Now().Add(firewallCacheTTL)
	return st
}

// detectFirewall 按优先级尝试 UFW → firewalld。哪个识别到启用直接返回。
// 命令不存在(LookPath 返 ""):跳过,继续下一个。命令存在但当前未启用:
// 返回带 Tool 但 Active=false 的状态(让前端能区分"装了 UFW 但关着" vs
// "彻底没装",虽然现在前端只关心 Active)。两个都未启用:返回零值。
func detectFirewall() FirewallStatus {
	if path, _ := exec.LookPath("ufw"); path != "" {
		if st, ok := readUFW(path); ok && st.Active {
			return st
		}
	}
	if path, _ := exec.LookPath("firewall-cmd"); path != "" {
		if st, ok := readFirewalld(path); ok && st.Active {
			return st
		}
	}
	return FirewallStatus{}
}

// ufwAllowLine 抓的是 `ufw status` 默认输出里 IPv4 / IPv6 ALLOW 入向规则
// 行。形如:
//
//	10001/tcp                  ALLOW       Anywhere
//	10001/tcp (v6)             ALLOW       Anywhere (v6)
//
// 端口范围(`6881:6889/tcp`)没纳入 — panel inbound 是单端口,误判损失
// 小;真要支持留给后续。
var ufwAllowLine = regexp.MustCompile(`^(\d+)/tcp(?:\s*\(v6\))?\s+ALLOW\b`)

func readUFW(path string) (FirewallStatus, bool) {
	out, err := exec.Command(path, "status").CombinedOutput()
	if err != nil {
		return FirewallStatus{}, false
	}
	text := string(out)
	// "Status: active" / "Status: inactive" 是 ufw 自己写死的两行之一;
	// 多语言环境也不翻译这一行,稳定字段。
	if !strings.Contains(text, "Status: active") {
		return FirewallStatus{Tool: "ufw"}, true
	}
	st := FirewallStatus{Active: true, Tool: "ufw"}
	seen := map[int]struct{}{}
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		m := ufwAllowLine.FindStringSubmatch(line)
		if len(m) != 2 {
			continue
		}
		p, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		st.OpenPorts = append(st.OpenPorts, p)
	}
	return st, true
}

func readFirewalld(path string) (FirewallStatus, bool) {
	// firewall-cmd --state 在停服时返回非 0 + "not running",不能光看
	// exit code，要看 stdout/stderr 文本。
	stateOut, _ := exec.Command(path, "--state").CombinedOutput()
	if !strings.Contains(string(stateOut), "running") || strings.Contains(string(stateOut), "not running") {
		return FirewallStatus{Tool: "firewalld"}, true
	}
	st := FirewallStatus{Active: true, Tool: "firewalld"}
	out, err := exec.Command(path, "--list-ports").Output()
	if err != nil {
		return st, true
	}
	seen := map[int]struct{}{}
	for _, tok := range strings.Fields(string(out)) {
		// tok 形如 "10001/tcp"。同时支持 "10001/tcp,udp" 这种 firewalld
		// 写法 — 拆出协议段做后缀匹配,只要包含 tcp 就算放行。
		parts := strings.SplitN(tok, "/", 2)
		if len(parts) != 2 {
			continue
		}
		if !strings.Contains(parts[1], "tcp") {
			continue
		}
		p, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		st.OpenPorts = append(st.OpenPorts, p)
	}
	return st, true
}
