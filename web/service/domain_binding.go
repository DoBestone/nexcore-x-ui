package service

// 一键域名绑定 — 替操作员把"配 nginx 反代 + 取 Let's Encrypt 证书 +
// 起 xray WS 入站 + 把节点地址换成域名"这条链路自动化做掉。结果:
// 客户端连接走 example.com:443 → CF 边缘 → 你的 nginx → 内网 xray,
// origin IP 不暴露。
//
// 设计取舍:
//   - 不重新发明 ACME 客户端,shell 调 acme.sh / certbot 子进程拿证书。
//     这俩在 Linux 节点机上极常见,用户可控也好排查。
//   - nginx 配置只在专用文件 /etc/nginx/conf.d/nx-<domain>.conf 写,
//     不动用户已有的 server 块;同名文件检测到就拒绝(用户决策)。
//   - 失败回滚:任一步报错把已写的 nginx conf / xray inbound 撤回,
//     避免半成品。
//   - 所有 shell 命令带超时 + 输出,失败把命令输出回前端方便排查。
//   - 必须 root 才能跑(写 /etc/nginx + reload systemd)。检测到非 root
//     直接报错,不静默失败。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"nexcore-x-ui/database/model"
)

// EnvironmentReport — 一键绑定前的预检报告,read-only 多次安全。
//
// Issues 列表是 human-readable 的"现在还不能开始绑定"原因。空列表 =
// 立刻可以执行;非空 = 前端把每条挂在页面上,告诉用户该装什么/腾哪个端口。
type EnvironmentReport struct {
	PanelIsRoot     bool     `json:"panelIsRoot"`
	NginxInstalled  bool     `json:"nginxInstalled"`
	NginxVersion    string   `json:"nginxVersion"`
	NginxRunning    bool     `json:"nginxRunning"`
	NginxConfDir    string   `json:"nginxConfDir"`
	Port80Free      bool     `json:"port80Free"`
	Port443Free     bool     `json:"port443Free"`
	Port80Owner     string   `json:"port80Owner,omitempty"`
	Port443Owner    string   `json:"port443Owner,omitempty"`
	AcmeShInstalled bool     `json:"acmeShInstalled"`
	AcmeShPath      string   `json:"acmeShPath,omitempty"`
	CertbotInstalled bool    `json:"certbotInstalled"`
	PackageManager  string   `json:"packageManager"`
	Issues          []string `json:"issues"`
}

// BindRequest 绑定请求体。
type BindRequest struct {
	Domain     string `json:"domain"`
	Email      string `json:"email"`
	AcmeMode   string `json:"acmeMode"` // "http01" | "dns01-cf"
	CfApiToken string `json:"cfApiToken"`
	// 自动安装 nginx / acme.sh 开关。默认 false — 显式同意才跑 apt-get。
	InstallMissing bool `json:"installMissing"`
}

// BindResult 绑定结果。Success=true 时 ShareLink / InboundID 有值。
// 失败时 Error 是首句错误,Logs 是从开始到失败的全过程行(每条带 [step] 前缀)。
type BindResult struct {
	Success       bool     `json:"success"`
	Error         string   `json:"error,omitempty"`
	Logs          []string `json:"logs"`
	Domain        string   `json:"domain,omitempty"`
	NginxConfPath string   `json:"nginxConfPath,omitempty"`
	InboundID     int      `json:"inboundId,omitempty"`
	WSPath        string   `json:"wsPath,omitempty"`
	XrayPort      int      `json:"xrayPort,omitempty"`
}

// DomainBindingService 是无状态的;对外暴露的方法都接 ctx + 输入,返回报告。
type DomainBindingService struct {
	settingService SettingService
	inboundService InboundService
}

