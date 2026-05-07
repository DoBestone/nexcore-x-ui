package entity

import (
	"crypto/tls"
	"encoding/json"
	"net"
	"strings"
	"time"
	"nexcore-x-ui/util/common"
	"nexcore-x-ui/xray"
)

type Msg struct {
	Success bool        `json:"success"`
	Msg     string      `json:"msg"`
	Obj     interface{} `json:"obj"`
}

type Pager struct {
	Current  int         `json:"current"`
	PageSize int         `json:"page_size"`
	Total    int         `json:"total"`
	OrderBy  string      `json:"order_by"`
	Desc     bool        `json:"desc"`
	Key      string      `json:"key"`
	List     interface{} `json:"list"`
}

type AllSetting struct {
	WebListen          string `json:"webListen" form:"webListen"`
	WebPort            int    `json:"webPort" form:"webPort"`
	WebCertFile        string `json:"webCertFile" form:"webCertFile"`
	WebKeyFile         string `json:"webKeyFile" form:"webKeyFile"`
	WebBasePath        string `json:"webBasePath" form:"webBasePath"`
	TgBotEnable        bool   `json:"tgBotEnable" form:"tgBotEnable"`
	TgBotToken         string `json:"tgBotToken" form:"tgBotToken"`
	TgBotChatId        int    `json:"tgBotChatId" form:"tgBotChatId"`
	TgRunTime          string `json:"tgRunTime" form:"tgRunTime"`
	XrayTemplateConfig string `json:"xrayTemplateConfig" form:"xrayTemplateConfig"`

	TimeLocation string `json:"timeLocation" form:"timeLocation"`

	// 在线 IP webhook 推送 — 上游业务系统跨节点聚合用。Url 空 = 禁用。
	OnlineWebhookUrl    string `json:"onlineWebhookUrl" form:"onlineWebhookUrl"`
	OnlineWebhookSecret string `json:"onlineWebhookSecret" form:"onlineWebhookSecret"`
	OnlineWebhookNodeId string `json:"onlineWebhookNodeId" form:"onlineWebhookNodeId"`

	// 节点名称(e.g. "香港节点1") — 注入到 share link 的 ps/remarks 字段,
	// 客户端导入订阅时一眼能看出这一条出自哪个节点。空 = 不加前缀(旧行为)。
	NodeName string `json:"nodeName" form:"nodeName"`

	// 安全入口:启用后面板需要带上 secureEntryPath 才能访问,扫端口
	// 看到的是裸 404。default off,启用要二次确认 + 提示新 URL。
	SecureEntryEnabled bool   `json:"secureEntryEnabled" form:"secureEntryEnabled"`
	SecureEntryPath    string `json:"secureEntryPath" form:"secureEntryPath"`

	// 节点地址:分享链接里写的 host。空 = 用浏览器访问面板的域名/IP。
	// CF 橙云代理 + 域名访问面板场景必须显式配 — 否则链接 host 落到
	// CF 代理域,客户端打 xray 跑的非标端口直接超时。
	NodeAddress string `json:"nodeAddress" form:"nodeAddress"`

	// CF API token(Zone:DNS:Edit)。前端展示用 password input,提交时空
	// 字符串视作"不变"(避免回读时把存好的 token 误清)。复用于 DNS-01
	// 取证书 + 一键切换橙云/灰云。
	CfApiToken string `json:"cfApiToken" form:"cfApiToken"`
}

func (s *AllSetting) CheckValid() error {
	if s.WebListen != "" {
		ip := net.ParseIP(s.WebListen)
		if ip == nil {
			return common.NewError("web listen is not valid ip:", s.WebListen)
		}
	}

	if s.WebPort <= 0 || s.WebPort > 65535 {
		return common.NewError("web port is not a valid port:", s.WebPort)
	}

	if s.WebCertFile != "" || s.WebKeyFile != "" {
		_, err := tls.LoadX509KeyPair(s.WebCertFile, s.WebKeyFile)
		if err != nil {
			return common.NewErrorf("cert file <%v> or key file <%v> invalid: %v", s.WebCertFile, s.WebKeyFile, err)
		}
	}

	if !strings.HasPrefix(s.WebBasePath, "/") {
		s.WebBasePath = "/" + s.WebBasePath
	}
	if !strings.HasSuffix(s.WebBasePath, "/") {
		s.WebBasePath += "/"
	}

	xrayConfig := &xray.Config{}
	err := json.Unmarshal([]byte(s.XrayTemplateConfig), xrayConfig)
	if err != nil {
		return common.NewError("xray template config invalid:", err)
	}

	_, err = time.LoadLocation(s.TimeLocation)
	if err != nil {
		return common.NewError("time location not exist:", s.TimeLocation)
	}

	// 安全入口校验:启用时 path 必须非空且只包含 letters/digits/_/-,
	// 单段无斜杠 — 避免用户写出 "/foo/bar" 这种导致路由拼接异常。
	// 长度 4-64,太短没意义(秒级穷举),太长复制易漏字符。
	if s.SecureEntryEnabled {
		p := strings.TrimSpace(s.SecureEntryPath)
		if p == "" {
			return common.NewError("启用安全入口必须提供 secureEntryPath(建议随机字母数字串)")
		}
		if len(p) < 4 || len(p) > 64 {
			return common.NewError("secureEntryPath 长度需在 4-64 之间,当前长度:", len(p))
		}
		for _, r := range p {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r == '_' || r == '-') {
				return common.NewError("secureEntryPath 只允许字母/数字/_/-,出现非法字符:", string(r))
			}
		}
		s.SecureEntryPath = p
	}

	return nil
}
