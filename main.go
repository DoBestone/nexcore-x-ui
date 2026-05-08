package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "unsafe"

	"nexcore-x-ui/config"
	"nexcore-x-ui/database"
	"nexcore-x-ui/logger"
	"nexcore-x-ui/util/random"
	"nexcore-x-ui/v2ui"
	"nexcore-x-ui/web"
	"nexcore-x-ui/web/global"
	"nexcore-x-ui/web/service"
)

// buildTag is injected at link time by the release workflow:
//
//	go build -ldflags '-X main.buildTag=${{ github.ref_name }}' …
//
// Stays empty in dev / non-release builds. We log it on boot so the
// release manager can spot a mismatch between the git tag CI used and
// the config/version embedded in the binary — those drifting apart is
// the most common release bug, and a 1-line warning catches it before
// the dashboard lies to operators.
var buildTag string

func runWebServer() {
	log.Printf("%v %v", config.GetName(), config.GetVersion())
	if buildTag != "" {
		expected := "v" + config.GetVersion()
		if buildTag != expected {
			log.Printf("WARNING: build tag %q does not match embedded version %q — release mismatch?",
				buildTag, expected)
		}
	}

	switch config.GetLogLevel() {
	case config.Debug:
		logger.InitLogger(logger.LevelDebug)
	case config.Info:
		logger.InitLogger(logger.LevelInfo)
	case config.Warn:
		logger.InitLogger(logger.LevelWarning)
	case config.Error:
		logger.InitLogger(logger.LevelError)
	default:
		log.Fatal("unknown log level:", config.GetLogLevel())
	}

	err := database.InitDB(config.GetDBPath())
	if err != nil {
		log.Fatal(err)
	}
	// Wipe any leftover install-info.txt from versions ≤ v2.1.2 — that
	// file is no longer maintained, see database.CleanupLegacyInstallInfo
	// for the reasoning. Best-effort: failure here just means the stale
	// file lingers another boot.
	database.CleanupLegacyInstallInfo(config.GetDBPath())
	info, frsErr := database.RunFirstRunSetup(config.GetDBPath())
	// 关键不变性:只要 info.Generated == true,凭据就一定要打印 —— 这是
	// 操作员能看到明文密码的唯一窗口(bcrypt 不可逆)。systemd 把 stdout
	// 捕获到 journal,install.sh 装完会从 journal grep 出来回显一次给操作员。
	// 不再写盘了 —— v2.1.2 之前用 install-info.txt 落盘的 plaintext snapshot
	// 既要 24h auto-expire 又要操作员手动 rm,体感非常糟糕,且容器快照 / 备份
	// 工具会顺手把它收走,不必要的暴露面。journal 已经是凭据的真理之源,
	// 没必要再搞一份。
	if info != nil && info.Generated {
		// 首装一并发一把 admin-scope API token,操作员零步骤拿到一把可
		// 直接调 /api/v1/* 的 token,不必登面板再去 UI 里手发一个。
		// 跟密码一样,plaintext 只在这一行 banner 里出现一次,DB 里只
		// 存 SHA256 hash。错过 → 用 panel 内"Tokens"页签新发一把,或
		// `nexcore-x-ui reset`(连同 admin 一起重置)。
		//
		// 仅当 api_tokens 表是空的时候才发 — 这样 `nexcore-x-ui reset`
		// (清 admin + webPort 但保留 api_tokens 让旧集成不断)再次走到
		// 这条路径时,不会churn 出第二把意外的 token 。
		tokenLine := "(skipped — existing tokens preserved; manage via panel)"
		tokenSvc := service.APITokenService{}
		if existing, err := tokenSvc.ListTokens(); err != nil {
			tokenLine = "(read failed: " + err.Error() + ")"
			logger.Warning("first-run api token count failed:", err)
		} else if len(existing) == 0 {
			if created, terr := tokenSvc.CreateToken("first-install", service.ScopeAdmin, 0); terr == nil {
				tokenLine = created.Plaintext
			} else {
				tokenLine = "(generation failed: " + terr.Error() + " — issue one via panel)"
				logger.Warning("first-run api token mint failed:", terr)
			}
		}
		fmt.Println("=================================================")
		fmt.Println("  NexCore x-ui · first-run install info")
		fmt.Println("  ★ RECORD THIS NOW — only copy lives in journal")
		fmt.Println("=================================================")
		fmt.Printf("  panel port: %d\n", info.Port)
		fmt.Printf("  username:   %s\n", info.Username)
		fmt.Printf("  password:   %s\n", info.Password)
		fmt.Printf("  api token:  %s\n", tokenLine)
		fmt.Printf("  api scope:  admin (full /api/v1/* — POST/PUT/DELETE all)\n")
		fmt.Println("  → http://<server-ip>:" + fmt.Sprint(info.Port))
		fmt.Println("    curl -H \"Authorization: Bearer <api token>\" \\")
		fmt.Println("      http://<server-ip>:" + fmt.Sprint(info.Port) + "/api/v1/health")
		fmt.Println("=================================================")
		fmt.Println("  忘记可用: nexcore-x-ui reset  (强制重新生成)")
		fmt.Println("=================================================")
	}
	if frsErr != nil {
		// Banner already emitted above — log AFTER so the operator's
		// credentials are flushed to journal first regardless of any
		// downstream truncation.
		logger.Warning("first-run setup degraded:", frsErr)
	}

	var server *web.Server

	server = web.NewServer()
	global.SetWebServer(server)
	err = server.Start()
	if err != nil {
		log.Println(err)
		return
	}

	sigCh := make(chan os.Signal, 1)
	//信号量捕获处理
	signal.Notify(sigCh, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGKILL)
	for {
		sig := <-sigCh

		switch sig {
		case syscall.SIGHUP:
			// Graceful reload: build the new server first, swap only on
			// success. If Start() on the new instance fails (e.g. config
			// poisoned by an aborted upgrade) we keep the old server
			// running rather than exiting and leaving systemd to restart
			// the process with a 502-window.
			old := server
			next := web.NewServer()
			if err := old.Stop(); err != nil {
				logger.Warning("stop server err:", err)
			}
			if err := next.Start(); err != nil {
				logger.Warning("reload failed, keeping previous server alive:", err)
				// Best-effort: try to bring the previous instance back.
				revived := web.NewServer()
				if e := revived.Start(); e != nil {
					log.Println("revive previous server failed:", e)
					return
				}
				server = revived
				global.SetWebServer(server)
				continue
			}
			server = next
			global.SetWebServer(server)
		default:
			server.Stop()
			return
		}
	}
}

