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
	"nexcore-x-ui/database"
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

// walCheckpointJob implements cron.Job. We don't pull this into web/job/
// because it has zero state and zero deps beyond database — a separate
// file would be more ceremony than the body. Errors are logged but
// non-fatal: if the DB is busy we'll retry on the next tick.
type walCheckpointJob struct{}

func (walCheckpointJob) Run() {
	db := database.GetDB()
	if db == nil {
		return
	}
	if err := db.Exec("PRAGMA wal_checkpoint(PASSIVE)").Error; err != nil {
		logger.Debugf("wal_checkpoint passive: %v", err)
	}
}

// configureTrustedProxies wires gin's trusted-proxy list. Reads
// NEXCORE_TRUSTED_PROXIES (comma-separated CIDRs / IPs) from env. Empty
// or unset = no proxies trusted (the safe default). Returns an error if
// gin itself rejects the list (bad CIDR, etc.).
func configureTrustedProxies(engine *gin.Engine) error {
	raw := strings.TrimSpace(os.Getenv("NEXCORE_TRUSTED_PROXIES"))
	if raw == "" {
		return engine.SetTrustedProxies(nil)
	}
	parts := strings.Split(raw, ",")
	cidrs := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			cidrs = append(cidrs, p)
		}
	}
	if len(cidrs) == 0 {
		return engine.SetTrustedProxies(nil)
	}
	return engine.SetTrustedProxies(cidrs)
}

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
		// Upstream already encoded it. If we'd buffered any bytes from
		// earlier Write calls (handler did Write(prefix) then set
		// Content-Encoding, then Write(rest)), flush them first — they
		// are part of that already-encoded body and would otherwise be
		// silently dropped.
		if len(w.buffered) > 0 {
			if _, err := w.ResponseWriter.Write(w.buffered); err != nil {
				return 0, err
			}
			w.buffered = nil
		}
		return w.ResponseWriter.Write(p)
	}
	if isCompressedContentType(ct) {
		// Same flush-before-bypass invariant for image/* etc.
		if len(w.buffered) > 0 {
			if _, err := w.ResponseWriter.Write(w.buffered); err != nil {
				return 0, err
			}
			w.buffered = nil
		}
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

// WriteString routes back through Write so callers using
// io.WriteString(c.Writer, …) or c.Writer.WriteString(…) hit the same
// buffer / threshold / Content-Encoding logic as Write([]byte). Without
// this override Gin's embedded responseWriter implementation would
// invoke ResponseWriter.Write directly, silently bypassing both gzip
// compression AND the small-response buffer flush — i.e. exactly the
// v1.0.4 ERR_CONTENT_LENGTH_MISMATCH bug, just via a different entry
// point. This is the only public method on the interface that needs an
// explicit forward; ReadFrom / Hijack / etc. don't write the body.
func (w *gzipResponseWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
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
	// Trusted-proxy policy.
	//
	// gin's default is "trust every X-Forwarded-For header from anyone",
	// which means c.ClientIP() returns whatever the caller wants when the
	// panel is exposed directly to the public internet. The login
	// throttler keys on c.ClientIP(), so that default is a free
	// brute-force-bypass: the attacker simply rotates X-Forwarded-For per
	// request and never trips the lockout.
	//
	// Default here is `nil` — no proxy is trusted, c.ClientIP() ignores
	// X-Forwarded-For and returns the direct TCP peer. Operators who run
	// the panel behind a reverse proxy (Caddy, nginx, Cloudflare Tunnel)
	// set NEXCORE_TRUSTED_PROXIES to a comma-separated list of CIDRs of
	// the front-end's source addresses. SetTrustedProxies returning an
	// error here would mean a typo in the env var; we log and fall back
	// to the safe default so a misconfiguration can't open the bypass.
	if err := configureTrustedProxies(engine); err != nil {
		logger.Warningf("trusted proxies misconfigured (%v); falling back to direct peer only", err)
		_ = engine.SetTrustedProxies(nil)
	}
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
	// 安全入口:把 secureEntryPath 拼到 webBasePath 后作为生效前缀。
	// e.g. webBasePath="/" + secureEntryPath="aBc8d2x9" → "/aBc8d2x9/"
	// 这条之外的任何路径(包括 "/" 本身)走 NoRoute 一律 404,扫端口
	// 看到的是裸 404 不再吐 SPA。
	secureEntryEnabled := s.settingService.GetSecureEntryEnabled()
	secureEntryPath := strings.TrimSpace(s.settingService.GetSecureEntryPath())
	if secureEntryEnabled && secureEntryPath != "" {
		basePath = basePath + secureEntryPath + "/"
		logger.Info("secure entry enabled, panel base path = ", basePath)
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

	// SPA 入口:base path 根 GET → index.html。SPA 用 createWebHistory,
	// 任何匹配不到 API/asset 的 GET 都通过 noRoute 兜底回这里,客户端 vue-router 接管。
	//
	// 同时把 basePath 注入到 HTML 里,替换 __NX_BASE__ 占位符。这样同一份
	// SPA bundle 部署在 "/" 和 "/admin/" 都能跑:axios 用它做 baseURL,
	// vue-router 用它做 history base,前端不再硬编码任何前缀。
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
		// JS-string-safe: basePath comes from settings (operator-controlled,
		// so already trusted), but defense-in-depth — escape the only
		// chars that could break the inline `window.__NX_BASE__ = "..."`
		// statement: backslash, double-quote, and the literal "</" that
		// would close the surrounding <script> tag prematurely. Order
		// matters: backslash MUST come first so the others' replacement
		// backslashes aren't double-escaped.
		safeBase := strings.NewReplacer(
			`\`, `\\`,
			`"`, `\"`,
			`</`, `<\/`,
		).Replace(basePath)
		// 占位符是 %%NX_BASE%%,刻意跟 JS 变量名 window.__NX_BASE__ 区分。
		// v2.0.5 的故障:占位符跟变量名同名 __NX_BASE__,ReplaceAll 把变量名
		// 也替换成 basePath,生成 window./ = "/" 这种破 JS 语法 → 全站
		// SyntaxError。占位符必须跟变量名分离。
		injected := strings.ReplaceAll(string(data), "%%NX_BASE%%", safeBase)
		c.Header("Cache-Control", "no-cache")
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(injected))
	}
	engine.GET(basePath, indexHandler)
	// v2.0 SPA 兜底:basePath 下任何没匹配到 API/asset 路由的 GET 全部返回
	// SPA 入口(让 vue-router 接管 history)。HEAD 一并兜底。
	//
	// 安全入口启用时,basePath 自带秘密 slug。落在 basePath 之外的请求
	// (e.g. /、/login、/admin/、/wp-admin/)一律返裸 404 — 扫端口的工具
	// 看不到任何登录线索,大幅提升发现门槛。这是 secureEntryEnabled 的
	// 主作用,前置 basePath 改动只是把所有合法路径都迁到 secret slug 下。
	engine.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead {
			if strings.HasPrefix(path, basePath) {
				indexHandler(c)
				return
			}
			// 未启用安全入口时保持旧行为(SPA 兜底任何 GET),避免
			// 已经把 webBasePath 设成非 "/" 的老用户突然 404。
			if !secureEntryEnabled {
				indexHandler(c)
				return
			}
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

	// Magic-link login.
	//
	// Old design: GET /panel-login/<plaintext-token> — server consumed the
	// token from the URL path, set the cookie, redirected. That put the
	// plaintext token into every layer of the request lifecycle: gin
	// access logs, any reverse proxy's access log, the browser history,
	// and (when the user clicked a link from the freshly-loaded SPA) the
	// Referer header to whatever the next outbound request was. Even
	// after the token was consumed, those secondary log lines persisted
	// the now-dead-but-once-valid token forever.
	//
	// New design: the URL the operator shares is
	//
	//	{base}magic-login#tk=<plaintext>
	//
	// — the token lives in the fragment, which browsers never send over
	// the wire. Server side, /magic-login is just an SPA route (resolved
	// by noRoute → indexHandler). The SPA route component reads the
	// fragment, POSTs the token to /panel-login/consume, gets a session
	// cookie back, and navigates to /dashboard. The fragment is cleared
	// from history.replaceState so a back-button trip doesn't leak it
	// either.
	//
	// /panel-login/consume is the only HTTP entry point that ever sees
	// the plaintext, and only as a POST body — so it's never in any URL
	// that gets logged.
	g.POST("panel-login/consume", s.handleMagicConsume)

	return engine, nil
}

// handleMagicConsume validates a magic token POSTed by the SPA and
// promotes the request to a logged-in panel session. Always returns
// 401 on any failure mode so the caller can't distinguish "no such
// token" from "expired" from "already used".
func (s *Server) handleMagicConsume(c *gin.Context) {
	var body struct {
		Token string `json:"token"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Token) == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"msg":     "invalid magic link",
		})
		return
	}
	if err := s.magicService.ConsumeMagicToken(body.Token); err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"msg":     "magic link invalid or expired",
		})
		return
	}
	user, err := s.userService.GetFirstUser()
	if err != nil || user == nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"msg":     "no admin user available",
		})
		return
	}
	if err := session.SetLoginUser(c, user); err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"msg":     "session save failed",
		})
		return
	}
	logger.Infof("magic-link login as %s from %s", user.Username, c.ClientIP())
	c.JSON(http.StatusOK, gin.H{"success": true})
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
	// 启动时一次性兜底:把所有 inbound 内嵌的 clients[].email 同步到
	// client_traffics 表。覆盖 v2.0.0 之前 AddInbound 漏掉的旧 inbound,
	// 让 modal 客户列表 / 入站卡 badge 永远对上。
	if err := s.inboundService.SyncAllClientTraffics(); err != nil {
		logger.Warning("sync embedded client_traffics failed:", err)
	}

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

	// WAL checkpoint cron.
	//
	// SQLite WAL mode 写入会累积到 *.db-wal 文件,默认 1000 页(~4MB)
	// autocheckpoint 触发一次合并到主库。在持续写入(流量统计每 10s
	// flush)+ 长时间不重启的节点机上,这个文件能涨到几十 MB,占满 1G
	// 机器的 inode/磁盘缓存。每 5 分钟主动跑 PASSIVE checkpoint 把它
	// 压回主库,不阻塞读写、不影响运行中的事务,代价接近零。
	//
	// 不用 FULL/RESTART/TRUNCATE:那几种会等所有 reader 退出再合并,
	// 长事务下会阻塞;PASSIVE 只合并能合并的部分,合不上也无害。
	s.cron.AddJob("@every 5m", walCheckpointJob{})
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
