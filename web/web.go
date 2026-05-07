package web

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"embed"
	"io"
	"io/fs"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"nexcore-x-ui/config"
	"nexcore-x-ui/logger"
	"nexcore-x-ui/util/common"
	"nexcore-x-ui/web/controller"
	"nexcore-x-ui/web/controller/api"
	"nexcore-x-ui/web/job"
	"nexcore-x-ui/web/session"
	"nexcore-x-ui/web/network"
	"nexcore-x-ui/web/service"

	"github.com/BurntSushi/toml"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"github.com/robfig/cron/v3"
	"golang.org/x/text/language"
)

// v2.0:Vue 3 SPA 构建产物。`web/frontend/dist/` 必须存在(至少有
// index.html),否则编译时 embed 失败。CI 在 go build 之前会跑
// `cd web/frontend && npm ci && npm run build` 生成它。
//
//go:embed all:frontend/dist
var spaDistFS embed.FS

// securityHeadersMiddleware applies a conservative set of headers to
// every panel response. They cost nothing on the server and make a
// large class of browser-side bugs (clickjacking, MIME sniffing of an
// uploaded file as HTML, stray inline scripts) harder to exploit. The
// CSP is deliberately permissive on inline styles/scripts because the
// upstream vaxilu/x-ui panel still relies on inline event handlers and
// inline <style>; tightening that requires a frontend rewrite. We do
// forbid object/embed sources and frame-ancestors entirely, which
// covers the most common XSS payload styles without breaking the UI.
func securityHeadersMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		// Pre-set so handlers that send their own Content-Type still
		// get the security headers attached.
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy",
			"accelerometer=(), camera=(), geolocation=(), gyroscope=(), "+
				"magnetometer=(), microphone=(), payment=(), usb=()")
		// CSP — frame-ancestors gives anti-clickjacking even on browsers
		// that ignore X-Frame-Options. 'unsafe-inline' is required for the
		// legacy template's inline handlers; the rest is locked down.
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'unsafe-inline' 'unsafe-eval'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data: blob:; "+
				"font-src 'self' data:; "+
				"connect-src 'self'; "+
				"frame-ancestors 'none'; "+
				"object-src 'none'; "+
				"base-uri 'self'; "+
				"form-action 'self'")
		c.Next()
	}
}

// gzipMiddleware compresses responses for clients that send
// Accept-Encoding: gzip. Stdlib compress/gzip — no new dependency. We
// skip:
//   - clients that didn't ask for it
//   - already-encoded responses (e.g. our static .gz files, or images
//     where Content-Type already starts with image/ video/ audio/)
//   - very small bodies where gzip overhead exceeds the savings
//
// The big win: bundled vue.min.js / antd.min.js (≈3MB combined as
// audited) compress to ~25% of their original size, cutting first-load
// payload from 5MB+ to ~1.3MB. JSON API responses also benefit on
// inbounds list / subscription endpoints.
func gzipMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !strings.Contains(c.GetHeader("Accept-Encoding"), "gzip") {
			c.Next()
			return
		}
		// Skip the SSE-ish / streaming endpoints if any handler ever
		// adds them. We don't have any today, but this guard is cheap.
		if c.GetHeader("Connection") == "Upgrade" {
			c.Next()
			return
		}
		gz := acquireGzipWriter(c.Writer)
		defer releaseGzipWriter(gz)
		wrapper := &gzipResponseWriter{ResponseWriter: c.Writer, gz: gz}
		c.Writer = wrapper
		c.Header("Vary", "Accept-Encoding")
		c.Next()
		// 关键:Finalize 必须在 c.Next() 之后跑。它处理两种情况:
		//   (A) 响应 ≥ 1KB,已经走 gzip 路径 → 关闭 gz stream(写 trailer)
		//   (B) 响应 <  1KB,内容还在 wrapper.buffered 里没出去 → 直接以
		//       未压缩明文写到 socket
		// 之前只调 gz.Close() 漏了 (B),小响应(axios-init.js 这种几百字节的
		// 静态文件 / dashboard 的 /server/status JSON)直接被吞掉,浏览器
		// 看到 ERR_CONTENT_LENGTH_MISMATCH。
		wrapper.Finalize()
	}
}

// gzipResponseWriter only kicks in compression on the first Write that
// has a body large enough to be worth it. For tiny replies (<1KB) we
// pass through plain — the encoding header is set lazily so the client
// sees identity-encoded responses for short bodies.
type gzipResponseWriter struct {
	gin.ResponseWriter
	gz       *gzip.Writer
	wroteHdr bool
	gzipped  bool
	buffered []byte
}