func resetSetting() {
	err := database.InitDB(config.GetDBPath())
	if err != nil {
		fmt.Println(err)
		return
	}

	settingService := service.SettingService{}
	err = settingService.ResetSettings()
	if err != nil {
		fmt.Println("reset setting failed:", err)
	} else {
		fmt.Println("reset setting success")
	}
}

// showSetting prints the current panel state read live from the database.
// Designed to never panic / crash even when the DB is partially populated:
// missing user row, missing port row, missing settings — every getter is
// nil-checked before dereference. The output also assembles the full panel
// URL so the operator can copy-paste straight into their browser; this is
// what the install.sh banner and `nexcore-x-ui creds` shell command both
// fall back to when install-info.txt is unavailable.
func showSetting(show bool) {
	if !show {
		return
	}
	// updateSetting is the only other CLI path that runs before showSetting
	// in `setting -show -port X` style invocations, and it already opened
	// the DB. Avoid a second open (which on SQLite single-conn pool would
	// leak the prior *sql.DB) by skipping when GetDB() is already non-nil.
	if database.GetDB() == nil {
		if err := database.InitDB(config.GetDBPath()); err != nil {
			fmt.Println("数据库无法打开:", err)
			fmt.Println("提示:确保 systemd 服务已启动一次,或运行 `nexcore-x-ui start`。")
			return
		}
	}

	settingService := service.SettingService{}

	port, err := settingService.GetPort()
	if err != nil {
		fmt.Println("读取面板端口失败:", err)
	}
	listen, _ := settingService.GetListen()
	basePath, _ := settingService.GetBasePath()
	secureEnabled := settingService.GetSecureEntryEnabled()
	securePath := strings.TrimSpace(settingService.GetSecureEntryPath())
	certFile, _ := settingService.GetCertFile()
	keyFile, _ := settingService.GetKeyFile()
	tlsEnabled := certFile != "" && keyFile != ""

	userService := service.UserService{}
	userModel, userErr := userService.GetFirstUser()
	username := "(未创建)"
	if userErr == nil && userModel != nil && userModel.Username != "" {
		username = userModel.Username
	}

	scheme := "http"
	if tlsEnabled {
		scheme = "https"
	}
	// URL host 选择策略:
	//   1. listen 显式设置 → 操作员选的就是它,不动。
	//   2. listen 空(bind any) → firstNonLoopbackIP。云主机这步常拿到
	//      VPC 内网 IP(阿里云 172.18/X、AWS 10.X、Tencent 10.X) — 直接拼成
	//      URL 给操作员是没用的,粘到浏览器打不开。所以再走 detectPublicIPv4
	//      探一次公网 IP,拿到就替换。失败再退回内网 IP。
	// 不打"请自行替换"提示 — 正常流程不该让操作员手工改输出。
	hostBare := listen
	if hostBare == "" {
		hostBare = firstNonLoopbackIP()
		if isPrivateIPv4(hostBare) {
			if pub := detectPublicIPv4(1500 * time.Millisecond); pub != "" {
				hostBare = pub
			}
		}
	}
	if hostBare == "" {
		hostBare = "<server-ip>"
	}
	host := hostBare
	if !strings.Contains(host, ":") && port > 0 {
		host = fmt.Sprintf("%s:%d", host, port)
	}
	pathSuffix := basePath
	if !strings.HasSuffix(pathSuffix, "/") {
		pathSuffix += "/"
	}
	if secureEnabled && securePath != "" {
		pathSuffix = pathSuffix + securePath + "/"
	}

	fmt.Println("─── NexCore x-ui · 当前实时设置(read from DB) ───")
	fmt.Printf("  port:           %d\n", port)
	if listen == "" {
		fmt.Println("  listen:         (any) — 默认绑定全部网卡")
	} else {
		fmt.Printf("  listen:         %s\n", listen)
	}
	fmt.Printf("  base path:      %s\n", basePath)
	if secureEnabled && securePath != "" {
		fmt.Printf("  secure entry:   %s  (面板只在 base+entry 下应答,扫端口看到 404)\n", securePath)
	} else {
		fmt.Println("  secure entry:   (未启用)")
	}
	if tlsEnabled {
		fmt.Printf("  TLS:            yes  (cert=%s)\n", certFile)
	} else {
		fmt.Println("  TLS:            no   (面板裸 HTTP,生产建议开启)")
	}
	fmt.Printf("  username:       %s\n", username)
	fmt.Println("  password:       (bcrypt 哈希存储;忘记请用 `nexcore-x-ui reset`)")
	if userErr != nil {
		fmt.Println("  ! 未读到 admin 用户:" + userErr.Error())
		fmt.Println("    若是首装,等服务完全起来再试;否则 `nexcore-x-ui reset` 重新生成")
	}
	fmt.Println()
	fmt.Printf("  → %s://%s%s\n", scheme, host, pathSuffix)
	fmt.Println()
}