// DetectEnvironment 跑所有预检。10s 超时 — 子进程 spawn 慢但不会无限阻塞。
func (s *DomainBindingService) DetectEnvironment() *EnvironmentReport {
	r := &EnvironmentReport{}

	r.PanelIsRoot = os.Getuid() == 0
	if !r.PanelIsRoot {
		r.Issues = append(r.Issues,
			"面板不是以 root 运行,无法写 /etc/nginx 或调 systemctl。请重启面板进程为 root,或在生产 systemd 部署下使用。")
	}

	if path, err := exec.LookPath("nginx"); err == nil && path != "" {
		r.NginxInstalled = true
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out, _ := exec.CommandContext(ctx, path, "-v").CombinedOutput()
		cancel()
		r.NginxVersion = strings.TrimSpace(string(out))
		// systemctl is-active 输出 "active" / "inactive" / "failed"
		ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
		stOut, _ := exec.CommandContext(ctx, "systemctl", "is-active", "nginx").Output()
		cancel()
		r.NginxRunning = strings.TrimSpace(string(stOut)) == "active"
	} else {
		r.Issues = append(r.Issues, "nginx 未安装。Ubuntu/Debian: apt-get install nginx;CentOS/RHEL: yum install nginx")
	}
	if _, err := os.Stat("/etc/nginx/conf.d"); err == nil {
		r.NginxConfDir = "/etc/nginx/conf.d"
	} else if _, err := os.Stat("/etc/nginx/sites-available"); err == nil {
		// Debian/Ubuntu 风格也支持 conf.d 包含,通常 nginx.conf 默认 include 两个,
		// 我们统一写到 conf.d 下避免 sites-enabled 软链接复杂度。
		r.NginxConfDir = "/etc/nginx/conf.d"
	}

	if path, err := exec.LookPath("acme.sh"); err == nil && path != "" {
		r.AcmeShInstalled = true
		r.AcmeShPath = path
	} else {
		// 默认家路径
		for _, p := range []string{"/root/.acme.sh/acme.sh", os.ExpandEnv("$HOME/.acme.sh/acme.sh")} {
			if _, err := os.Stat(p); err == nil {
				r.AcmeShInstalled = true
				r.AcmeShPath = p
				break
			}
		}
	}
	if _, err := exec.LookPath("certbot"); err == nil {
		r.CertbotInstalled = true
	}
	if !r.AcmeShInstalled && !r.CertbotInstalled {
		r.Issues = append(r.Issues, "acme.sh 与 certbot 均未安装,无法取证书。安装 acme.sh: curl https://get.acme.sh | sh -s email=you@example.com")
	}

	r.Port80Free, r.Port80Owner = checkPortFree("80")
	r.Port443Free, r.Port443Owner = checkPortFree("443")
	if !r.NginxRunning && !r.Port80Free {
		r.Issues = append(r.Issues, fmt.Sprintf("80 端口被 %s 占用,且 nginx 没在跑。绑定流程会让 nginx 接管 80/443,先停止占用者", r.Port80Owner))
	}
	if !r.NginxRunning && !r.Port443Free {
		r.Issues = append(r.Issues, fmt.Sprintf("443 端口被 %s 占用,且 nginx 没在跑。同上", r.Port443Owner))
	}

	for _, m := range []string{"apt-get", "yum", "dnf", "pacman", "apk"} {
		if _, err := exec.LookPath(m); err == nil {
			r.PackageManager = m
			break
		}
	}
	return r
}

// checkPortFree 判断端口空闲。占用方通过 ss -tlnp 拿进程名(尽力而为,
// 失败也只是返回 "unknown")。
func checkPortFree(port string) (bool, string) {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 500*time.Millisecond)
	if err == nil {
		conn.Close()
		owner := lookupPortOwner(port)
		return false, owner
	}
	// dial 失败 = 没人监听 / 防火墙拦了。前者居多,当作空闲。
	return true, ""
}

func lookupPortOwner(port string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ss", "-tlnp", "sport", "= :"+port).CombinedOutput()
	if err != nil {
		return "unknown"
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, ":"+port+" ") {
			continue
		}
		// users:(("nginx",pid=1234,fd=6))
		if i := strings.Index(line, `users:(("`); i >= 0 {
			rest := line[i+len(`users:(("`):]
			if j := strings.Index(rest, `"`); j > 0 {
				return rest[:j]
			}
		}
	}
	return "unknown"
}