const gzipMinSize = 1024

func (w *gzipResponseWriter) WriteHeader(status int) {
	w.wroteHdr = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *gzipResponseWriter) Write(p []byte) (int, error) {
	if w.gzipped {
		return w.gz.Write(p)
	}
	// Don't compress already-compressed payloads (image/, video/,
	// audio/, application/zip, etc.). Look at Content-Type AFTER the
	// handler has set it — Gin's default ResponseWriter populates
	// Content-Type via http.DetectContentType when not set.
	ct := w.Header().Get("Content-Type")
	if ce := w.Header().Get("Content-Encoding"); ce != "" {
		// Some upstream already encoded it.
		return w.ResponseWriter.Write(p)
	}
	if isCompressedContentType(ct) {
		return w.ResponseWriter.Write(p)
	}
	w.buffered = append(w.buffered, p...)
	if len(w.buffered) < gzipMinSize {
		return len(p), nil
	}
	w.gzipped = true
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Del("Content-Length") // body will change size
	if _, err := w.gz.Write(w.buffered); err != nil {
		return 0, err
	}
	w.buffered = nil
	return len(p), nil
}

func (w *gzipResponseWriter) Flush() {
	// Drain the buffered tail. If we never crossed the gzip threshold
	// flush it as plain bytes; otherwise flush the gzip stream.
	if !w.gzipped && len(w.buffered) > 0 {
		_, _ = w.ResponseWriter.Write(w.buffered)
		w.buffered = nil
	}
	if w.gzipped {
		_ = w.gz.Flush()
	}
	w.ResponseWriter.Flush()
}

// Finalize is called by gzipMiddleware after the handler returns. It is the
// only place where buffered (sub-threshold) responses get flushed AND the
// gzip stream gets closed — gin doesn't call Flush() automatically on
// non-streaming handlers, so without this small responses silently vanish.
func (w *gzipResponseWriter) Finalize() {
	if w.gzipped {
		_ = w.gz.Close()
		return
	}
	if len(w.buffered) > 0 {
		_, _ = w.ResponseWriter.Write(w.buffered)
		w.buffered = nil
	}
}

func isCompressedContentType(ct string) bool {
	if ct == "" {
		return false
	}
	switch {
	case strings.HasPrefix(ct, "image/"):
		// SVG is text and worth compressing; everything else (png, jpeg,
		// webp, gif) is already compressed.
		return !strings.Contains(ct, "svg")
	case strings.HasPrefix(ct, "video/"),
		strings.HasPrefix(ct, "audio/"),
		strings.HasPrefix(ct, "application/zip"),
		strings.HasPrefix(ct, "application/gzip"),
		strings.HasPrefix(ct, "application/x-gzip"),
		strings.HasPrefix(ct, "application/x-bzip2"),
		strings.HasPrefix(ct, "application/x-xz"):
		return true
	}
	return false
}

// gzipWriterPool reuses gzip.Writer instances. Allocating one per
// request churns the heap; with the pool the per-request cost is a
// channel-free Get + Reset, ~150 ns.
var gzipWriterPool = sync.Pool{
	New: func() any {
		w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
		return w
	},
}

func acquireGzipWriter(w io.Writer) *gzip.Writer {
	gz := gzipWriterPool.Get().(*gzip.Writer)
	gz.Reset(w)
	return gz
}

func releaseGzipWriter(gz *gzip.Writer) {
	gz.Reset(io.Discard)
	gzipWriterPool.Put(gz)
}

// maxBodyBytes caps every request body that the panel reads. Without
// this Gin happily slurps unbounded input — a single attacker on a
// throttled link could fill memory by streaming a multi-GB JSON body.
// The xray template upload (PUT /api/v1/xray/template) is the only
// legitimate "large" body and even that fits well under this ceiling
// for any realistic xray config.
const maxBodyBytes = 8 << 20 // 8MB

// limitBodyMiddleware wraps c.Request.Body in http.MaxBytesReader. Any
// downstream handler that c.GetRawData / c.ShouldBindJSON / c.Request.Body.Read
// past the limit will get an EOF/error and should respond 413 — which
// is what BadRequest "invalid_body" already maps to in practice.
func limitBodyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)
		}
		c.Next()
	}
}

