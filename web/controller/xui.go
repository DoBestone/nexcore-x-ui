package controller

import (
	"github.com/gin-gonic/gin"
)

type XUIController struct {
	BaseController

	inboundController   *InboundController
	settingController   *SettingController
	apiPanelController  *APIPanelController
	blockRuleController *BlockRuleController
}

func NewXUIController(g *gin.RouterGroup) *XUIController {
	a := &XUIController{}
	a.initRouter(g)
	return a
}

func (a *XUIController) initRouter(g *gin.RouterGroup) {
	g = g.Group("/xui")
	g.Use(a.checkLogin)

	g.GET("/", a.index)
	g.GET("/inbounds", a.inbounds)
	g.GET("/setting", a.setting)
	g.GET("/api-console", a.apiConsole)
	g.GET("/block-rules", a.blockRules)

	a.inboundController = NewInboundController(g)
	a.settingController = NewSettingController(g)
	a.apiPanelController = NewAPIPanelController(g)
	a.blockRuleController = NewBlockRuleController(g)
}

func (a *XUIController) index(c *gin.Context) {
	html(c, "index.html", "系统状态", nil)
}

func (a *XUIController) inbounds(c *gin.Context) {
	html(c, "inbounds.html", "入站列表", nil)
}

func (a *XUIController) setting(c *gin.Context) {
	html(c, "setting.html", "设置", nil)
}

func (a *XUIController) apiConsole(c *gin.Context) {
	html(c, "api_console.html", "API 控制台", nil)
}

func (a *XUIController) blockRules(c *gin.Context) {
	html(c, "block_rules.html", "屏蔽规则", nil)
}