// BindDomain 跑完整绑定流程。每步成功才进下一步,失败立刻回滚。
//
// 流程:
//   1. 校验输入 + DetectEnvironment 没新增 blocking issue
//   2. 检查 conf.d/ 中是否已有同域名 server 块(任何 .conf 文件包含 server_name <domain>),
//      命中就报错退出(用户的明确决策)
//   3. (可选)apt-get install nginx / acme.sh
//   4. 取证书 (acme.sh + 选定 mode)
//   5. 创建 xray VLESS+WS 入站,listen 127.0.0.1:<random>,无 TLS
//   6. 写 /etc/nginx/conf.d/nx-<domain>.conf
//   7. nginx -t 通过 → systemctl reload nginx
//   8. 把 nodeAddress 设成 <domain>
//   9. 计算并返回分享链接
func (s *DomainBindingService) BindDomain(req *BindRequest) *BindResult {
	res := &BindResult{Logs: []string{}, Domain: req.Domain}
	logf := func(stage, msg string) {
		res.Logs = append(res.Logs, fmt.Sprintf("[%s] %s", stage, msg))
	}
	fail := func(stage string, err error) *BindResult {
		logf(stage, "失败: "+err.Error())
		res.Success = false
		res.Error = fmt.Sprintf("%s: %s", stage, err.Error())
		return res
	}

	// Step 1: 输入校验
	logf("validate", "开始")
	req.Domain = strings.TrimSpace(strings.ToLower(req.Domain))
	req.Email = strings.TrimSpace(req.Email)
	if !validDomain(req.Domain) {
		return fail("validate", errors.New("域名格式不合法"))
	}
	if req.Email == "" || !strings.Contains(req.Email, "@") {
		return fail("validate", errors.New("email 必填(Let's Encrypt 必填)"))
	}
	if req.AcmeMode != "http01" && req.AcmeMode != "dns01-cf" {
		return fail("validate", errors.New("acmeMode 必须是 http01 或 dns01-cf"))
	}
	if req.AcmeMode == "dns01-cf" && req.CfApiToken == "" {
		return fail("validate", errors.New("DNS-01 模式必须提供 CF API token"))
	}

	// Step 1.5: 必须 root + nginx 已装
	env := s.DetectEnvironment()
	if !env.PanelIsRoot {
		return fail("preflight", errors.New("面板不是以 root 运行"))
	}
	if !env.NginxInstalled {
		if !req.InstallMissing {
			return fail("preflight", errors.New("nginx 未安装,请勾选 installMissing 让面板自动装,或手动 apt-get install nginx 后重试"))
		}
		logf("install-nginx", "正在安装 nginx ...")
		if err := installPackage(env.PackageManager, "nginx"); err != nil {
			return fail("install-nginx", err)
		}
	}
	if !env.AcmeShInstalled {
		if !req.InstallMissing {
			return fail("preflight", errors.New("acme.sh 未安装,请勾选 installMissing 自动装"))
		}
		logf("install-acme", "正在安装 acme.sh ...")
		if err := installAcmeSh(req.Email); err != nil {
			return fail("install-acme", err)
		}
		// 重新探测
		env = s.DetectEnvironment()
	}

	// Step 2: 冲突检测
	confPath := filepath.Join(env.NginxConfDir, "nx-"+req.Domain+".conf")
	if _, err := os.Stat(confPath); err == nil {
		return fail("conflict-check",
			fmt.Errorf("已存在同名配置 %s — 之前可能绑过这个域名,请先「解绑」再重新绑定", confPath))
	}
	if conflict, where := scanNginxConflict(env.NginxConfDir, req.Domain); conflict {
		return fail("conflict-check",
			fmt.Errorf("nginx 配置 %s 中已有该域名的 server 块,请先解决", where))
	}

	// Step 3: 取证书
	logf("acme", "开始申请证书 ("+req.AcmeMode+") ...")
	certPath, keyPath, err := acquireCert(env.AcmeShPath, req.Domain, req.Email, req.AcmeMode, req.CfApiToken)
	if err != nil {
		return fail("acme", err)
	}
	logf("acme", "证书已签发: "+certPath)

	// Step 4: 创建 xray 入站
	wsPath := "/" + randomHex(16)
	xrayPort := pickRandomLoopbackPort()
	uuid := newUUID()
	clientEmail := "user-" + randomHex(3)
	logf("xray", fmt.Sprintf("创建 VLESS+WS 入站 127.0.0.1:%d path=%s", xrayPort, wsPath))
	inb := buildVlessWSInbound(xrayPort, wsPath, uuid, clientEmail, req.Domain)
	if err := s.inboundService.AddInbound(inb); err != nil {
		return fail("xray", fmt.Errorf("创建入站失败: %w", err))
	}
	res.InboundID = inb.Id
	res.WSPath = wsPath
	res.XrayPort = xrayPort

	// Step 5: 写 nginx 配置
	conf := buildNginxConf(req.Domain, certPath, keyPath, wsPath, xrayPort)
	logf("nginx", "写入配置 "+confPath)
	if err := os.WriteFile(confPath, []byte(conf), 0o644); err != nil {
		// 回滚 xray 入站
		_ = s.inboundService.DelInbound(inb.Id)
		return fail("nginx", err)
	}
	res.NginxConfPath = confPath

	// Step 6: nginx -t + reload
	logf("nginx", "校验配置 (nginx -t)")
	if out, err := runCmd(15*time.Second, "nginx", "-t"); err != nil {
		_ = os.Remove(confPath)
		_ = s.inboundService.DelInbound(inb.Id)
		return fail("nginx", fmt.Errorf("nginx -t 失败:\n%s", string(out)))
	}
	logf("nginx", "reload nginx")
	if out, err := runCmd(10*time.Second, "systemctl", "reload", "nginx"); err != nil {
		// reload 失败可能是 nginx 没在跑,试 start
		if out2, err2 := runCmd(15*time.Second, "systemctl", "start", "nginx"); err2 != nil {
			_ = os.Remove(confPath)
			_ = s.inboundService.DelInbound(inb.Id)
			return fail("nginx", fmt.Errorf("nginx reload + start 都失败:\nreload: %s\nstart: %s", string(out), string(out2)))
		}
	}

	// Step 7: 设置节点地址
	if err := s.settingService.saveSetting("nodeAddress", req.Domain); err != nil {
		// 不致命,只 warn
		logf("settings", "warning: 写 nodeAddress 失败 — "+err.Error())
	} else {
		logf("settings", "已设置节点地址 = "+req.Domain)
	}

	// Step 8: 完成
	res.Success = true
	logf("done", fmt.Sprintf("绑定完成 — 客户端 vless://uuid@%s:443?type=ws&security=tls&path=%s", req.Domain, wsPath))
	return res
}