// originCSRFMiddleware rejects state-changing requests whose Origin (or
// Referer) header doesn't match the request Host. Standard browser-side
// CSRF defense: cross-origin pages that issue a POST will carry an
// Origin (or at minimum Referer) header pointing at their own origin,
// not at the panel's. SameSite=Lax already blocks most of these but a
// malicious sibling subdomain still slips through; this closes that gap.
//
// GET/HEAD/OPTIONS are exempt (they're supposed to be side-effect-free).
func originCSRFMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}
		var headerHost string
		if origin := c.GetHeader("Origin"); origin != "" {
			if u, err := neturl.Parse(origin); err == nil {
				headerHost = u.Host
			}
		} else if ref := c.GetHeader("Referer"); ref != "" {
			if u, err := neturl.Parse(ref); err == nil {
				headerHost = u.Host
			}
		} else {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"success": false,
				"msg":     "missing Origin/Referer on state-changing request",
			})
			return
		}
		if headerHost == "" || !strings.EqualFold(headerHost, c.Request.Host) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"success": false,
				"msg":     "cross-origin request blocked",
			})
			return
		}
		c.Next()
	}
}

//go:embed translation/*
var i18nFS embed.FS

var startTime = time.Now()

// spaSubFS 把 embed 根目录从 `frontend/dist` 收成 `dist 根`,这样
// gin.StaticFS 直接把 /assets/foo.js 映射到 frontend/dist/assets/foo.js,
// 而不用让上层路由意识到 frontend/dist 这个前缀。同时把 ModTime 钉到
// 进程启动时间,避免每次请求 stat 出 1970 影响 If-Modified-Since。
type spaSubFS struct {
	root fs.FS
}

func newSPASubFS() (*spaSubFS, error) {
	sub, err := fs.Sub(spaDistFS, "frontend/dist")
	if err != nil {
		return nil, err
	}
	return &spaSubFS{root: sub}, nil
}

func (f *spaSubFS) Open(name string) (fs.File, error) {
	file, err := f.root.Open(name)
	if err != nil {
		return nil, err
	}
	return &wrapAssetsFile{File: file}, nil
}

type wrapAssetsFile struct {
	fs.File
}

func (f *wrapAssetsFile) Stat() (fs.FileInfo, error) {
	info, err := f.File.Stat()
	if err != nil {
		return nil, err
	}
	return &wrapAssetsFileInfo{
		FileInfo: info,
	}, nil
}

type wrapAssetsFileInfo struct {
	fs.FileInfo
}

func (f *wrapAssetsFileInfo) ModTime() time.Time {
	return startTime
}

type Server struct {
	httpServer *http.Server
	listener   net.Listener
	certLoader *network.CertLoader // nil when running plain HTTP

	index  *controller.IndexController
	server *controller.ServerController
	xui    *controller.XUIController
	apiV1  *api.V1Controller

	xrayService    service.XrayService
	settingService service.SettingService
	inboundService service.InboundService
	userService    service.UserService
	magicService   service.MagicTokenService

	cron *cron.Cron

	ctx    context.Context
	cancel context.CancelFunc
}

func NewServer() *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		ctx:    ctx,
		cancel: cancel,
	}
}

