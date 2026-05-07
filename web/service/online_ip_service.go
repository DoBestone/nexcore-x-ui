package service

import (
	"bufio"
	"io"
	"os"
	"regexp"
	"sync"
	"time"

	"nexcore-x-ui/logger"
	"nexcore-x-ui/xray"
)

// 在线 IP 统计：通过 tail Xray access log，按入站 tag 维护活跃源 IP 集合。
// 每个 IP 在 onlineIPTTL 时间窗口内无新连接即被移除。运行时数据，不持久化。

const (
	// onlineIPTTL — access.log 只在连接 accept 那一刻有日志行,长连接
	// (xtls-rprx-vision 看视频/挂代理)建立后没有新行,旧值 60s 让长连接
	// 用户在 modal 上闪一下就显示离线。5 分钟兼顾"真断线响应"和"长连接
	// 不闪烁",代价是用户真断线后 5 分钟内 modal 还显示在线。
	onlineIPTTL          = 5 * time.Minute
	onlineIPGCInterval   = 30 * time.Second
	onlineIPTailInterval = 500 * time.Millisecond
)

// 匹配 Xray access log 中的连接接受行。示例:
//   2024/12/15 10:23:45 from 1.2.3.4:12345 accepted tcp:example.com:443 [inbound-12345 -> direct] email: foo
//   2024/12/15 10:23:45 from [2001:db8::1]:12345 accepted udp:... [inbound-12345 -> direct]
var accessLogRegex = regexp.MustCompile(`from\s+(\[[0-9a-fA-F:]+\]|\d+\.\d+\.\d+\.\d+):\d+\s+accepted\s+\S+\s+\[([^\s\]]+)\s+->`)

// emailRegex — xray access.log 在 [tag -> outbound] 之后会跟 ` email: foo`,
// 客户级聚合靠它。无 email 的连接(socks/http/sniffing-only)跳过。
var emailRegex = regexp.MustCompile(`email:\s*(\S+)`)

type OnlineIPService struct {
	mu      sync.RWMutex
	state   map[string]map[string]time.Time // tag   -> ip -> lastSeen
	byEmail map[string]map[string]time.Time // email -> ip -> lastSeen (v1.1.3)
	started bool
	resetCh chan struct{}
}

var (
	onlineIPSvcOnce sync.Once
	onlineIPSvc     *OnlineIPService
)

func GetOnlineIPService() *OnlineIPService {
	onlineIPSvcOnce.Do(func() {
		onlineIPSvc = &OnlineIPService{
			state:   make(map[string]map[string]time.Time),
			byEmail: make(map[string]map[string]time.Time),
			resetCh: make(chan struct{}, 1),
		}
	})
	return onlineIPSvc
}

// Start 幂等启动后台 tail + GC + access.log 截断 goroutine。
func (s *OnlineIPService) Start() {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()

	go s.tailLoop()
	go s.gcLoop()
	go s.truncateLoop()
}

// truncateLoop — loglevel=info 后 access.log 增长很快,1H1G VPS 盘会被吃满。
// 每分钟检查一次,文件 ≥ accessLogMaxBytes 就 truncate 到 0。tailLoop 已经
// 会发现文件被截断后自动从头重读(见那边的 stat.Size() < cur 分支),不用
// 额外通信。truncate 而不是 rename+create:rename 后旧 fd 仍指向无名 inode,
// xray 会继续往那个 inode 写 → 我们读不到新内容了;truncate(0) 让 xray 的
// 写入位置在下次 write 时被内核 reset,新行从 offset=0 开始可读。
const accessLogMaxBytes int64 = 5 * 1024 * 1024 // 5MB

func (s *OnlineIPService) truncateLoop() {
	path := xray.GetAccessLogPath()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		stat, err := os.Stat(path)
		if err != nil {
			continue
		}
		if stat.Size() < accessLogMaxBytes {
			continue
		}
		if err := os.Truncate(path, 0); err != nil {
			logger.Warning("access.log truncate failed:", err)
		}
	}
}

// OnXrayRestart 重置内存状态，并通知 tailer 重新打开日志文件
// （Xray 重启会重新创建 access.log）。
func (s *OnlineIPService) OnXrayRestart() {
	s.mu.Lock()
	s.state = make(map[string]map[string]time.Time)
	s.byEmail = make(map[string]map[string]time.Time)
	s.mu.Unlock()
	select {
	case s.resetCh <- struct{}{}:
	default:
	}
}

func (s *OnlineIPService) record(tag, ip string) {
	now := time.Now()
	s.mu.Lock()
	ips, ok := s.state[tag]
	if !ok {
		ips = make(map[string]time.Time)
		s.state[tag] = ips
	}
	ips[ip] = now
	s.mu.Unlock()
}

// recordEmail — 与 record(tag, ip) 镜像,但按 email 维度索引,给客户级
// 在线 IP 显示用。同一连接同时调两条,GC 也对两个 map 同时跑。
func (s *OnlineIPService) recordEmail(email, ip string) {
	now := time.Now()
	s.mu.Lock()
	ips, ok := s.byEmail[email]
	if !ok {
		ips = make(map[string]time.Time)
		s.byEmail[email] = ips
	}
	ips[ip] = now
	s.mu.Unlock()
}