// UnbindDomain 删除该域名的 nginx 配置 + reload。xray 入站不动(里面可能
// 有重要 client),由用户自己在入站列表里决定要不要删。
func (s *DomainBindingService) UnbindDomain(domain string) error {
	if !validDomain(domain) {
		return errors.New("域名格式不合法")
	}
	confPath := filepath.Join("/etc/nginx/conf.d", "nx-"+domain+".conf")
	if _, err := os.Stat(confPath); err != nil {
		return fmt.Errorf("找不到 %s — 没有面板创建的绑定配置", confPath)
	}
	if err := os.Remove(confPath); err != nil {
		return fmt.Errorf("删除 %s 失败: %w", confPath, err)
	}
	if out, err := runCmd(15*time.Second, "nginx", "-t"); err != nil {
		return fmt.Errorf("nginx -t 失败:\n%s", string(out))
	}
	if _, err := runCmd(10*time.Second, "systemctl", "reload", "nginx"); err != nil {
		return fmt.Errorf("nginx reload 失败: %w", err)
	}
	return nil
}

// ---- helpers ----

func validDomain(d string) bool {
	if len(d) < 3 || len(d) > 253 {
		return false
	}
	for _, r := range d {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-'
		if !ok {
			return false
		}
	}
	return strings.Contains(d, ".") && !strings.HasPrefix(d, ".") && !strings.HasSuffix(d, ".")
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// pickRandomLoopbackPort 在 30000-50000 范围找一个不被占用的本机端口给
// xray 内网入站用。撞了概率极低,撞了后端口约束自然报错。
func pickRandomLoopbackPort() int {
	for try := 0; try < 20; try++ {
		// 用 net.Listen 拿一个真空闲端口然后立刻关掉
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			continue
		}
		port := l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
		if port >= 1024 && port <= 65535 {
			return port
		}
	}
	return 30000 + int(time.Now().UnixNano()%20000)
}