func (s *Server) initRouter() (*gin.Engine, error) {
	if config.IsDebug() {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.DefaultWriter = io.Discard
		gin.DefaultErrorWriter = io.Discard
		gin.SetMode(gin.ReleaseMode)
	}

	// gin.New() instead of gin.Default(): we explicitly mount only the
	// middleware we need. gin.Default() also bolts on a stdout request
	// logger that, in production where stdout is io.Discard'd anyway,
	// still costs sprintf+time formatting per request. Recovery alone
	// is what we actually want from "Default".
	engine := gin.New()
	engine.Use(gin.Recovery())
	if config.IsDebug() {
		// Only spend the formatting budget when debug logs are wanted.
		engine.Use(gin.Logger())
	}
	// gzip first so other middlewares' bodies also benefit. Skipping
	// the compression for static images / already-encoded content is
	// handled inside the middleware.
	engine.Use(gzipMiddleware())
	// Body size cap applies to every route — panel POSTs, /api/v1, magic
	// link, the lot. Mounted on the root engine so it runs before any
	// per-group middleware reads the body.
	engine.Use(limitBodyMiddleware())
	// Security headers also live at the root so HTML pages, JSON
	// responses, and static assets all get them.
	engine.Use(securityHeadersMiddleware())

	secret, err := s.settingService.GetSecret()
	if err != nil {
		return nil, err
	}

	basePath, err := s.settingService.GetBasePath()
	if err != nil {
		return nil, err
	}
	assetsBasePath := basePath + "assets/"

	store := cookie.NewStore(secret)
	certFile, _ := s.settingService.GetCertFile()
	keyFile, _ := s.settingService.GetKeyFile()
	tlsEnabled := certFile != "" && keyFile != ""
	store.Options(sessions.Options{
		Path:     "/",
		HttpOnly: true,
		Secure:   tlsEnabled,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   3600 * 8,
	})
	engine.Use(sessions.Sessions("session", store))
	engine.Use(func(c *gin.Context) {
		c.Set("base_path", basePath)
	})
	// Static asset cache headers. Two layers:
	//   1. Cache-Control: max-age=1y — browsers cache aggressively.
	//   2. ETag: W/"<version>" — when the binary version bumps, the
	//      stale-cache resources revalidate and pull the new bytes;
	//      same-version revalidations short-circuit to 304 with no body.
	// Both together: zero-body re-fetch on Ctrl+Shift+R against an
	// up-to-date binary, and hard cache-bust on every release.
	assetEtag := `W/"` + config.GetVersion() + `"`
	engine.Use(func(c *gin.Context) {
		uri := c.Request.RequestURI
		if !strings.HasPrefix(uri, assetsBasePath) {
			c.Next()
			return
		}
		c.Header("Cache-Control", "max-age=31536000")
		c.Header("ETag", assetEtag)
		if match := c.GetHeader("If-None-Match"); match != "" && match == assetEtag {
			c.Status(http.StatusNotModified)
			c.Abort()
			return
		}
		c.Next()
	})
	err = s.initI18n(engine)
	if err != nil {
		return nil, err
	}

	// v2.0:Vue 3 SPA 接管所有 UI 路由。dev 模式直接从磁盘读 dist/,
	// 这样 `npm run build` 后不用重启 Go 进程就能看到新前端。
	// prod 模式从 embed FS 读,保持单二进制部署形态。
	var distRoot fs.FS
	if config.IsDebug() {
		distRoot = os.DirFS("web/frontend/dist")
	} else {
		sub, err := newSPASubFS()
		if err != nil {
			return nil, err
		}
		distRoot = sub
	}
	assetsSub, err := fs.Sub(distRoot, "assets")
	if err != nil {
		return nil, err
	}
	engine.StaticFS(basePath+"assets", http.FS(assetsSub))

	// SPA 入口:base path 根 GET → index.html。SPA 用 hash router,
	// 但用户直接访问 /xui/inbounds 之类老路径时这个兜底负责把 SPA 壳
	// 给浏览器,后续路由由 vue-router 处理。
	indexHandler := func(c *gin.Context) {
		f, err := distRoot.Open("index.html")
		if err != nil {
			c.String(http.StatusInternalServerError, "frontend not built — run `cd web/frontend && npm run build`")
			return
		}
		defer f.Close()
		data, err := io.ReadAll(f)
		if err != nil {
			c.String(http.StatusInternalServerError, "read index.html: "+err.Error())
			return
		}
		c.Header("Cache-Control", "no-cache")
		c.Data(http.StatusOK, "text/html; charset=utf-8", data)
	}
	engine.GET(basePath, indexHandler)
	// v2.0 SPA 兜底:面板下任何没匹配到 API/asset 路由的 GET 全部返回
	// SPA 入口。HEAD 一并兜底。其他方法仍按 405 处理。
	engine.NoRoute(func(c *gin.Context) {
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead {
			indexHandler(c)
			return
		}
		c.String(http.StatusNotFound, "not found")
	})

	g := engine.Group(basePath)
	// CSRF defense for the cookie-authenticated panel: state-changing
	// methods must come with an Origin (or, fallback, Referer) header
	// whose host matches the request Host. SameSite=Lax already blocks
	// the most common cross-site POST vectors but a malicious page
	// inside the same eTLD+1 (subdomain takeover, internal proxy) can
	// still issue a top-level POST; the Origin check closes that gap.
	// API token routes are NOT cookie-authed and don't need this — they
	// live under engine.Group("api/v1") and skip the middleware below.
	g.Use(originCSRFMiddleware())

	s.index = controller.NewIndexController(g)
	s.server = controller.NewServerController(g)
	s.xui = controller.NewXUIController(g)

	apiGroup := engine.Group(basePath + "api/v1")
	s.apiV1 = api.NewV1Controller(apiGroup)

	// Magic-link login endpoint. Public (no API token / no panel session),
	// authentication is by the one-time token in the URL.
	g.GET("panel-login/:token", s.handleMagicLogin)

	return engine, nil
}