func updateTgbotEnableSts(status bool) {
	settingService := service.SettingService{}
	currentTgSts, err := settingService.GetTgbotenabled()
	if err != nil {
		fmt.Println(err)
		return
	}
	logger.Infof("current enabletgbot status[%v],need update to status[%v]", currentTgSts, status)
	if currentTgSts != status {
		err := settingService.SetTgbotenabled(status)
		if err != nil {
			fmt.Println(err)
			return
		} else {
			logger.Infof("SetTgbotenabled[%v] success", status)
		}
	}
	return
}

func updateTgbotSetting(tgBotToken string, tgBotChatid int, tgBotRuntime string) {
	err := database.InitDB(config.GetDBPath())
	if err != nil {
		fmt.Println(err)
		return
	}

	settingService := service.SettingService{}

	if tgBotToken != "" {
		err := settingService.SetTgBotToken(tgBotToken)
		if err != nil {
			fmt.Println(err)
			return
		} else {
			logger.Info("updateTgbotSetting tgBotToken success")
		}
	}

	if tgBotRuntime != "" {
		err := settingService.SetTgbotRuntime(tgBotRuntime)
		if err != nil {
			fmt.Println(err)
			return
		} else {
			logger.Infof("updateTgbotSetting tgBotRuntime[%s] success", tgBotRuntime)
		}
	}

	if tgBotChatid != 0 {
		err := settingService.SetTgBotChatId(tgBotChatid)
		if err != nil {
			fmt.Println(err)
			return
		} else {
			logger.Info("updateTgbotSetting tgBotChatid success")
		}
	}
}

