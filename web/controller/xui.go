package controller

import (
	"github.com/gin-gonic/gin"
)

// XUIController 把所有 panel 业务 API(/xui/inbound/*, /xui/setting/*,
// /xui/api/*, /xui/block-rule/*) 挂到一个 session-auth group 下。
// v2.0:不再渲染任何 HTML 模板,UI 整体由 Vue 3 SPA 接管,在 web.go
// 顶层挂 SPA 入口 + StaticFS。
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

	a.inboundController = NewInboundController(g)
	a.settingController = NewSettingController(g)
	a.apiPanelController = NewAPIPanelController(g)
	a.blockRuleController = NewBlockRuleController(g)
}
