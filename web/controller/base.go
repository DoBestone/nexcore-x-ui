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

// checkLogin gates every authenticated panel route. Beyond the basic
// "is there a session?" question, it verifies the session's snapshot of
// User.Password still matches what's in the DB. If they diverge — the
// admin changed credentials, or the row was nuked by `nexcore-x-ui
// reset` — every existing cookie loses access on its next request,
// which is what we want: a leaked cookie can't outlive a password
// rotation.
//
// The DB hit is one indexed PK lookup; sqlite serves it in microseconds
// and the result isn't cached because we explicitly want every request
// to see the freshest credential snapshot.
func (a *BaseController) checkLogin(c *gin.Context) {
	user := session.GetLoginUser(c)
	if user == nil {
		a.failLogin(c)
		return
	}
	current, err := a.userService.GetFirstUser()
	if err != nil || current == nil || current.Id != user.Id || current.Password != user.Password {
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
