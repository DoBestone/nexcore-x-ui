package controller

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"nexcore-x-ui/config"
	"nexcore-x-ui/logger"
	"nexcore-x-ui/web/entity"
)

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

func html(c *gin.Context, name string, title string, data gin.H) {
	if data == nil {
		data = gin.H{}
	}
	data["title"] = title
	data["request_uri"] = c.Request.RequestURI
	data["base_path"] = c.GetString("base_path")
	c.HTML(http.StatusOK, name, getContext(data))
}

func getContext(h gin.H) gin.H {
	a := gin.H{
		"cur_ver": config.GetVersion(),
	}
	if h != nil {
		for key, value := range h {
			a[key] = value
		}
	}
	return a
}

func isAjax(c *gin.Context) bool {
	return c.GetHeader("X-Requested-With") == "XMLHttpRequest"
}
