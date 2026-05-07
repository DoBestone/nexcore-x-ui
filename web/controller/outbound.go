package controller

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"nexcore-x-ui/database/model"
	"nexcore-x-ui/web/service"
)

// OutboundController 是面板("/xui/outbound/*")CRUD 入口。镜像 BlockRuleController
// 风格:list / add / update / del / toggle,session-auth(挂在 /xui group 下)。
// 出站修改后必置 needRestart — XrayService.GetXrayConfig 把出站 + 路由
// 注入到最终 xray 配置,只有重启后才生效。
type OutboundController struct {
	outboundService service.OutboundService
	xrayService     service.XrayService
}

func NewOutboundController(g *gin.RouterGroup) *OutboundController {
	a := &OutboundController{}
	a.initRouter(g)
	return a
}

func (a *OutboundController) initRouter(g *gin.RouterGroup) {
	g = g.Group("/outbound")

	g.POST("/list", a.list)
	g.POST("/add", a.add)
	g.POST("/update/:id", a.update)
	g.POST("/del/:id", a.del)
	g.POST("/toggle/:id", a.toggle)
	g.POST("/test/:id", a.test)
}

func (a *OutboundController) list(c *gin.Context) {
	rows, err := a.outboundService.List()
	if err != nil {
		jsonMsg(c, "获取", err)
		return
	}
	jsonObj(c, rows, nil)
}

func (a *OutboundController) add(c *gin.Context) {
	r := &model.Outbound{}
	if err := c.ShouldBind(r); err != nil {
		jsonMsg(c, "添加", err)
		return
	}
	err := a.outboundService.Add(r)
	jsonMsg(c, "添加", err)
	if err == nil {
		a.xrayService.SetToNeedRestart()
	}
}

func (a *OutboundController) update(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "修改", err)
		return
	}
	r := &model.Outbound{Id: id}
	if err := c.ShouldBind(r); err != nil {
		jsonMsg(c, "修改", err)
		return
	}
	err = a.outboundService.Update(r)
	jsonMsg(c, "修改", err)
	if err == nil {
		a.xrayService.SetToNeedRestart()
	}
}

func (a *OutboundController) del(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "删除", err)
		return
	}
	err = a.outboundService.Delete(id)
	jsonMsg(c, "删除", err)
	if err == nil {
		a.xrayService.SetToNeedRestart()
	}
}

// test — 出站连通测试。轻量 TCP dial + 可选 TLS 握手,不动 xray 进程,
// 不影响线上流量。前端按 OutboundTestResult.message 直接展示,reachable
// + tlsOk(如果有 TLS)双绿才算通。失败把 message 当 toast 给用户排错。
func (a *OutboundController) test(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "测试", err)
		return
	}
	r, err := a.outboundService.TestConnectivity(id)
	if err != nil {
		jsonMsg(c, "测试", err)
		return
	}
	jsonObj(c, r, nil)
}

func (a *OutboundController) toggle(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "切换", err)
		return
	}
	var body struct {
		Enable bool `json:"enable" form:"enable"`
	}
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, "切换", err)
		return
	}
	err = a.outboundService.SetEnable(id, body.Enable)
	jsonMsg(c, "切换", err)
	if err == nil {
		a.xrayService.SetToNeedRestart()
	}
}