func (s *Server) handleMagicLogin(c *gin.Context) {
	token := c.Param("token")
	if err := s.magicService.ConsumeMagicToken(token); err != nil {
		c.String(http.StatusUnauthorized, "magic link invalid or expired")
		return
	}
	user, err := s.userService.GetFirstUser()
	if err != nil || user == nil {
		c.String(http.StatusInternalServerError, "no admin user available")
		return
	}
	if err := session.SetLoginUser(c, user); err != nil {
		c.String(http.StatusInternalServerError, "session save failed")
		return
	}
	logger.Infof("magic-link login as %s from %s", user.Username, c.ClientIP())
	basePath := c.GetString("base_path")
	if basePath == "" {
		basePath = "/"
	}
	// v2.0:SPA 入口在 base path 根,hash router 自动把 / 解析到
	// /#/dashboard。原来的 xui/ 老路径已经废弃。
	c.Redirect(http.StatusFound, basePath)
}

func (s *Server) initI18n(engine *gin.Engine) error {
	bundle := i18n.NewBundle(language.SimplifiedChinese)
	bundle.RegisterUnmarshalFunc("toml", toml.Unmarshal)
	err := fs.WalkDir(i18nFS, "translation", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := i18nFS.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = bundle.ParseMessageFileBytes(data, path)
		return err
	})
	if err != nil {
		return err
	}

	findI18nParamNames := func(key string) []string {
		names := make([]string, 0)
		keyLen := len(key)
		for i := 0; i < keyLen-1; i++ {
			if key[i:i+2] == "{{" { // 判断开头 "{{"
				j := i + 2
				isFind := false
				for ; j < keyLen-1; j++ {
					if key[j:j+2] == "}}" { // 结尾 "}}"
						isFind = true
						break
					}
				}
				if isFind {
					names = append(names, key[i+3:j])
				}
			}
		}
		return names
	}

	var localizer *i18n.Localizer

	engine.FuncMap["i18n"] = func(key string, params ...string) (string, error) {
		names := findI18nParamNames(key)
		if len(names) != len(params) {
			return "", common.NewError("find names:", names, "---------- params:", params, "---------- num not equal")
		}
		templateData := map[string]interface{}{}
		for i := range names {
			templateData[names[i]] = params[i]
		}
		return localizer.Localize(&i18n.LocalizeConfig{
			MessageID:    key,
			TemplateData: templateData,
		})
	}

	// Cache localizers by Accept-Language header. NewLocalizer parses
	// the header into a tag list and matches against the bundle —
	// neither is huge work, but doing it on every request was pure
	// waste because the same browser sends the same header every time.
	// Bounded so a flood of distinct fake Accept-Language values can't
	// grow the map without bound.
	const localizerCacheCap = 128
	var (
		localizerCacheMu sync.Mutex
		localizerCache   = map[string]*i18n.Localizer{}
	)
	engine.Use(func(c *gin.Context) {
		accept := c.GetHeader("Accept-Language")
		localizerCacheMu.Lock()
		l, ok := localizerCache[accept]
		if !ok {
			if len(localizerCache) >= localizerCacheCap {
				// Evict an arbitrary entry (Go's map iteration order
				// is randomized, so this is effectively random
				// eviction) — fine for a 128-cap cache where a real
				// LRU would dominate the cost we're trying to save.
				for k := range localizerCache {
					delete(localizerCache, k)
					break
				}
			}
			l = i18n.NewLocalizer(bundle, accept)
			localizerCache[accept] = l
		}
		localizerCacheMu.Unlock()
		localizer = l
		c.Set("localizer", localizer)
		c.Next()
	})

	return nil
}

