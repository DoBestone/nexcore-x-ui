package controller

// 一键域名绑定的 panel session-auth 路由。
//
//   GET  /xui/domain/detect — preflight,read-only,前端进绑定页时拉一次
//   POST /xui/domain/bind   — 真做事:申请证书 + 写 nginx + 起入站 + reload
//   POST /xui/domain/unbind — 反操作,删 nginx 配置 + reload
//
// 业务逻辑全在 DomainBindingService 里;controller 只负责绑参 + 转 jsonObj。

import (
	"github.com/gin-gonic/gin"

	"nexcore-x-ui/web/service"
)

type DomainController struct {
	BaseController
	domainService service.DomainBindingService
	cfService     service.CloudflareService
	xrayService   service.XrayService
}

func NewDomainController(g *gin.RouterGroup) *DomainController {
	a := &DomainController{}
	a.initRouter(g)
	return a
}

func (a *DomainController) initRouter(g *gin.RouterGroup) {
	g = g.Group("/domain")
	g.GET("/detect", a.detect)
	g.POST("/bind", a.bind)
	g.POST("/unbind", a.unbind)
	g.GET("/cf-status", a.cfStatus)
	g.POST("/cf-toggle", a.cfToggle)
}

func (a *DomainController) detect(c *gin.Context) {
	jsonObj(c, a.domainService.DetectEnvironment(), nil)
}

func (a *DomainController) bind(c *gin.Context) {
	var req service.BindRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonMsg(c, "绑定", err)
		return
	}
	res := a.domainService.BindDomain(&req)
	if res.Success {
		// 入站新增了,触发一次 xray 重启把 WS 入站接到 nginx 后面
		a.xrayService.SetToNeedRestart()
	}
	jsonObj(c, res, nil)
}

func (a *DomainController) unbind(c *gin.Context) {
	var body struct {
		Domain string `json:"domain" form:"domain"`
	}
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, "解绑", err)
		return
	}
	err := a.domainService.UnbindDomain(body.Domain)
	jsonMsg(c, "解绑", err)
}

// cfStatus 返回某域名当前的 CF 代理状态(橙云/灰云 + 解析目标)。
//   GET /xui/domain/cf-status?domain=node1.example.com
// 前端进域名页 / 切完 toggle 后调,刷新展示。
func (a *DomainController) cfStatus(c *gin.Context) {
	domain := c.Query("domain")
	state, err := a.cfService.GetRecordState(domain)
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	jsonObj(c, state, nil)
}

// cfToggle 切换 proxied 字段。Body: {domain, proxied}。返回更新后的状态,
// 前端不必再调一次 cfStatus 拉。
func (a *DomainController) cfToggle(c *gin.Context) {
	var body struct {
		Domain  string `json:"domain"`
		Proxied bool   `json:"proxied"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonMsg(c, "切换 CF 代理", err)
		return
	}
	state, err := a.cfService.SetProxied(body.Domain, body.Proxied)
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	jsonObj(c, state, nil)
}
