package controller

import (
	"embed"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"nexcore-x-ui/web/service"
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
}

func NewAPIPanelController(g *gin.RouterGroup) *APIPanelController {
	a := &APIPanelController{}
	a.initRouter(g)
	return a
}

func (a *APIPanelController) initRouter(g *gin.RouterGroup) {
	g = g.Group("/api")
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
	if err := a.clientTrafficService.SetLimits(email, body); err != nil {
		jsonMsg(c, "更新", err)
		return
	}
	a.xrayService.SetToNeedRestart()
	jsonMsg(c, "更新", nil)
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
