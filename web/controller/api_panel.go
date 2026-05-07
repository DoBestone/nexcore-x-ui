package controller

import (
	"embed"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"nexcore-x-ui/logger"
	"nexcore-x-ui/web/service"
	"nexcore-x-ui/web/session"
)

// docsFS embeds the markdown source so the panel can serve it inline. The
// directive lives in this file so the docs/ folder is part of the binary
// regardless of build context.
//
//go:embed all:docs
var docsFS embed.FS

// APIPanelController exposes panel-session-authenticated endpoints for
// browser pages that need to call the same services that /api/v1 exposes
// to external systems. Wiring a separate controller (rather than calling
// /api/v1 from the browser with an API token) keeps tokens server-side.
type APIPanelController struct {
	tokenService         service.APITokenService
	apiLogService        service.APILogService
	updateService        service.UpdateService
	clientTrafficService service.ClientTrafficService
	xrayService          service.XrayService
	clientService        service.ClientService
	inboundService       service.InboundService
	shareService         service.ShareService
	firewallService      service.FirewallService
}

func NewAPIPanelController(g *gin.RouterGroup) *APIPanelController {
	a := &APIPanelController{}
	a.initRouter(g)
	return a
}

func (a *APIPanelController) initRouter(g *gin.RouterGroup) {
	g = g.Group("/api")
	g.GET("/me", a.me)
	g.GET("/tokens", a.listTokens)
	g.POST("/tokens", a.createToken)
	g.POST("/tokens/:id/revoke", a.revokeToken)
	g.DELETE("/tokens/:id", a.deleteToken)

	g.GET("/logs", a.listLogs)
	g.DELETE("/logs", a.purgeLogs)

	g.GET("/docs", a.docs)

	g.GET("/update/check", a.updateCheck)
	g.POST("/update/apply", a.updateApply)

	// v1.1.0 per-client traffic 面板侧:给入站详情 modal 用,session-auth。
	// 跟 /api/v1/clients/* 镜像但不要 token,前端 ajax 直接调即可。
	g.GET("/inbounds/:id/client-traffics", a.listClientTraffics)
	g.POST("/clients/:email/reset-traffic", a.resetClientTraffic)
	g.POST("/clients/:email/limits", a.patchClientLimits)  // 浏览器走全局 form-urlencoded
	g.POST("/clients/:email/enable", a.setClientEnable)    // 启停切换专用,镜像 /api/v1/clients/:email/enable

	// v1.1.x:面板侧 client CRUD,给客户端流量 modal 用。镜像
	// /api/v1/inbounds/:id/clients/* 但 session-auth,前端不需要 token。
	g.POST("/inbounds/:id/clients", a.addInboundClient)
	g.PUT("/inbounds/:id/clients/:identifier", a.updateInboundClient)
	g.DELETE("/inbounds/:id/clients/:identifier", a.deleteInboundClient)
	g.GET("/inbounds/:id/info", a.getInboundInfo) // 给 modal 拿 protocol 决定字段
	g.GET("/inbounds/:id/links", a.inboundLinks)  // email→link map,给 modal 行内二维码用
	g.GET("/online-ips-by-email", a.onlineIPsByEmail)

	// firewall-status:面板检测系统层 UFW / firewalld 状态 + 已放行 TCP
	// 端口列表。前端用这个跟 inbound 端口做差集,提示"端口被防火墙拦
	// 了,客户端连不进来"。只读探测,不动用户配置;详见 firewall.go。
	g.GET("/firewall-status", a.firewallStatus)

	// xray 进程操作:面板 dashboard 用。/api/v1/xray/restart 已经存在
	// 但需要 token,这里给浏览器走 session-auth 的镜像。stop 在 v1 没暴露
	// (调试场景多),也补一条;logs 单独 GET 让前端弹层展示最近 stdout/stderr,
	// 不再让用户对着 systemctl 排错。config 把当前生效的 xray JSON 拉回来,
	// 配合「修改后未生效」这种诊断场景。
	g.POST("/xray/restart", a.xrayRestart)
	g.POST("/xray/stop", a.xrayStop)
	g.GET("/xray/logs", a.xrayLogs)
	g.GET("/xray/config", a.xrayEffectiveConfig)
}

// me — SPA 启动时打,用来判断当前 session 是否登录。未登录走 401(由
// XUIController.checkLogin 中间件统一拦截)。
func (a *APIPanelController) me(c *gin.Context) {
	user := session.GetLoginUser(c)
	if user == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"success": false, "msg": "未登录"})
		return
	}
	jsonObj(c, gin.H{
		"id":       user.Id,
		"username": user.Username,
	}, nil)
}

