package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"nexcore-x-ui/web/service"
	"nexcore-x-ui/web/session"
)

type BaseController struct {
	userService service.UserService
}

// checkLogin gates every authenticated panel route. The cookie carries
// only (uid, sig); LoadCurrentUser pulls the live row from SQLite and
// verifies the password fingerprint still matches. If the admin rotated
// credentials (or `nexcore-x-ui reset` nuked the row), every outstanding
// cookie's sig diverges and the next request fails — leaked cookies
// can't outlive a password rotation.
//
// The DB hit is one indexed PK lookup; sqlite serves it in microseconds
// and we cache the resulting *User on gin.Context so downstream handlers
// (inbound.go, setting.go, api_panel.go) get it for free.
func (a *BaseController) checkLogin(c *gin.Context) {
	user := session.LoadCurrentUser(c, a.userService.GetFirstUser)
	if user == nil {
		session.ClearSession(c)
		a.failLogin(c)
		return
	}
	c.Next()
}

func (a *BaseController) failLogin(c *gin.Context) {
	// SPA / 任何 fetch 走 AJAX 分支:返回 401 + JSON body,前端 axios
	// 拦截器据此跳 /login。浏览器直接刷面板路径(/dashboard 之类)走
	// 重定向分支:把用户送回 /,SPA 入口接管后再跳 /login。
	if isAjax(c) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"msg":     "登录时效已过,请重新登录",
		})
		return
	}
	c.Redirect(http.StatusTemporaryRedirect, c.GetString("base_path"))
	c.Abort()
}
