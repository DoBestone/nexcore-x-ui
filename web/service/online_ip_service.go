package service

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"sync"
	"time"

	"nexcore-x-ui/database"
	"nexcore-x-ui/database/model"
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

// 匹配 Xray access log 中的连接接受行。xray 不同 inbound 类型写出的格式
// 不一致,典型两族:
//
//   A) vmess / vless / trojan 等"完整"格式(带 tcp:/udp: 前缀 + tag 段):
//      2024/12/15 10:23:45 from 1.2.3.4:12345 accepted tcp:example.com:443 [inbound-12345 -> direct] email: foo
//      2024/12/15 10:23:45 from [2001:db8::1]:12345 accepted udp:... [inbound-12345 -> direct]
//
//   B) shadowsocks-2022 multi-user "精简"格式(无 tcp:/udp: 前缀,无 [tag -> outbound]):
//      2026/05/07 14:12:07.584524 from 39.144.187.172:61117 accepted 194.221.250.50:80 email: user-645802
//
// 早先实现把 IP / tag 同捆在 accessLogRegex 一条正则里硬要求 [tag -> ...],
// SS-2022 行直接 match 失败,handleLine 提前 return,recordEmail 永远不被
// 调到 → 客户端流量 modal 显示"离线",但流量在跑。拆成"IP 必抓 / tag
// 可选 / email 单独抓"三层后两族都 cover。
var ipRegex = regexp.MustCompile(`from\s+(\[[0-9a-fA-F:]+\]|\d+\.\d+\.\d+\.\d+):\d+\s+accepted\b`)

// tagRegex — 仅 A 族 inbound(VLESS / VMess / Trojan / SS-legacy)写 tag 段。
// SS-2022 没有 → 走 nil branch,inbound 级在线数会少 SS 用户,但模态框
// 客户级路径(走 byEmail)仍然准。后续有需要可以加 email→inbound 的
// 反查表把 SS-2022 也补到 inbound 级 state map 里。
var tagRegex = regexp.MustCompile(`\[([^\s\]]+)\s+->\s+[^\s\]]+\]`)

// emailRegex — A 族在 [tag -> outbound] 之后跟 ` email: foo`,B 族(SS-2022)
// 在 dest:port 之后直接跟 ` email: user-xxx`。无 email 的连接(socks/http/
// 嗅探流量、API 入站 -> api outbound)跳过。
var emailRegex = regexp.MustCompile(`email:\s*(\S+)`)

type OnlineIPService struct {
	mu      sync.RWMutex
	state   map[string]map[string]time.Time // tag   -> ip -> lastSeen
	byEmail map[string]map[string]time.Time // email -> ip -> lastSeen (v1.1.3)
	// emailToTag — settings.clients[].email → inbound tag 的反查表。仅用
	// 来给 access.log 行里没写 [tag -> outbound] 的协议(SS-2022)补 inbound
	// 维度计数,让入站卡片"在线人数"角标能涵盖 SS-2022 客户端。OnXrayRestart
	// 后从 DB 重建一次,中间靠不变就一直对。新建 inbound / 改 inbound
	// 都会触发 xray 重启(needRestart cron),反查表跟着同步。
	emailToTag map[string]string
	started    bool
	resetCh    chan struct{}
}

var (
	onlineIPSvcOnce sync.Once
	onlineIPSvc     *OnlineIPService
)