func updateSetting(port int, username string, password string) {
	err := database.InitDB(config.GetDBPath())
	if err != nil {
		fmt.Println(err)
		return
	}

	settingService := service.SettingService{}

	if port > 0 {
		err := settingService.SetPort(port)
		if err != nil {
			fmt.Println("set port failed:", err)
		} else {
			fmt.Printf("set port %v success", port)
		}
	}
	if username != "" || password != "" {
		userService := service.UserService{}
		err := userService.UpdateFirstUser(username, password)
		if err != nil {
			fmt.Println("set username and password failed:", err)
		} else {
			fmt.Println("set username and password success")
		}
	}
}

// applySecureEntry 是 `setting -secureEntry on|off [-secureEntryPath xxx]`
// 的处理器。语义:
//   - secureEntry == "on":开启;path 为空时自动生成 24 位 alnum slug。
//   - secureEntry == "off":关闭;不动 path(留着方便 re-enable 时复用)。
//   - secureEntry == "":只改 path(若 -secureEntryPath 给了)。
//
// 改完不重启进程 — 安全入口在 initRouter 启动期把 path 拼进 basePath,
// 运行时不动态切换。CLI 调用方(install.sh / nexcore-x-ui.sh)负责
// 后续 systemctl restart 让新 basePath 生效。
//
// 错误用 stderr + 非零 exit code,这样调用方脚本能 set -e 兜住。
func applySecureEntry(toggle, path string) {
	if database.GetDB() == nil {
		if err := database.InitDB(config.GetDBPath()); err != nil {
			fmt.Fprintln(os.Stderr, "open db failed:", err)
			os.Exit(1)
		}
	}
	settingService := service.SettingService{}

	toggle = strings.ToLower(strings.TrimSpace(toggle))
	switch toggle {
	case "on":
		// path 来源:命令行优先,空则看 DB 现存(re-enable 复用旧 slug),
		// DB 也空才新生成。re-enable 复用旧 slug 是有意为之 — 操作员
		// "临时关一下"再开,不该把已经发出去的 secret URL 全废掉。
		newPath := strings.TrimSpace(path)
		if newPath == "" {
			newPath = strings.TrimSpace(settingService.GetSecureEntryPath())
		}
		if newPath == "" {
			newPath = random.Seq(24)
		}
		if err := settingService.SetSecureEntryPath(newPath); err != nil {
			fmt.Fprintln(os.Stderr, "set secureEntryPath failed:", err)
			os.Exit(1)
		}
		if err := settingService.SetSecureEntryEnabled(true); err != nil {
			fmt.Fprintln(os.Stderr, "set secureEntryEnabled failed:", err)
			os.Exit(1)
		}
		fmt.Printf("secure entry: ENABLED, path = %s\n", newPath)
		fmt.Println("→ 重启服务后生效:systemctl restart nexcore-x-ui")
	case "off":
		if err := settingService.SetSecureEntryEnabled(false); err != nil {
			fmt.Fprintln(os.Stderr, "set secureEntryEnabled failed:", err)
			os.Exit(1)
		}
		fmt.Println("secure entry: DISABLED (path 保留,日后 -secureEntry on 可复用)")
		fmt.Println("→ 重启服务后生效:systemctl restart nexcore-x-ui")
	case "":
		// 只改 path,不切开关。enabled 状态不动。
		if path == "" {
			fmt.Fprintln(os.Stderr, "-secureEntryPath empty and -secureEntry not given — nothing to do")
			os.Exit(2)
		}
		if err := settingService.SetSecureEntryPath(strings.TrimSpace(path)); err != nil {
			fmt.Fprintln(os.Stderr, "set secureEntryPath failed:", err)
			os.Exit(1)
		}
		fmt.Printf("secure entry path updated: %s\n", strings.TrimSpace(path))
		fmt.Println("→ 重启服务后生效:systemctl restart nexcore-x-ui")
	default:
		fmt.Fprintln(os.Stderr, "-secureEntry must be one of: on, off")
		os.Exit(2)
	}
}