// ---------- tokens ----------

func (a *APIPanelController) listTokens(c *gin.Context) {
	tokens, err := a.tokenService.ListTokens()
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	jsonObj(c, tokens, nil)
}

func (a *APIPanelController) createToken(c *gin.Context) {
	// Browser sends form-urlencoded by default (see web/assets/js/axios-init.js
	// global interceptor inherited from vaxilu/x-ui). ShouldBind picks JSON or
	// form based on Content-Type, so this works for both browser and curl.
	var body struct {
		Name       string `json:"name"        form:"name"`
		Scope      string `json:"scope"       form:"scope"`
		TTLSeconds int64  `json:"ttlSeconds"  form:"ttlSeconds"`
	}
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, "创建 token", err)
		return
	}
	ttl := time.Duration(body.TTLSeconds) * time.Second
	t, err := a.tokenService.CreateToken(body.Name, body.Scope, ttl)
	if err != nil {
		jsonMsg(c, "创建 token", err)
		return
	}
	jsonObj(c, gin.H{
		"id":        t.Row.Id,
		"name":      t.Row.Name,
		"token":     t.Plaintext, // plaintext, exposed once; DB stores SHA256
		"scope":     t.Row.Scope,
		"createdAt": t.Row.CreatedAt,
		"expiresAt": t.Row.ExpiresAt,
	}, nil)
}

func (a *APIPanelController) revokeToken(c *gin.Context) {
	id := getUriId(c)
	if err := a.tokenService.RevokeToken(int(id)); err != nil {
		jsonMsg(c, "撤销 token", err)
		return
	}
	jsonMsg(c, "撤销 token", nil)
}

func (a *APIPanelController) deleteToken(c *gin.Context) {
	id := getUriId(c)
	if err := a.tokenService.DeleteToken(int(id)); err != nil {
		jsonMsg(c, "删除 token", err)
		return
	}
	jsonMsg(c, "删除 token", nil)
}

// ---------- logs ----------

func (a *APIPanelController) listLogs(c *gin.Context) {
	q := service.APILogQuery{
		Path:      c.Query("path"),
		Method:    c.Query("method"),
		TokenName: c.Query("token"),
	}
	if v := c.Query("page"); v != "" {
		_, _ = fmtSscanf(v, &q.Page)
	}
	if v := c.Query("size"); v != "" {
		_, _ = fmtSscanf(v, &q.Size)
	}
	items, total, err := a.apiLogService.Query(q)
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	jsonObj(c, gin.H{"items": items, "total": total}, nil)
}

func (a *APIPanelController) purgeLogs(c *gin.Context) {
	n, err := a.apiLogService.Purge()
	if err != nil {
		jsonMsg(c, "清理日志", err)
		return
	}
	jsonObj(c, gin.H{"deleted": n}, nil)
}

// ---------- self-update ----------

func (a *APIPanelController) updateCheck(c *gin.Context) {
	out, err := a.updateService.CheckLatest()
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	jsonObj(c, out, nil)
}

func (a *APIPanelController) updateApply(c *gin.Context) {
	var body struct {
		Version string `json:"version" form:"version"`
	}
	_ = c.ShouldBind(&body)
	out, err := a.updateService.ApplyLatest(body.Version)
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	jsonObj(c, out, nil)
}

// ---------- per-client traffic (v1.1.0) ----------

func (a *APIPanelController) listClientTraffics(c *gin.Context) {
	id := int(getUriId(c))
	rows, err := a.clientTrafficService.ListByInbound(id)
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	jsonObj(c, rows, nil)
}

func (a *APIPanelController) resetClientTraffic(c *gin.Context) {
	email := c.Param("email")
	if err := a.clientTrafficService.ResetTraffic(email); err != nil {
		jsonMsg(c, "重置流量", err)
		return
	}
	jsonMsg(c, "重置流量", nil)
}

