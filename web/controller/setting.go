package controller

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"x-ui/util/crypto"
	"x-ui/web/entity"
	"x-ui/web/service"
	"x-ui/web/session"
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

	t, err := a.magicService.CreateMagicToken(time.Duration(body.TtlSeconds)*time.Second, body.Note)
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
	url := scheme + "://" + host + basePath + "panel-login/" + t.Token

	c.JSON(http.StatusOK, gin.H{
		"success":   true,
		"obj": gin.H{
			"url":       url,
			"token":     t.Token,
			"expiresAt": t.ExpiresAt,
			"ttlSeconds": t.ExpiresAt - t.CreatedAt,
		},
	})
}