func main() {
	if len(os.Args) < 2 {
		runWebServer()
		return
	}

	var showVersion bool
	flag.BoolVar(&showVersion, "v", false, "show version")

	runCmd := flag.NewFlagSet("run", flag.ExitOnError)

	v2uiCmd := flag.NewFlagSet("v2-ui", flag.ExitOnError)
	var dbPath string
	v2uiCmd.StringVar(&dbPath, "db", "/etc/v2-ui/v2-ui.db", "set v2-ui db file path")

	magicCmd := flag.NewFlagSet("magic", flag.ExitOnError)
	var magicTTL int
	var magicNote string
	var magicHost string
	magicCmd.IntVar(&magicTTL, "ttl", 600, "magic link lifetime in seconds (60..86400)")
	magicCmd.StringVar(&magicNote, "note", "", "audit note (free-form)")
	magicCmd.StringVar(&magicHost, "host", "", "panel host[:port] for the URL (auto-detect if empty)")

	settingCmd := flag.NewFlagSet("setting", flag.ExitOnError)
	var port int
	var username string
	var password string
	var fromEnv bool
	var tgbottoken string
	var tgbotchatid int
	var enabletgbot bool
	var tgbotRuntime string
	var reset bool
	var show bool
	var secureEntry string
	var secureEntryPath string
	settingCmd.BoolVar(&reset, "reset", false, "reset all settings")
	settingCmd.BoolVar(&show, "show", false, "show current settings")
	settingCmd.IntVar(&port, "port", 0, "set panel port")
	settingCmd.StringVar(&username, "username", "", "set login username")
	settingCmd.StringVar(&password, "password", "", "set login password (avoid: visible in ps; use -from-env)")
	settingCmd.BoolVar(&fromEnv, "from-env", false, "read username/password from NEXCORE_USERNAME/NEXCORE_PASSWORD env vars")
	settingCmd.StringVar(&tgbottoken, "tgbottoken", "", "set telegrame bot token")
	settingCmd.StringVar(&tgbotRuntime, "tgbotRuntime", "", "set telegrame bot cron time")
	settingCmd.IntVar(&tgbotchatid, "tgbotchatid", 0, "set telegrame bot chat id")
	settingCmd.BoolVar(&enabletgbot, "enabletgbot", false, "enable telegram bot notify")
	settingCmd.StringVar(&secureEntry, "secureEntry", "", "enable/disable 安全入口 (on|off; empty = no change)")
	settingCmd.StringVar(&secureEntryPath, "secureEntryPath", "", "secure entry slug (empty when enabling = auto-generate 24-char)")

	oldUsage := flag.Usage
	flag.Usage = func() {
		oldUsage()
		fmt.Println()
		fmt.Println("Commands:")
		fmt.Println("    run            run web panel")
		fmt.Println("    v2-ui          migrate form v2-ui")
		fmt.Println("    setting        set settings")
	}

	flag.Parse()
	if showVersion {
		fmt.Println(config.GetVersion())
		return
	}

	switch os.Args[1] {
	case "run":
		err := runCmd.Parse(os.Args[2:])
		if err != nil {
			fmt.Println(err)
			return
		}
		runWebServer()
	case "v2-ui":
		err := v2uiCmd.Parse(os.Args[2:])
		if err != nil {
			fmt.Println(err)
			return
		}
		err = v2ui.MigrateFromV2UI(dbPath)
		if err != nil {
			fmt.Println("migrate from v2-ui failed:", err)
		}
	case "setting":
		err := settingCmd.Parse(os.Args[2:])
		if err != nil {
			fmt.Println(err)
			return
		}
		if reset {
			resetSetting()
		} else {
			if fromEnv {
				if envUser := os.Getenv("NEXCORE_USERNAME"); envUser != "" {
					username = envUser
				}
				if envPwd := os.Getenv("NEXCORE_PASSWORD"); envPwd != "" {
					password = envPwd
				}
				_ = os.Unsetenv("NEXCORE_PASSWORD")
			}
			updateSetting(port, username, password)
		}
		if secureEntry != "" || secureEntryPath != "" {
			applySecureEntry(secureEntry, secureEntryPath)
		}
		if show {
			showSetting(show)
		}
		if (tgbottoken != "") || (tgbotchatid != 0) || (tgbotRuntime != "") {
			updateTgbotSetting(tgbottoken, tgbotchatid, tgbotRuntime)
		}
	case "magic":
		if err := magicCmd.Parse(os.Args[2:]); err != nil {
			fmt.Println(err)
			return
		}
		mintMagicLink(magicTTL, magicNote, magicHost)
	default:
		fmt.Println("expected one of: run, v2-ui, setting, magic, -v")
		fmt.Println()
		runCmd.Usage()
		fmt.Println()
		v2uiCmd.Usage()
		fmt.Println()
		settingCmd.Usage()
		fmt.Println()
		magicCmd.Usage()
	}
}