func (a *APIPanelController) patchClientLimits(c *gin.Context) {
	email := c.Param("email")
	var body service.SetLimitsParams
	// 前端 ctModal 显式 application/json,所以这里走 ShouldBindJSON;
	// SetLimitsParams 用 *int64/*bool 指针语义,form-urlencoded 表达不了
	// "未设置"和"显式 0/false" 的区别。
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonMsg(c, "更新", err)
		return
	}
	enableStr := "<unset>"
	if body.Enable != nil {
		enableStr = fmt.Sprintf("%v", *body.Enable)
	}
	logger.Info(fmt.Sprintf("patchClientLimits email=%q enable=%s", email, enableStr))
	if err := a.clientTrafficService.SetLimits(email, body); err != nil {
		jsonMsg(c, "更新", err)
		return
	}
	applyImmediately(&a.xrayService)
	jsonMsg(c, "更新", nil)
}

// setClientEnable — 客户端启停的专用轻量端点,前端开关组件直接调,不需要
// 拼 SetLimitsParams 形状。body 只看一个 enable 字段;ShouldBind 自动按
// Content-Type 选 JSON / form,浏览器侧两种写法都能用。
func (a *APIPanelController) setClientEnable(c *gin.Context) {
	email := c.Param("email")
	var body struct {
		Enable bool `json:"enable" form:"enable"`
	}
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, "切换启用", err)
		return
	}
	enable := body.Enable
	if err := a.clientTrafficService.SetLimits(email, service.SetLimitsParams{Enable: &enable}); err != nil {
		jsonMsg(c, "切换启用", err)
		return
	}
	applyImmediately(&a.xrayService)
	jsonObj(c, gin.H{"email": email, "enable": enable}, nil)
}

// applyImmediately:用户主动 toggle / 编辑 client 时,立即触发一次 xray
// reload,不等 10s cron。RestartXray(false) 内置 config-equality 短路,
// 多个并发 toggle 会在 lock 上排队,代价小。失败兜底用 SetToNeedRestart
// 让下一拍 cron 兜过来。
func applyImmediately(xs *service.XrayService) {
	if err := xs.RestartXray(false); err != nil {
		logger.Warning("立即重启 xray 失败,留给 cron 兜底:", err)
		xs.SetToNeedRestart()
		return
	}
	logger.Info("xray 已重启 (per-client 改动后立即生效)")
}

// ---------- inbound client CRUD (v1.1.2 panel-side) ----------

// addInboundClient — 前端"+添加客户端"按钮调。body 是协议对应的
// client object,直接传给 ClientService。错误经 humanizeApiError 映射
// 成中文(jsonMsg 已经做了 err.Error 拼接)。
func (a *APIPanelController) addInboundClient(c *gin.Context) {
	id := int(getUriId(c))
	var client map[string]any
	if err := c.ShouldBindJSON(&client); err != nil {
		jsonMsg(c, "添加客户端", err)
		return
	}
	if _, err := a.clientService.AddClient(id, client); err != nil {
		jsonMsg(c, "添加客户端", err)
		return
	}
	a.xrayService.SetToNeedRestart()
	jsonMsg(c, "添加客户端", nil)
}

func (a *APIPanelController) updateInboundClient(c *gin.Context) {
	id := int(getUriId(c))
	identifier := c.Param("identifier")
	var client map[string]any
	if err := c.ShouldBindJSON(&client); err != nil {
		jsonMsg(c, "更新客户端", err)
		return
	}
	if _, err := a.clientService.UpdateClient(id, identifier, client); err != nil {
		jsonMsg(c, "更新客户端", err)
		return
	}
	a.xrayService.SetToNeedRestart()
	jsonMsg(c, "更新客户端", nil)
}

func (a *APIPanelController) deleteInboundClient(c *gin.Context) {
	id := int(getUriId(c))
	identifier := c.Param("identifier")
	if _, err := a.clientService.DeleteClient(id, identifier); err != nil {
		jsonMsg(c, "删除客户端", err)
		return
	}
	a.xrayService.SetToNeedRestart()
	jsonMsg(c, "删除客户端", nil)
}

// getInboundInfo — modal 弹出时调一次,返回 protocol + 现有 client 数,
// 让前端根据协议类型决定显示什么字段(vless: id+flow+email,vmess:
// id+alterId+email,trojan/ss-2022: password+email)
func (a *APIPanelController) getInboundInfo(c *gin.Context) {
	id := int(getUriId(c))
	in, err := a.inboundService.GetInbound(id)
	if err != nil {
		jsonMsg(c, "获取入站", err)
		return
	}
	jsonObj(c, gin.H{
		"id":       in.Id,
		"protocol": in.Protocol,
		"port":     in.Port,
		"settings": in.Settings,
	}, nil)
}

