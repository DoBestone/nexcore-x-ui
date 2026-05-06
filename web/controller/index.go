package controller

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"nexcore-x-ui/logger"
	"nexcore-x-ui/web/job"
	"nexcore-x-ui/web/service"
	"nexcore-x-ui/web/session"
)

// loginThrottle is a tiny in-memory failure counter keyed by remote IP.
// Crossing the threshold delays subsequent attempts by ~the lockout
// window so an attacker can't usefully brute-force from one IP. We
// deliberately key on IP only (not IP+user) so an attacker probing many
// usernames from one IP can't dodge the lockout by rotating names.
//
// State is process-local — restarting the panel clears it. That's
// acceptable for a single-node panel; multi-node deployments should
// front the panel with a real WAF/rate-limiter.
var loginThrottle = newThrottler(7, 10*time.Minute)

type throttler struct {
	mu        sync.Mutex
	failures  map[string]*failureRecord
	threshold int
	window    time.Duration
}

type failureRecord struct {
	count    int
	firstAt  time.Time
	blockUntil time.Time
}

func newThrottler(threshold int, window time.Duration) *throttler {
	return &throttler{
		failures:  make(map[string]*failureRecord),
		threshold: threshold,
		window:    window,
	}
}

// retryAfter returns >0 when the caller is currently locked out, in
// seconds. Zero means "go ahead, attempt the login".
func (t *throttler) retryAfter(key string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.failures[key]
	if !ok {
		return 0
	}
	now := time.Now()
	if now.Before(r.blockUntil) {
		return int(r.blockUntil.Sub(now).Seconds()) + 1
	}
	return 0
}

func (t *throttler) recordFailure(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	r, ok := t.failures[key]
	if !ok || now.Sub(r.firstAt) > t.window {
		t.failures[key] = &failureRecord{count: 1, firstAt: now}
		return
	}
	r.count++
	if r.count >= t.threshold {
		r.blockUntil = now.Add(t.window)
	}
}

func (t *throttler) recordSuccess(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.failures, key)
}

type LoginForm struct {
	Username string `json:"username" form:"username"`
	Password string `json:"password" form:"password"`
}

type IndexController struct {
	BaseController

	userService service.UserService
}

func NewIndexController(g *gin.RouterGroup) *IndexController {
	a := &IndexController{}
	a.initRouter(g)
	return a
}

func (a *IndexController) initRouter(g *gin.RouterGroup) {
	g.GET("/", a.index)
	g.POST("/login", a.login)
	g.GET("/logout", a.logout)
}

func (a *IndexController) index(c *gin.Context) {
	if session.IsLogin(c) {
		c.Redirect(http.StatusTemporaryRedirect, "xui/")
		return
	}
	html(c, "login.html", "登录", nil)
}

func (a *IndexController) login(c *gin.Context) {
	ip := getRemoteIp(c)
	if wait := loginThrottle.retryAfter(ip); wait > 0 {
		c.Header("Retry-After", time.Duration(wait*int(time.Second)).String())
		logger.Warningf("login throttled for %s (retry after %ds)", ip, wait)
		pureJsonMsg(c, false, "登录尝试过于频繁,请稍后再试")
		return
	}

	var form LoginForm
	err := c.ShouldBind(&form)
	if err != nil {
		pureJsonMsg(c, false, "数据格式错误")
		return
	}
	if form.Username == "" {
		pureJsonMsg(c, false, "请输入用户名")
		return
	}
	if form.Password == "" {
		pureJsonMsg(c, false, "请输入密码")
		return
	}
	user := a.userService.CheckUser(form.Username, form.Password)
	timeStr := time.Now().Format("2006-01-02 15:04:05")
	if user == nil {
		loginThrottle.recordFailure(ip)
		job.NewStatsNotifyJob().UserLoginNotify(form.Username, ip, timeStr, 0)
		logger.Infof("login failed for user %q from %s", form.Username, ip)
		pureJsonMsg(c, false, "用户名或密码错误")
		return
	}
	loginThrottle.recordSuccess(ip)
	logger.Infof("%s login success,Ip Address:%s\n", form.Username, ip)
	job.NewStatsNotifyJob().UserLoginNotify(form.Username, ip, timeStr, 1)

	err = session.SetLoginUser(c, user)
	logger.Info("user", user.Id, "login success")
	jsonMsg(c, "登录", err)
}

func (a *IndexController) logout(c *gin.Context) {
	user := session.GetLoginUser(c)
	if user != nil {
		logger.Info("user", user.Id, "logout")
	}
	session.ClearSession(c)
	c.Redirect(http.StatusTemporaryRedirect, c.GetString("base_path"))
}