// mintMagicLink is a CLI helper that initialises the database and inserts a
// one-shot login token, then prints a ready-to-paste URL. Operators use this
// to remotely log themselves into the panel without typing credentials.
func mintMagicLink(ttlSeconds int, note, host string) {
	if err := database.InitDB(config.GetDBPath()); err != nil {
		fmt.Println("open db failed:", err)
		os.Exit(1)
	}
	svc := service.MagicTokenService{}
	t, err := svc.CreateMagicToken(time.Duration(ttlSeconds)*time.Second, note)
	if err != nil {
		fmt.Println("mint magic token failed:", err)
		os.Exit(1)
	}
	if host == "" {
		host = autoDetectHost()
	}
	scheme := "http"
	// Token in URL fragment, NOT path. The fragment never reaches
	// server logs / proxy logs / Referer headers; the SPA reads it
	// client-side and POSTs it to /panel-login/consume.
	url := fmt.Sprintf("%s://%s/magic-login#tk=%s", scheme, host, t.Plaintext)
	fmt.Println(url)
}

func autoDetectHost() string {
	settingService := service.SettingService{}
	port, err := settingService.GetPort()
	if err != nil || port == 0 {
		port = 54321
	}
	listen, _ := settingService.GetListen()
	if listen == "" {
		// best-effort: pick first non-loopback IPv4. On cloud VMs this is
		// usually a VPC private IP (172.18/X, 10.X) — useless in a URL.
		// detectPublicIPv4 races a few echo / metadata endpoints to find
		// the routable address; fall back to the private IP only if
		// everything fails so we never break offline boxes.
		listen = firstNonLoopbackIP()
		if isPrivateIPv4(listen) {
			if pub := detectPublicIPv4(1500 * time.Millisecond); pub != "" {
				listen = pub
			}
		}
		if listen == "" {
			listen = "127.0.0.1"
		}
	}
	return fmt.Sprintf("%s:%d", listen, port)
}

func firstNonLoopbackIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return ""
}

// isPrivateIPv4 reports whether the given dotted-quad sits in a non-routable
// or NAT'd range — i.e. pasting it into a browser from outside the host's
// network won't reach it. Catches RFC1918, CGNAT (100.64/10), link-local
// (169.254/16), and loopback. Used to decide whether to bother probing for
// a public IP before printing a URL.
func isPrivateIPv4(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil {
		return false
	}
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	switch {
	case v4[0] == 10:
		return true
	case v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31:
		return true
	case v4[0] == 192 && v4[1] == 168:
		return true
	case v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127:
		return true
	case v4[0] == 169 && v4[1] == 254:
		return true
	case v4[0] == 127:
		return true
	}
	return false
}

// detectPublicIPv4 races a handful of public-IP echo + cloud metadata
// endpoints in parallel and returns the first valid IPv4 returned. Total
// wall budget = timeout (slow endpoints don't extend it). Returns "" if
// every endpoint fails (offline / firewalled / GFW).
//
// Endpoint mix is deliberately diverse:
//   - Cloud metadata (Aliyun 100.100.100.200, AWS+Tencent 169.254.169.254):
//     fast and authoritative when running on that cloud, instant TCP-RST
//     elsewhere.  Aliyun's eipv4 path returns the EIP plain text with no
//     auth header — perfect for this case.
//   - HTTPS echos (api.ipify.org, checkip.amazonaws.com): work from any
//     internet-connected host, may be slow/blocked in some regions.
//
// The HTTP client gets a per-request timeout equal to the full budget so
// that the slowest survivor doesn't outlive the budget. Request bodies are
// LimitReader-capped so a misbehaving endpoint can't pour megabytes at us.
func detectPublicIPv4(timeout time.Duration) string {
	endpoints := []string{
		// Aliyun ECS: eipv4 returns the Elastic IP if one is attached;
		// public-ipv4 covers the older "经典公网 IP" / pay-by-traffic style.
		// Both fail fast (ECONNREFUSED / 404) elsewhere.
		"http://100.100.100.200/latest/meta-data/eipv4",
		"http://100.100.100.200/latest/meta-data/public-ipv4",
		// Tencent Cloud CVM
		"http://metadata.tencentyun.com/latest/meta-data/public-ipv4",
		// AWS / Azure / GCP all use 169.254.169.254. AWS IMDSv2 needs a
		// token — IMDSv1 still works on most instances; if disabled this
		// just 401s and the HTTPS echos pick up the slack.
		"http://169.254.169.254/latest/meta-data/public-ipv4",
		// Generic HTTPS echos — last-resort, work from any reachable host.
		"https://api.ipify.org",
		"https://checkip.amazonaws.com",
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	client := &http.Client{Timeout: timeout}
	out := make(chan string, len(endpoints))

	for _, ep := range endpoints {
		go func(url string) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				out <- ""
				return
			}
			resp, err := client.Do(req)
			if err != nil {
				out <- ""
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				out <- ""
				return
			}
			body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
			if err != nil {
				out <- ""
				return
			}
			ip := strings.TrimSpace(string(body))
			if parsed := net.ParseIP(ip); parsed != nil && parsed.To4() != nil && !isPrivateIPv4(ip) {
				out <- ip
				return
			}
			out <- ""
		}(ep)
	}

	for i := 0; i < len(endpoints); i++ {
		select {
		case ip := <-out:
			if ip != "" {
				return ip
			}
		case <-ctx.Done():
			return ""
		}
	}
	return ""
}
