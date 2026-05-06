package controller

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"nexcore-x-ui/database/model"
	"nexcore-x-ui/web/service"
)

// BlockRuleController 是面板("/xui/block-rule/*")CRUD 入口。
// 业务 API("/api/v1/block-rules")在 web/controller/api/v1.go 复用同一套 service。
type BlockRuleController struct {
	blockRuleService service.BlockRuleService
	xrayService      service.XrayService
}

func NewBlockRuleController(g *gin.RouterGroup) *BlockRuleController {
	a := &BlockRuleController{}
	a.initRouter(g)
	return a
}

func (a *BlockRuleController) initRouter(g *gin.RouterGroup) {
	g = g.Group("/block-rule")

	g.POST("/list", a.list)
	g.POST("/add", a.add)
	g.POST("/update/:id", a.update)
	g.POST("/del/:id", a.del)
	g.POST("/toggle/:id", a.toggle)
	g.POST("/presets", a.presets)
	g.POST("/apply-preset", a.applyPreset)
}

func (a *BlockRuleController) list(c *gin.Context) {
	rows, err := a.blockRuleService.List()
	if err != nil {
		jsonMsg(c, "获取", err)
		return
	}
	jsonObj(c, rows, nil)
}

func (a *BlockRuleController) add(c *gin.Context) {
	r := &model.BlockRule{}
	if err := c.ShouldBind(r); err != nil {
		jsonMsg(c, "添加", err)
		return
	}
	err := a.blockRuleService.Add(r)
	jsonMsg(c, "添加", err)
	if err == nil {
		a.xrayService.SetToNeedRestart()
	}
}

func (a *BlockRuleController) update(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "修改", err)
		return
	}
	r := &model.BlockRule{Id: id}
	if err := c.ShouldBind(r); err != nil {
		jsonMsg(c, "修改", err)
		return
	}
	err = a.blockRuleService.Update(r)
	jsonMsg(c, "修改", err)
	if err == nil {
		a.xrayService.SetToNeedRestart()
	}
}

func (a *BlockRuleController) del(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "删除", err)
		return
	}
	err = a.blockRuleService.Delete(id)
	jsonMsg(c, "删除", err)
	if err == nil {
		a.xrayService.SetToNeedRestart()
	}
}

func (a *BlockRuleController) toggle(c *gin.Context) {
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
	err = a.blockRuleService.SetEnable(id, body.Enable)
	jsonMsg(c, "切换", err)
	if err == nil {
		a.xrayService.SetToNeedRestart()
	}
}

func (a *BlockRuleController) presets(c *gin.Context) {
	jsonObj(c, a.blockRuleService.ListPresets(), nil)
}

func (a *BlockRuleController) applyPreset(c *gin.Context) {
	var body struct {
		Key        string `json:"key"        form:"key"`
		InboundTag string `json:"inboundTag" form:"inboundTag"`
	}
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, "应用预置", err)
		return
	}
	err := a.blockRuleService.ApplyPreset(body.Key, body.InboundTag)
	jsonMsg(c, "应用预置", err)
	if err == nil {
		a.xrayService.SetToNeedRestart()
	}
}
