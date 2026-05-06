package model

import (
	"fmt"
	"nexcore-x-ui/util/json_util"
	"nexcore-x-ui/xray"
)

type Protocol string

const (
	VMess       Protocol = "vmess"
	VLESS       Protocol = "vless"
	Dokodemo    Protocol = "Dokodemo-door"
	Http        Protocol = "http"
	Trojan      Protocol = "trojan"
	Shadowsocks Protocol = "shadowsocks"
	Socks       Protocol = "socks"
	Wireguard   Protocol = "wireguard"
)

type User struct {
	Id       int    `json:"id" gorm:"primaryKey;autoIncrement"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type Inbound struct {
	Id         int    `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`
	UserId     int    `json:"-"`
	Up         int64  `json:"up" form:"up"`
	Down       int64  `json:"down" form:"down"`
	Total      int64  `json:"total" form:"total"`
	Remark     string `json:"remark" form:"remark"`
	Enable     bool   `json:"enable" form:"enable"`
	ExpiryTime int64  `json:"expiryTime" form:"expiryTime"`

	// config part
	Listen         string   `json:"listen" form:"listen"`
	Port           int      `json:"port" form:"port" gorm:"unique"`
	Protocol       Protocol `json:"protocol" form:"protocol"`
	Settings       string   `json:"settings" form:"settings"`
	StreamSettings string   `json:"streamSettings" form:"streamSettings"`
	Tag            string   `json:"tag" form:"tag" gorm:"unique"`
	Sniffing       string   `json:"sniffing" form:"sniffing"`
}

func (i *Inbound) GenXrayInboundConfig() *xray.InboundConfig {
	listen := i.Listen
	if listen != "" {
		listen = fmt.Sprintf("\"%v\"", listen)
	}
	return &xray.InboundConfig{
		Listen:         json_util.RawMessage(listen),
		Port:           i.Port,
		Protocol:       string(i.Protocol),
		Settings:       json_util.RawMessage(i.Settings),
		StreamSettings: json_util.RawMessage(i.StreamSettings),
		Tag:            i.Tag,
		Sniffing:       json_util.RawMessage(i.Sniffing),
	}
}

type Setting struct {
	Id    int    `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`
	Key   string `json:"key" form:"key"`
	Value string `json:"value" form:"value"`
}

type APIToken struct {
	Id         int    `json:"id" gorm:"primaryKey;autoIncrement"`
	Name       string `json:"name" gorm:"uniqueIndex"`
	Token      string `json:"-"            gorm:"uniqueIndex"`
	CreatedAt  int64  `json:"createdAt"`
	LastUsedAt int64  `json:"lastUsedAt"`
	Revoked    bool   `json:"revoked"`
	// Scope gates which endpoints this token may hit. Values:
	//   "admin"        — full access (default for legacy / migrated rows)
	//   "readonly"     — GET-only: status, inbounds list, traffic, share
	//   "subscription" — share / subscription / health only
	// Anything else is treated as "no access" by the middleware.
	Scope string `json:"scope" gorm:"default:admin"`
	// ExpiresAt is unix seconds. 0 means "never expires" — the original
	// behavior, preserved for tokens that pre-date the column. Any
	// non-zero value < time.Now() is treated as expired by the auth
	// middleware (same code path as Revoked).
	ExpiresAt int64 `json:"expiresAt"`
}

// MagicToken backs the one-click panel-login feature. Each row is single-use
// and time-bounded; consumed rows are kept briefly for audit then GC'd.
type MagicToken struct {
	Id         int    `json:"id" gorm:"primaryKey;autoIncrement"`
	Token      string `json:"-"            gorm:"uniqueIndex"`
	CreatedAt  int64  `json:"createdAt"`
	ExpiresAt  int64  `json:"expiresAt"`
	ConsumedAt int64  `json:"consumedAt"`
	Note       string `json:"note"` // free-form, e.g. "remote support 2026-05-06"
}

// APILog records one row per /api/v1/* call so operators can audit who hit
// what. Bodies are NOT stored — only request meta + response status. Old
// rows are auto-purged by a cron task.
type APILog struct {
	Id         int    `json:"id" gorm:"primaryKey;autoIncrement"`
	At         int64  `json:"at" gorm:"index"`
	Method     string `json:"method"`
	Path       string `json:"path" gorm:"index"`
	Status     int    `json:"status"`
	DurationMs int64  `json:"durationMs"`
	IP         string `json:"ip"`
	TokenName  string `json:"tokenName"` // "" if anonymous (e.g. /health)
}

// BlockRule 描述一条"屏蔽规则"，最终注入到 Xray routing.rules 数组的最前面，
// 把命中的流量路由到 outbound `blocked`(黑洞)。多条规则按 (InboundTag, Type)
// 分组合并以减少 Xray 规则数。
//
// 设计上 BlockRule 与 Inbound 解耦:删除 Inbound 时不级联删除规则,因为规则常常
// 跨 Inbound 复用(尤其全局规则)。
//
// form tag 必须有:面板 axios 默认用 application/x-www-form-urlencoded
// 提交,gin 的 ShouldBind 走 form binding 时用 form tag 匹配字段。
type BlockRule struct {
	Id         int    `json:"id"         form:"id"         gorm:"primaryKey;autoIncrement"`
	Type       string `json:"type"       form:"type"`       // domain | ip | geosite | geoip | port | protocol | source
	Value      string `json:"value"      form:"value"`      // 单个值或逗号分隔的多个值
	Remark     string `json:"remark"     form:"remark"`
	InboundTag string `json:"inboundTag" form:"inboundTag"` // 留空 = 全局; 填了 = 仅对该入站生效
	Enable     bool   `json:"enable"     form:"enable"`
	CreatedAt  int64  `json:"createdAt"  form:"createdAt"`
}