func (s *Server) startTask() {
	err := s.xrayService.RestartXray(true)
	if err != nil {
		logger.Warning("start xray failed:", err)
	}
	// 每 30 秒检查一次 xray 是否在运行
	s.cron.AddJob("@every 30s", job.NewCheckXrayRunningJob())

	go func() {
		time.Sleep(time.Second * 5)
		// 每 10 秒统计一次流量，首次启动延迟 5 秒，与重启 xray 的时间错开
		s.cron.AddJob("@every 10s", job.NewXrayTrafficJob())
	}()

	// 每 30 秒检查一次 inbound 流量超出和到期的情况
	s.cron.AddJob("@every 30s", job.NewCheckInboundJob())
	// 每一天提示一次流量情况,上海时间8点30
	var entry cron.EntryID
	isTgbotenabled, err := s.settingService.GetTgbotenabled()
	if (err == nil) && (isTgbotenabled) {
		runtime, err := s.settingService.GetTgbotRuntime()
		if err != nil || runtime == "" {
			logger.Errorf("Add NewStatsNotifyJob error[%s],Runtime[%s] invalid,wil run default", err, runtime)
			runtime = "@daily"
		}
		logger.Infof("Tg notify enabled,run at %s", runtime)
		entry, err = s.cron.AddJob(runtime, job.NewStatsNotifyJob())
		if err != nil {
			logger.Warning("Add NewStatsNotifyJob error", err)
			return
		}
	} else {
		s.cron.Remove(entry)
	}
}

func (s *Server) Start() (err error) {
	//这是一个匿名函数，没没有函数名
	defer func() {
		if err != nil {
			s.Stop()
		}
	}()

	loc, err := s.settingService.GetTimeLocation()
	if err != nil {
		return err
	}
	s.cron = cron.New(cron.WithLocation(loc), cron.WithSeconds())
	s.cron.Start()

	if token, generated, terr := s.settingService.EnsureAPIToken(); terr != nil {
		logger.Warning("ensure api token failed:", terr)
	} else if generated {
		logger.Infof("API token generated (record now, will not be shown again): %s", token)
	}

	engine, err := s.initRouter()
	if err != nil {
		return err
	}

	certFile, err := s.settingService.GetCertFile()
	if err != nil {
		return err
	}
	keyFile, err := s.settingService.GetKeyFile()
	if err != nil {
		return err
	}
	listen, err := s.settingService.GetListen()
	if err != nil {
		return err
	}
	port, err := s.settingService.GetPort()
	if err != nil {
		return err
	}
	listenAddr := net.JoinHostPort(listen, strconv.Itoa(port))
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}
	if certFile != "" || keyFile != "" {
		// Hot-reloading loader: re-parses the cert+key whenever either
		// file's mtime changes (acme.sh / certbot post-renew hooks just
		// overwrite the files; we pick up the new pair on the next
		// handshake without dropping live connections). Eager initial
		// load fails loud so a missing/garbage cert at boot doesn't
		// silently downgrade us to plain HTTP.
		loader, err := network.NewCertLoader(certFile, keyFile)
		if err != nil {
			listener.Close()
			return err
		}
		network.SetLogger(func(e error) { logger.Warning("tls cert reload:", e) })
		s.certLoader = loader
		// MinVersion locked to 1.2 — TLS 1.0/1.1 are deprecated and have
		// known weaknesses (BEAST, POODLE, weak hash for handshake).
		// CipherSuites is left nil so Go's default secure-by-default
		// list applies for TLS 1.2; TLS 1.3 ignores the field.
		c := &tls.Config{
			GetCertificate: loader.GetCertificate,
			MinVersion:     tls.VersionTLS12,
		}
		listener = network.NewAutoHttpsListener(listener)
		listener = tls.NewListener(listener, c)
	}

	if certFile != "" || keyFile != "" {
		logger.Info("web server run https on", listener.Addr())
	} else {
		logger.Info("web server run http on", listener.Addr())
	}
	s.listener = listener

	s.startTask()

	s.httpServer = &http.Server{
		Handler: engine,
		// Without these the server lets a slow client hold a goroutine
		// indefinitely (Slowloris). Tune wide enough for the legitimate
		// large requests — uploading an xray template / large cert PEM
		// over a slow link — but tight enough to drop intentional drips.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		// Cap header bytes at 1MB. Default is also 1MB but being
		// explicit makes the intent visible to anyone tuning this later.
		MaxHeaderBytes: 1 << 20,
	}

	go func() {
		s.httpServer.Serve(listener)
	}()

	return nil
}

func (s *Server) Stop() error {
	s.cancel()
	s.xrayService.StopXray()
	if s.cron != nil {
		s.cron.Stop()
	}
	if s.certLoader != nil {
		s.certLoader.Close()
	}
	var err1 error
	var err2 error
	if s.httpServer != nil {
		err1 = s.httpServer.Shutdown(s.ctx)
	}
	if s.listener != nil {
		err2 = s.listener.Close()
	}
	return common.Combine(err1, err2)
}

func (s *Server) GetCtx() context.Context {
	return s.ctx
}

func (s *Server) GetCron() *cron.Cron {
	return s.cron
}
