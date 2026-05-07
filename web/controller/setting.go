package controller

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"nexcore-x-ui/util/crypto"
	"nexcore-x-ui/web/entity"
	"nexcore-x-ui/web/service"
	"nexcore-x-ui/web/session"
)

type updateUserForm struct {
	OldUsername string `json:"oldUsername" form:"oldUsername"`
	OldPassword string `json:"oldPassword" form:"oldPassword"`
	NewUsername string `json:"newUsername" form:"newUsername"`
	NewPassword string `json:"newPassword" form:"newPassword"`
}

type SettingController struct {
	settingService service.SettingService
	userService    service.UserService
	panelService   service.PanelService
	magicService   service.MagicTokenService
}

func NewSettingController(g *gin.RouterGroup) *SettingController {
	a := &SettingController{}
	a.initRouter(g)
	return a
}

func (a *SettingController) initRouter(g *gin.RouterGroup) {
	g = g.Group("/setting")

	g.POST("/all", a.getAllSetting)
	g.POST("/update", a.updateSetting)
	g.POST("/updateUser", a.updateUser)
	g.POST("/restartPanel", a.restartPanel)
	g.POST("/magicLink", a.createMagicLink)
	g.POST("/testOnlineWebhook", a.testOnlineWebhook)
}

// testOnlineWebhook 立即触发一次推送(绕过 5s tick),用于设置面板上"测试推送"
// 按钮。结果回到调用方好显示给操作员是否真的推到了业务系统。
func (a *SettingController) testOnlineWebhook(c *gin.Context) {
	err := service.GetOnlineWebhookService().SendNow()
	jsonMsg(c, "测试推送", err)
}

func (a *SettingController) getAllSetting(c *gin.Context) {
	allSetting, err := a.settingService.GetAllSetting()
	if err != nil {
		jsonMsg(c, "获取设置", err)
		return
	}
	jsonObj(c, allSetting, nil)
}

func (a *SettingController) updateSetting(c *gin.Context) {
	allSetting := &entity.AllSetting{}
	err := c.ShouldBind(allSetting)
	if err != nil {
		jsonMsg(c, "修改设置", err)
		return
	}
	err = a.settingService.UpdateAllSetting(allSetting)
	jsonMsg(c, "修改设置", err)
}

func (a *SettingController) updateUser(c *gin.Context) {
	form := &updateUserForm{}
	err := c.ShouldBind(form)
	if err != nil {
		jsonMsg(c, "修改用户", err)
		return
	}
	user := session.GetLoginUser(c)
	ok, _ := crypto.VerifyPassword(user.Password, form.OldPassword)
	if user.Username != form.OldUsername || !ok {
		jsonMsg(c, "修改用户", errors.New("原用户名或原密码错误"))
		return
	}
	if form.NewUsername == "" || form.NewPassword == "" {
		jsonMsg(c, "修改用户", errors.New("新用户名和新密码不能为空"))
		return
	}
	err = a.userService.UpdateUser(user.Id, form.NewUsername, form.NewPassword)
	if err == nil {
		newHash, hashErr := crypto.HashPassword(form.NewPassword)
		if hashErr == nil {
			user.Username = form.NewUsername
			user.Password = newHash
			session.SetLoginUser(c, user)
		}
	}
	jsonMsg(c, "修改用户", err)
}

func (a *SettingController) restartPanel(c *gin.Context) {
	err := a.panelService.RestartPanel(time.Second * 3)
	jsonMsg(c, "重启面板", err)
}

// createMagicLink mints a panel-session-authenticated magic link. Body is
// optional: {"ttlSeconds": 600, "note": "..."}. Returns a fully-qualified
// URL based on the incoming Host header so the operator can copy + share.
func (a *SettingController) createMagicLink(c *gin.Context) {
	var body struct {
		TtlSeconds int    `json:"ttlSeconds" form:"ttlSeconds"`
		Note       string `json:"note"       form:"note"`
	}
	_ = c.ShouldBind(&body)

	created, err := a.magicService.CreateMagicToken(time.Duration(body.TtlSeconds)*time.Second, body.Note)
	if err != nil {
		jsonMsg(c, "生成快捷登录链接", err)
		return
	}

	scheme := "http"
	if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	host := c.Request.Host
	basePath := c.GetString("base_path")
	if basePath == "" {
		basePath = "/"
	}
	// Plaintext is returned exactly once; the DB row carries the SHA256.
	// Token goes in the URL fragment so it never reaches server access
	// logs, proxies, or Referer headers — see web.go::handleMagicConsume
	// for the full rationale.
	url := scheme + "://" + host + basePath + "magic-login#tk=" + created.Plaintext

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"obj": gin.H{
			"url":        url,
			"token":      created.Plaintext,
			"expiresAt":  created.Row.ExpiresAt,
			"ttlSeconds": created.Row.ExpiresAt - created.Row.CreatedAt,
		},
	})
}