func buildVlessWSInbound(port int, wsPath, uuid, email, sniHint string) *model.Inbound {
	return &model.Inbound{
		Listen:   "127.0.0.1",
		Port:     port,
		Protocol: model.VLESS,
		Tag:      fmt.Sprintf("inbound-%d", port),
		Enable:   true,
		Remark:   "domain-binding",
		Settings: fmt.Sprintf(
			`{"clients":[{"id":"%s","flow":"","email":"%s"}],"decryption":"none","fallbacks":[]}`,
			uuid, email),
		StreamSettings: fmt.Sprintf(
			`{"network":"ws","wsSettings":{"path":"%s","headers":{"Host":"%s"}}}`,
			wsPath, sniHint),
		Sniffing: `{"enabled":true,"destOverride":["http","tls"]}`,
	}
}

func buildNginxConf(domain, certPath, keyPath, wsPath string, xrayPort int) string {
	return fmt.Sprintf(`# Generated by nexcore-x-ui domain binding for %s
# DO NOT EDIT — 这条文件由面板托管,删除请用「解绑」按钮。

server {
    listen 80;
    listen [::]:80;
    server_name %s;
    # ACME http-01 challenge 走 acme.sh standalone 模式后这里全量重定向 https
    location / { return 301 https://$host$request_uri; }
}

server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name %s;

    ssl_certificate     %s;
    ssl_certificate_key %s;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_session_cache shared:SSL:10m;

    # xray VLESS+WebSocket 反代 — 路径必须跟入站 wsSettings.path 一致
    location %s {
        if ($http_upgrade != "websocket") { return 404; }
        proxy_pass http://127.0.0.1:%d;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_read_timeout 86400s;
        proxy_send_timeout 86400s;
    }

    # 默认其它路径不暴露任何东西,扫到根域名什么都没看到
    location / { return 404; }
}
`, domain, domain, domain, certPath, keyPath, wsPath, xrayPort)
}

// scanNginxConflict 扫描 conf.d 下其它 .conf 文件,找含有 server_name <domain>
// 的(粗匹配,不解析 nginx 语法)。命中就告诉用户在哪个文件冲突。
func scanNginxConflict(confDir, domain string) (bool, string) {
	entries, err := os.ReadDir(confDir)
	if err != nil {
		return false, ""
	}
	probe := []byte("server_name")
	want := []byte(domain)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		if e.Name() == "nx-"+domain+".conf" {
			continue
		}
		full := filepath.Join(confDir, e.Name())
		body, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		// 任一行包含 server_name 且 包含 domain → 冲突
		// 这是粗匹配,可能误报(比如域名出现在注释里),宁可让用户人工处理。
		for _, line := range strings.Split(string(body), "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "#") {
				continue
			}
			tb := []byte(t)
			if bytesContains(tb, probe) && bytesContains(tb, want) {
				return true, full
			}
		}
	}
	return false, ""
}

func bytesContains(haystack, needle []byte) bool {
	return strings.Contains(string(haystack), string(needle))
}