func GetOnlineIPService() *OnlineIPService {
	onlineIPSvcOnce.Do(func() {
		onlineIPSvc = &OnlineIPService{
			state:      make(map[string]map[string]time.Time),
			byEmail:    make(map[string]map[string]time.Time),
			emailToTag: make(map[string]string),
			resetCh:    make(chan struct{}, 1),
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
	// 同步刷一遍 emailToTag —— xray 重启往往是因为新建 / 改了 inbound,
	// 反查表必须跟上。在锁外查 DB 避免长时间持锁;查回来再上锁覆盖。
	freshEmailToTag := loadEmailToTagFromDB()
	s.mu.Lock()
	s.state = make(map[string]map[string]time.Time)
	s.byEmail = make(map[string]map[string]time.Time)
	s.emailToTag = freshEmailToTag
	s.mu.Unlock()
	select {
	case s.resetCh <- struct{}{}:
	default:
	}
}

// loadEmailToTagFromDB 扫一遍 inbounds 表,提取 settings.clients[].email →
// inbound.Tag 映射。SS-2022 因为 access.log 没 tag 段,inbound 维度计数
// 必须靠这条反查兜底。读不到 DB(测试 / boot 早期)就返空 map,handleLine
// 那边 lookup 拿不到也只是退回原行为,不阻塞。
func loadEmailToTagFromDB() map[string]string {
	out := make(map[string]string)
	db := database.GetDB()
	if db == nil {
		return out
	}
	var inbounds []*model.Inbound
	if err := db.Model(&model.Inbound{}).Find(&inbounds).Error; err != nil {
		logger.Warning("loadEmailToTagFromDB: ", err)
		return out
	}
	for _, in := range inbounds {
		if in.Tag == "" || in.Settings == "" {
			continue
		}
		var parsed struct {
			Clients []map[string]any `json:"clients"`
		}
		if err := json.Unmarshal([]byte(in.Settings), &parsed); err != nil {
			continue
		}
		for _, c := range parsed.Clients {
			email, _ := c["email"].(string)
			if email == "" {
				continue
			}
			out[email] = in.Tag
		}
	}
	return out
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
	// 第一关:能不能找到 source IP。找不到说明这行根本不是 accept 行
	// (Warning / Info / 其它),直接丢。
	ipMatch := ipRegex.FindStringSubmatch(line)
	if ipMatch == nil {
		return
	}
	ip := ipMatch[1]
	if len(ip) >= 2 && ip[0] == '[' {
		ip = ip[1 : len(ip)-1]
	}

	// 第二关:有 [tag -> outbound] 就按 inbound tag 维度记一条;没有
	// (SS-2022)/或 tag 是 "api"(面板自身的 dokodemo 内部连接)跳过
	// inbound-tag 路径,但仍然走 email 路径。这一拆是 v2.0.10 修 SS-2022
	// "在线但显示离线"的核心:之前两条信息一捆,SS 行没 tag 直接整条丢。
	tagFromLine := ""
	if tagMatch := tagRegex.FindStringSubmatch(line); len(tagMatch) > 1 {
		tagFromLine = tagMatch[1]
		if tagFromLine != "" && tagFromLine != "api" {
			s.record(tagFromLine, ip)
		}
	}

	// 第三关:email 永远独立尝试。多用户协议(VLESS / VMess / Trojan /
	// SS-2022)都会写 email,客户端流量 modal "在线 IP" 列就靠它。无
	// email 的连接(socks/http/嗅探/API)跳过。
	if em := emailRegex.FindStringSubmatch(line); len(em) > 1 {
		email := em[1]
		s.recordEmail(email, ip)

		// v2.0.11 — SS-2022 这种 access.log 不带 [tag -> outbound] 的协议,
		// 上一关 tagFromLine 一直是空,入站卡角标计数永远 0。这里用
		// emailToTag 反查表补出 inbound tag,把这条连接也记到 state 维度,
		// 角标就能涵盖 SS-2022 客户端。tagFromLine 已经从行里抓到的话不
		// 重复记(行里写的 tag 是权威源)。
		if tagFromLine == "" {
			s.mu.RLock()
			mapped := s.emailToTag[email]
			s.mu.RUnlock()
			if mapped != "" && mapped != "api" {
				s.record(mapped, ip)
			}
		}
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

// EmailOnlineDetail — GetIPsByEmailDetailed 返回的 per-email 视图:除了
// 当前活跃 IP 列表,还带 inboundTag(走 emailToTag 反查)和 lastSeenAt
// (该 email 所有 IP 里最新的一拍 access.log 时间戳,unix 毫秒)。给
// 业务系统拼"客户在哪条入站、最后活动时间多久"用。
type EmailOnlineDetail struct {
	IPs        []string `json:"ips"`
	InboundTag string   `json:"inboundTag"`
	LastSeenAt int64    `json:"lastSeenAt"` // unix 毫秒
}

// GetIPsByEmailDetailed — 详尽版 GetIPsByEmail。inboundTag 走 emailToTag
// 反查表(SS-2022 行 access.log 不写 [tag -> outbound],靠这张表补);
// 反查不到留空串。lastSeenAt 是该 email 所有 IP 里最新一次 lastSeen,
// 没有活跃 IP 的 email 不进结果(跟 GetIPsByEmail 一致,免空 entry 噪声)。
func (s *OnlineIPService) GetIPsByEmailDetailed() map[string]EmailOnlineDetail {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cutoff := time.Now().Add(-onlineIPTTL)
	out := make(map[string]EmailOnlineDetail, len(s.byEmail))
	for email, ips := range s.byEmail {
		var live []string
		var newest time.Time
		for ip, last := range ips {
			if last.After(cutoff) {
				live = append(live, ip)
				if last.After(newest) {
					newest = last
				}
			}
		}
		if len(live) == 0 {
			continue
		}
		out[email] = EmailOnlineDetail{
			IPs:        live,
			InboundTag: s.emailToTag[email],
			LastSeenAt: newest.UnixMilli(),
		}
	}
	return out
}
