package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "unsafe"

	"nexcore-x-ui/config"
	"nexcore-x-ui/database"
	"nexcore-x-ui/logger"
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
	// Sweep an aged install-info.txt before deciding whether first-run
	// setup needs to print fresh credentials. If the file has lived past
	// installInfoMaxAge from the previous install it is removed here so
	// it can't keep showing stale plaintext to anyone with shell access.
	database.MaybeExpireInstallInfo(config.GetDBPath())
	info, frsErr := database.RunFirstRunSetup(config.GetDBPath())
	// 关键不变性:只要 info.Generated == true,凭据就一定要打印,无论
	// install-info.txt 写文件成功还是失败 — 否则操作员丢失唯一可见的明文密码,
	// bcrypt 不可逆,只能整库重置。早期版本在 frsErr != nil 时静默吞掉 banner,
	// 直接导致 install.sh 报"未生成"且 setting -show 显示 record not found,
	// 整个首次安装看起来像装失败但其实只是文件写不进去。
	if info != nil && info.Generated {
		savedTo := info.InfoPath
		if savedTo == "" {
			savedTo = "(write to disk failed — record this banner NOW)"
		}
		fmt.Println("=================================================")
		fmt.Println("  NexCore x-ui · first-run install info")
		fmt.Println("=================================================")
		fmt.Printf("  panel port: %d\n", info.Port)
		fmt.Printf("  username:   %s\n", info.Username)
		fmt.Printf("  password:   %s\n", info.Password)
		fmt.Printf("  saved to:   %s\n", savedTo)
		fmt.Println("  → http://<server-ip>:" + fmt.Sprint(info.Port))
		fmt.Println("=================================================")
	}
	if frsErr != nil {
		// Logged AFTER the banner so the credentials are emitted first
		// even if a downstream sink (journald rate-limit, broken pipe)
		// truncates output. logger.Warning routes through the same
		// stdout/journal path as fmt.Println and is preserved by systemd.
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
	host := listen
	if host == "" {
		host = autoDetectHost()
	}
	if host == "" {
		host = "<server-ip>"
	}
	// host 已含端口时(autoDetectHost 拼了 :port),URL 就别再追加端口
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
		// best-effort: pick first non-loopback IPv4
		listen = firstNonLoopbackIP()
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