// onlineIPsByEmail — 客户端流量 modal 用,每行显示该 client 当前在线 IP。
// 内存 60s TTL,无需 DB。返回 { "alice@x": ["1.2.3.4"], ... }。
func (a *APIPanelController) onlineIPsByEmail(c *gin.Context) {
	jsonObj(c, service.GetOnlineIPService().GetIPsByEmail(), nil)
}

// inboundLinks — 返回该 inbound 下每个 email 客户端的分享链接(email→link)。
//
// host 默认取请求 Host(去 port);用户也可显式 ?host= 覆盖。这个路由是
// panel session-auth 而不是 /api/v1 token-auth,所以不强制 subAllowedHosts
// 白名单 —— 已经登录的面板用户本来就能改 settings,白名单对他没意义。但
// **必须**走 ValidateShareHostSyntactic:c.Request.Host 是客户端可控的
// header,被 CRLF / `/` / 空白一注入,生成的分享链接就跑到 attacker.example
// 去了。审计指出过这条 host header 注入面;Validate* 把字符层面收掉后,
// panel 路径剩下的就只是纯白名单决策。
func (a *APIPanelController) inboundLinks(c *gin.Context) {
	id := int(getUriId(c))
	host := strings.TrimSpace(c.Query("host"))
	if host == "" {
		host = c.Request.Host
		if i := strings.IndexByte(host, ':'); i >= 0 {
			host = host[:i]
		}
	}
	cleaned, reason := service.ValidateShareHostSyntactic(host)
	if reason != "" {
		jsonObj(c, nil, errors.New("invalid host: "+reason))
		return
	}
	links, err := a.shareService.LinksByEmail(id, cleaned)
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	jsonObj(c, links, nil)
}

// ---------- firewall ----------

// firewallStatus 直接把 FirewallService.Status() 返回值丢回去。前端
// 30s 轮询一次即可(后端自己 cache 30s,所以多刷不会真去 fork ufw)。
// session-auth 路由,不放到 /api/v1 token 路径 —— 这是 panel telemetry,
// 跨节点 API 用户场景下意义不大。
func (a *APIPanelController) firewallStatus(c *gin.Context) {
	jsonObj(c, a.firewallService.Status(), nil)
}

// ---------- xray ops ----------

// xrayRestart force-restarts the xray subprocess. Equivalent to
// /api/v1/xray/restart but session-auth so the dashboard can call it
// without provisioning a token. Force=true (vs config-equality short-circuit
// in cron-driven restarts) — the user clicked the button precisely because
// they want xray to actually bounce.
func (a *APIPanelController) xrayRestart(c *gin.Context) {
	if err := a.xrayService.RestartXray(true); err != nil {
		jsonMsg(c, "重启 xray", err)
		return
	}
	jsonObj(c, gin.H{"restarted": true}, nil)
}

// xrayStop terminates the xray subprocess without restarting. The cron
// in InboundController doesn't auto-restart unless inbound config changes,
// so xray will stay stopped until the user hits "重启" — that's the
// intended way to "pause" a node from the panel.
func (a *APIPanelController) xrayStop(c *gin.Context) {
	if err := a.xrayService.StopXray(); err != nil {
		jsonMsg(c, "停止 xray", err)
		return
	}
	jsonObj(c, gin.H{"stopped": true}, nil)
}

// xrayLogs returns the xray subprocess' recent stdout/stderr buffer
// (last ~100 lines, in-memory, cleared on each restart). Used by the
// dashboard's 日志 dialog so users don't need shell access for the
// common "why won't xray start" diagnosis.
func (a *APIPanelController) xrayLogs(c *gin.Context) {
	jsonObj(c, gin.H{"logs": a.xrayService.GetRecentLogs()}, nil)
}

// xrayEffectiveConfig returns the JSON that would be (or was last) handed
// to xray. Helps diagnose "I edited an inbound but the change isn't
// visible" — the panel-side state vs xray's actual loaded config diverge
// when xray is stopped or last restart errored before reload.
func (a *APIPanelController) xrayEffectiveConfig(c *gin.Context) {
	cfg, err := a.xrayService.GetEffectiveConfig()
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.String(http.StatusOK, cfg)
}

// ---------- docs ----------

func (a *APIPanelController) docs(c *gin.Context) {
	data, err := docsFS.ReadFile("docs/api.md")
	if err != nil {
		c.String(http.StatusNotFound, "API 文档未嵌入二进制(go embed 失败)")
		return
	}
	c.Header("Content-Type", "text/markdown; charset=utf-8")
	c.Data(http.StatusOK, "text/markdown; charset=utf-8", data)
}