// runCmd:带超时的 shell 子进程。返回 stdout+stderr 合并、err。失败时
// 输出整段日志给调用方上报,方便用户在前端看明文错误。
func runCmd(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// installPackage 用户对应包管理器装单包。
func installPackage(pm, pkg string) error {
	if pm == "" {
		return errors.New("无可用包管理器")
	}
	var args []string
	switch pm {
	case "apt-get":
		args = []string{"-y", "install", pkg}
	case "yum", "dnf":
		args = []string{"install", "-y", pkg}
	case "pacman":
		args = []string{"-S", "--noconfirm", pkg}
	case "apk":
		args = []string{"add", pkg}
	default:
		return errors.New("不支持的包管理器: " + pm)
	}
	if pm == "apt-get" {
		if out, err := runCmd(60*time.Second, "apt-get", "update"); err != nil {
			return fmt.Errorf("apt-get update: %w (%s)", err, string(out))
		}
	}
	if out, err := runCmd(180*time.Second, pm, args...); err != nil {
		return fmt.Errorf("%s %s: %w (%s)", pm, strings.Join(args, " "), err, string(out))
	}
	return nil
}

// installAcmeSh 装 acme.sh 到 ~/.acme.sh — 官方安装方式。
// "curl https://get.acme.sh | sh -s email=..." 直接 pipe sh 太脏,
// 这里下载脚本到临时文件再执行,失败/中断都好排查。
func installAcmeSh(email string) error {
	// 检查 curl
	if _, err := exec.LookPath("curl"); err != nil {
		return errors.New("curl 未安装,无法下载 acme.sh")
	}
	tmp, err := os.CreateTemp("", "acme-install-*.sh")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	tmp.Close()
	if out, err := runCmd(60*time.Second, "curl", "-fsSL", "-o", tmp.Name(), "https://get.acme.sh"); err != nil {
		return fmt.Errorf("下载 acme.sh: %w (%s)", err, string(out))
	}
	if out, err := runCmd(120*time.Second, "sh", tmp.Name(), "--install", "-m", email); err != nil {
		return fmt.Errorf("安装 acme.sh: %w (%s)", err, string(out))
	}
	return nil
}

// acquireCert 调用 acme.sh 申请证书并返回 cert/key 文件路径。
//
// http01:acme.sh standalone 模式占用 80 端口跑一次性 HTTP server。
// 调用前要求 80 端口空闲(用户在 CF 临时改灰云)。
//
// dns01-cf:走 CF API 加 TXT 记录调战。CF token 通过环境变量传给 acme.sh
// 子进程,不写进文件 / log,避免泄漏。
//
// 用 Let's Encrypt 服务器(--server letsencrypt)。证书签发后用 --install-cert
// 把 cert/key 拷到固定路径(/etc/nginx/certs/<domain>/),后续续费 acme.sh
// 的 cron 自动续费 + 复制 + reload nginx。
func acquireCert(acmePath, domain, email, mode, cfToken string) (string, string, error) {
	if acmePath == "" {
		return "", "", errors.New("acme.sh 未安装")
	}
	certDir := "/etc/nginx/certs/" + domain
	if err := os.MkdirAll(certDir, 0o755); err != nil {
		return "", "", err
	}
	certPath := filepath.Join(certDir, "fullchain.pem")
	keyPath := filepath.Join(certDir, "privkey.pem")

	args := []string{"--issue", "-d", domain, "--server", "letsencrypt"}
	env := os.Environ()
	switch mode {
	case "http01":
		args = append(args, "--standalone")
	case "dns01-cf":
		args = append(args, "--dns", "dns_cf")
		env = append(env, "CF_Token="+cfToken)
	default:
		return "", "", errors.New("未知 acmeMode: " + mode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, acmePath, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("acme.sh issue: %w\n%s", err, string(out))
	}

	// 安装到固定路径
	installArgs := []string{
		"--install-cert", "-d", domain,
		"--key-file", keyPath,
		"--fullchain-file", certPath,
		"--reloadcmd", "systemctl reload nginx",
	}
	if out, err := runCmd(30*time.Second, acmePath, installArgs...); err != nil {
		return "", "", fmt.Errorf("acme.sh install-cert: %w\n%s", err, string(out))
	}
	return certPath, keyPath, nil
}
