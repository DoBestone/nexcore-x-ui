package controller

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"nexcore-x-ui/logger"
	"nexcore-x-ui/web/entity"
)

// isAjax reports whether the request looks like an XHR/fetch from the
// SPA. Used by base.checkLogin to decide between 401 JSON and a redirect.
func isAjax(c *gin.Context) bool {
	return c.GetHeader("X-Requested-With") == "XMLHttpRequest"
}

// fmtSscanf is a tiny helper to parse single-int query params without
// importing strconv in every controller file.
func fmtSscanf(s string, dst *int) (int, error) {
	return fmt.Sscanf(s, "%d", dst)
}

func getUriId(c *gin.Context) int64 {
	s := struct {
		Id int64 `uri:"id"`
	}{}

	_ = c.BindUri(&s)
	return s.Id
}

// getRemoteIp returns the client IP. It only honors X-Forwarded-For when the
// immediate peer is a loopback address — otherwise the header is attacker-
// controlled and must be ignored. If you front the panel with a real reverse
// proxy on a different host, configure gin's TrustedProxies via web.go instead.
func getRemoteIp(c *gin.Context) string {
	addr := c.Request.RemoteAddr
	peerIP, _, _ := net.SplitHostPort(addr)

	if ip := net.ParseIP(peerIP); ip != nil && ip.IsLoopback() {
		if value := c.GetHeader("X-Forwarded-For"); value != "" {
			ips := strings.Split(value, ",")
			return strings.TrimSpace(ips[0])
		}
		if value := c.GetHeader("X-Real-IP"); value != "" {
			return strings.TrimSpace(value)
		}
	}
	return peerIP
}

func jsonMsg(c *gin.Context, msg string, err error) {
	jsonMsgObj(c, msg, nil, err)
}

func jsonObj(c *gin.Context, obj interface{}, err error) {
	jsonMsgObj(c, "", obj, err)
}

func jsonMsgObj(c *gin.Context, msg string, obj interface{}, err error) {
	m := entity.Msg{
		Obj: obj,
	}
	if err == nil {
		m.Success = true
		if msg != "" {
			m.Msg = msg + "成功"
		}
	} else {
		m.Success = false
		m.Msg = msg + "失败: " + err.Error()
		logger.Warning(msg+"失败: ", err)
	}
	c.JSON(http.StatusOK, m)
}

func pureJsonMsg(c *gin.Context, success bool, msg string) {
	if success {
		c.JSON(http.StatusOK, entity.Msg{
			Success: true,
			Msg:     msg,
		})
	} else {
		c.JSON(http.StatusOK, entity.Msg{
			Success: false,
			Msg:     msg,
		})
	}
}

