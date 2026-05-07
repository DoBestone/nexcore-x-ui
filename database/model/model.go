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

	// OutboundTag 关联到 Outbound.Tag(空 = 直连,使用 freedom 出站)。
	// 设了的话,XrayService.GetXrayConfig 会注入一条 routing 规则
	// {inboundTag:[this.Tag], outboundTag:OutboundTag, type:"field"},
	// 把这条入站的流量定向到指定出站做中转。share link 的 ps 字段也会
	// 用对应 Outbound.Name 替代节点名称作为前缀。
	OutboundTag string `json:"outboundTag" form:"outboundTag"`
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

// ClientTraffic — v1.1.0 引入的 per-client 维度行,把"用户/客户"提升到
// 一等公民。每个 inbound.settings.clients[] 里每条 client 在这张表里有
// 一行,xray gRPC stats 按 email 聚合的流量也写到这里,而不再只在 inbound
// 级累加。这是支持订阅运营(单 inbound 多用户独立计费/到期)的基础设施。
//
// email 在 xray-core 设计里就是全局唯一(stats key 是
// "user>>>email>>>traffic>>>uplink/downlink"),所以这里 email 列加 unique
// 索引;同入站内重名会被 xray 自己拒掉,跨入站重名没意义 stats 会撞车。
//
// total / expiryTime 0 = 不限制 / 不过期,跟 Inbound 级语义一致。
// enable=false 由 AddTraffic 顺手 check 自动写(到期/触顶),也允许操作员
// 通过 API 手动置回 true(配合 ResetTraffic)。
type ClientTraffic struct {
	Id         int    `json:"id"         gorm:"primaryKey;autoIncrement"`
	InboundId  int    `json:"inboundId"  gorm:"index;not null"`
	Email      string `json:"email"      gorm:"uniqueIndex;not null"`
	Up         int64  `json:"up"`
	Down       int64  `json:"down"`
	Total      int64  `json:"total"`      // 流量上限(字节),0=不限
	ExpiryTime int64  `json:"expiryTime"` // unix 毫秒,0=永不过期
	Enable     bool   `json:"enable"      gorm:"default:true"`
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

// Outbound 描述一个用户配置的出站服务器(中转/代理)。inbound 通过
// OutboundTag 字段关联,XrayService.GetXrayConfig 把它转成 xray 的
// outbounds[] + 一条 routing 规则,实现"前置入站 → 后置出站"链路。
//
// 与 BlockRule 类似:与 Inbound 解耦,删除入站不级联;Tag 全局唯一,
// 修改时不允许变更(已有 Inbound.OutboundTag 引用)。Settings 与
// StreamSettings 是 raw JSON,语义完全跟 xray outbound 配置一致;
// 由 OutboundService 序列化成 xray.OutboundConfig。
type Outbound struct {
	Id             int    `json:"id"             form:"id"             gorm:"primaryKey;autoIncrement"`
	Tag            string `json:"tag"            form:"tag"            gorm:"uniqueIndex"`
	Name           string `json:"name"           form:"name"`           // 用户可读名,用作 share link 前缀
	Protocol       string `json:"protocol"       form:"protocol"`       // vless / vmess / trojan / shadowsocks
	Address        string `json:"address"        form:"address"`        // 可读字段,settings 里也有,这里冗余以便 list UI 一眼看清
	Port           int    `json:"port"           form:"port"`
	Settings       string `json:"settings"       form:"settings"`       // raw JSON,xray outbound.settings
	StreamSettings string `json:"streamSettings" form:"streamSettings"` // raw JSON,xray outbound.streamSettings
	Remark         string `json:"remark"         form:"remark"`
	Enable         bool   `json:"enable"         form:"enable"`
	CreatedAt      int64  `json:"createdAt"      form:"createdAt"`
}