// TouchEmail — 流量心跳刷 TTL。access.log 只在 connection accept 那一刻有
// 行,长连接(VLESS xtls-rprx-vision 看视频/挂代理)建立后不再产生新行,
// 单纯靠 access-log-tail + TTL 过期就显示离线。修法:xray_traffic_job
// 每次拉到 user 级 stats 时,有 delta>0 的 email 调一下 TouchEmail,把该
// email 已知 IP 的 lastSeen 刷新到现在。这样只要流量在跑就一直显示在线,
// 流量停了 TTL 自然过期。如果该 email 在 byEmail map 里还没有 IP(尚未
// 收到 access.log 行),只能等下一次连接 accept 把 IP 补上,这条无解 ——
// 所以 loglevel 也必须 info,保证 accepted 行能被 tailer 抓到一次。
func (s *OnlineIPService) TouchEmail(email string) {
	if email == "" {
		return
	}
	now := time.Now()
	s.mu.Lock()
	if ips, ok := s.byEmail[email]; ok {
		for ip := range ips {
			ips[ip] = now
		}
	}
	s.mu.Unlock()
}

func (s *OnlineIPService) gcLoop() {
	ticker := time.NewTicker(onlineIPGCInterval)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-onlineIPTTL)
		s.mu.Lock()
		for tag, ips := range s.state {
			for ip, last := range ips {
				if last.Before(cutoff) {
					delete(ips, ip)
				}
			}
			if len(ips) == 0 {
				delete(s.state, tag)
			}
		}
		// 同样 GC byEmail map
		for email, ips := range s.byEmail {
			for ip, last := range ips {
				if last.Before(cutoff) {
					delete(ips, ip)
				}
			}
			if len(ips) == 0 {
				delete(s.byEmail, email)
			}
		}
		s.mu.Unlock()
	}
}

// GetCounts 返回每个入站 tag 当前的活跃 IP 数。
func (s *OnlineIPService) GetCounts() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cutoff := time.Now().Add(-onlineIPTTL)
	out := make(map[string]int, len(s.state))
	for tag, ips := range s.state {
		n := 0
		for _, last := range ips {
			if last.After(cutoff) {
				n++
			}
		}
		if n > 0 {
			out[tag] = n
		}
	}
	return out
}

// GetIPs 返回指定入站 tag 的当前活跃 IP 列表。
func (s *OnlineIPService) GetIPs(tag string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ips := s.state[tag]
	cutoff := time.Now().Add(-onlineIPTTL)
	out := make([]string, 0, len(ips))
	for ip, last := range ips {
		if last.After(cutoff) {
			out = append(out, ip)
		}
	}
	return out
}

// GetAllIPs 返回所有入站 tag → 活跃 IP 列表。
func (s *OnlineIPService) GetAllIPs() map[string][]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cutoff := time.Now().Add(-onlineIPTTL)
	out := make(map[string][]string, len(s.state))
	for tag, ips := range s.state {
		list := make([]string, 0, len(ips))
		for ip, last := range ips {
			if last.After(cutoff) {
				list = append(list, ip)
			}
		}
		if len(list) > 0 {
			out[tag] = list
		}
	}
	return out
}

func (s *OnlineIPService) tailLoop() {
	var (
		f      *os.File
		reader *bufio.Reader
	)
	path := xray.GetAccessLogPath()
	closeFile := func() {
		if f != nil {
			f.Close()
			f = nil
			reader = nil
		}
	}
	defer closeFile()

	for {
		if f == nil {
			fp, err := os.Open(path)
			if err != nil {
				// 日志文件可能尚未生成（xray 未启动），等待重试
				select {
				case <-s.resetCh:
				case <-time.After(2 * time.Second):
				}
				continue
			}
			// 跳到文件末尾，避免回放历史日志
			fp.Seek(0, io.SeekEnd)
			f = fp
			reader = bufio.NewReader(f)
		}

		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			s.handleLine(line)
		}
		if err == io.EOF {
			select {
			case <-s.resetCh:
				closeFile()
				continue
			case <-time.After(onlineIPTailInterval):
			}
			if f != nil {
				cur, _ := f.Seek(0, io.SeekCurrent)
				stat, statErr := os.Stat(path)
				if statErr == nil && stat.Size() < cur {
					// 日志被截断或重建，重新打开
					closeFile()
				}
			}
			continue
		}
		if err != nil {
			logger.Warning("online ip tailer read error:", err)
			closeFile()
			time.Sleep(2 * time.Second)
			continue
		}
	}
}

func (s *OnlineIPService) handleLine(line string) {
	m := accessLogRegex.FindStringSubmatch(line)
	if m == nil {
		return
	}
	ip := m[1]
	if len(ip) >= 2 && ip[0] == '[' {
		ip = ip[1 : len(ip)-1]
	}
	tag := m[2]
	if tag == "" || tag == "api" {
		return
	}
	s.record(tag, ip)
	// 顺手抽 email — 多用户客户级在线 IP 走这条。无 email(socks/http
	// 等单 password 协议、嗅探流量)只走 inbound 级 record。
	if em := emailRegex.FindStringSubmatch(line); len(em) > 1 {
		s.recordEmail(em[1], ip)
	}
}

// GetIPsByEmail — 返回每个 email 当前活跃的源 IP 列表。客户端流量 modal
// 用它给每行显示在线 IP 数 / 列表。
func (s *OnlineIPService) GetIPsByEmail() map[string][]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cutoff := time.Now().Add(-onlineIPTTL)
	out := make(map[string][]string, len(s.byEmail))
	for email, ips := range s.byEmail {
		var live []string
		for ip, last := range ips {
			if last.After(cutoff) {
				live = append(live, ip)
			}
		}
		if len(live) > 0 {
			out[email] = live
		}
	}
	return out
}
