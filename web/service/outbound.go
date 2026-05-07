package service

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"nexcore-x-ui/database"
	"nexcore-x-ui/database/model"
	"nexcore-x-ui/util/common"
)

// 用户能配置的出站协议。SS 写成 "shadowsocks" 跟 inbound 一致,xray 接的也是
// 这个名字。Freedom / blackhole / dns 这些"内置"出站不让用户从 UI 加,模板
// 已经有了,加了也用不上 OutboundTag 路由(用户不会想把入站定向到 freedom
// 之外的 freedom)。
var validOutboundProtocols = map[string]bool{
	"vless":       true,
	"vmess":       true,
	"trojan":      true,
	"shadowsocks": true,
}

// Tag 是 xray routing.outboundTag 的引用键,不允许出现影响 JSON / 路由匹配
// 的字符。与 inbound tag 风格一致(letters / digits / dash / underscore /
// dot),保证编辑入站时下拉框可读。
var outboundTagRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

var (
	ErrOutboundTagInvalid    = errors.New("outbound tag must match [A-Za-z0-9_.-]{1,64}")
	ErrOutboundTagDuplicate  = errors.New("outbound tag already exists")
	ErrOutboundTagReserved   = errors.New("outbound tag is reserved (api / direct / blocked / dns-out)")
	ErrOutboundProtocolUnsup = errors.New("unsupported outbound protocol")
	ErrOutboundNotFound      = errors.New("outbound not found")
	ErrOutboundInvalidJSON   = errors.New("settings or streamSettings is not valid JSON")
)

// 模板里 reserved 的 tag,加用户出站不能撞,否则 routing 规则歧义。
var reservedOutboundTags = map[string]bool{
	"api":     true,
	"direct":  true,
	"blocked": true,
	"dns-out": true,
}

type OutboundService struct{}

func (s *OutboundService) List() ([]*model.Outbound, error) {
	db := database.GetDB()
	var rows []*model.Outbound
	if err := db.Order("created_at desc").Find(&rows).Error; err != nil &&
		!errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	return rows, nil
}

func (s *OutboundService) Get(id int) (*model.Outbound, error) {
	db := database.GetDB()
	var r model.Outbound
	if err := db.First(&r, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrOutboundNotFound
		}
		return nil, err
	}
	return &r, nil
}

// GetByTag 按 tag 取出站,xray config 构建 + share link 前缀都用这个 lookup
// 路径。tag 唯一,所以一条命中即可。
func (s *OutboundService) GetByTag(tag string) (*model.Outbound, error) {
	if tag == "" {
		return nil, ErrOutboundNotFound
	}
	db := database.GetDB()
	var r model.Outbound
	if err := db.Where("tag = ?", tag).First(&r).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrOutboundNotFound
		}
		return nil, err
	}
	return &r, nil
}

func (s *OutboundService) Add(r *model.Outbound) error {
	if err := s.validate(r); err != nil {
		return err
	}
	// tag 重复检查 — 唯一索引也会拦,但提前查一次给可读错误。
	var n int64
	if err := database.GetDB().Model(&model.Outbound{}).
		Where("tag = ?", r.Tag).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return ErrOutboundTagDuplicate
	}
	r.Id = 0
	r.CreatedAt = time.Now().UnixMilli()
	return database.GetDB().Create(r).Error
}

func (s *OutboundService) Update(r *model.Outbound) error {
	if err := s.validate(r); err != nil {
		return err
	}
	existing, err := s.Get(r.Id)
	if err != nil {
		return err
	}
	// Tag 不允许改 — 已有 Inbound.OutboundTag 引用,改了会一次性失联所有
	// 绑定的 inbound。前端把 tag 字段在 edit 模式下置 disabled,这里再兜底。
	if existing.Tag != r.Tag {
		return common.NewError("出站 tag 不允许修改;请删除后重建,或先把引用此 tag 的入站解绑")
	}
	r.CreatedAt = existing.CreatedAt
	return database.GetDB().Save(r).Error
}

func (s *OutboundService) SetEnable(id int, enable bool) error {
	r, err := s.Get(id)
	if err != nil {
		return err
	}
	r.Enable = enable
	return database.GetDB().Save(r).Error
}

func (s *OutboundService) Delete(id int) error {
	r, err := s.Get(id)
	if err != nil {
		return err
	}
	// 解绑所有引用此 tag 的 inbound — 否则 xray 重启时 routing 规则指向
	// 不存在的 outboundTag,xray 启动失败。把它们退回直连(空 OutboundTag)。
	db := database.GetDB()
	if err := db.Model(&model.Inbound{}).
		Where("outbound_tag = ?", r.Tag).
		Update("outbound_tag", "").Error; err != nil {
		return err
	}
	return db.Delete(&model.Outbound{}, id).Error
}

func (s *OutboundService) validate(r *model.Outbound) error {
	r.Tag = strings.TrimSpace(r.Tag)
	r.Name = strings.TrimSpace(r.Name)
	r.Protocol = strings.TrimSpace(r.Protocol)
	r.Address = strings.TrimSpace(r.Address)
	if !outboundTagRe.MatchString(r.Tag) {
		return ErrOutboundTagInvalid
	}
	if reservedOutboundTags[r.Tag] {
		return ErrOutboundTagReserved
	}
	if r.Name == "" {
		return common.NewError("出站名称不能为空")
	}
	if !validOutboundProtocols[r.Protocol] {
		return ErrOutboundProtocolUnsup
	}
	if r.Address == "" {
		return common.NewError("地址不能为空")
	}
	if r.Port <= 0 || r.Port > 65535 {
		return common.NewError("端口越界")
	}
	// settings / streamSettings 至少要是合法 JSON,具体字段交给 xray 自己校验
	// (xray dry-run 在 GetXrayConfig 里面会跑)。
	for _, raw := range []string{r.Settings, r.StreamSettings} {
		if raw == "" {
			continue
		}
		var probe any
		if err := json.Unmarshal([]byte(raw), &probe); err != nil {
			return ErrOutboundInvalidJSON
		}
	}
	return nil
}

// GenXrayOutboundConfigs 把启用的出站全部转成 xray outbound JSON 对象,
// 调用方负责把它们 append 到模板的 outbounds 数组里。返回 []map 方便和
// 模板里 raw JSON 出站对象做合并(模板里 outbounds 是 RawMessage)。
func (s *OutboundService) GenXrayOutboundConfigs() ([]map[string]interface{}, error) {
	rows, err := s.List()
	if err != nil {
		return nil, err
	}
	out := make([]map[string]interface{}, 0, len(rows))
	for _, r := range rows {
		if !r.Enable {
			continue
		}
		obj := map[string]interface{}{
			"tag":      r.Tag,
			"protocol": r.Protocol,
		}
		if r.Settings != "" {
			var settings any
			if err := json.Unmarshal([]byte(r.Settings), &settings); err != nil {
				// validate() 已经保证合法 JSON,这里只是防御。失败就跳过这条
				// 出站,避免拖垮整个 xray reload。
				continue
			}
			obj["settings"] = settings
		} else {
			obj["settings"] = map[string]any{}
		}
		if r.StreamSettings != "" {
			var stream any
			if err := json.Unmarshal([]byte(r.StreamSettings), &stream); err != nil {
				continue
			}
			obj["streamSettings"] = stream
		}
		out = append(out, obj)
	}
	return out, nil
}

// OutboundTestResult 是 TestConnectivity 返回值。前端按这个结构展示。
//   - Reachable:TCP 是否能 dial 通(端口拨号 5s 超时)
//   - LatencyMs:TCP 拨号到 connection 建立的耗时(对网络状况的粗估)
//   - TLS 字段:streamSettings.security == "tls" 时填,握手成功 → TLSOK;
//     失败把 err 写 TLSError。Reality 协议不做握手(伪装 TLS,真证书不可见),
//     只看 TCP。
//   - Message:给 UI 一段人话,把 reachable/latency/tls 结合起来。
type OutboundTestResult struct {
	Reachable bool   `json:"reachable"`
	LatencyMs int64  `json:"latencyMs"`
	TLSOK     bool   `json:"tlsOk"`
	TLSError  string `json:"tlsError,omitempty"`
	Message   string `json:"message"`
}

// TestConnectivity 对出站做一次轻量连通测试。不发起完整代理握手 — 那需要
// 起临时 xray 子进程并塞个 dummy outbound,代价太高;TCP dial + 可选 TLS
// 握手已经能告诉用户 90% 的"上游能不能通":
//   - DNS 解析失败 → 地址写错 / 域名挂了
//   - TCP timeout → 防火墙拦了 / 上游下线 / 端口写错
//   - TCP OK / TLS 失败 → 上游证书不匹配 / SNI 写错 / 上游不是预期的服务
//   - 全 OK → 至少链路打得通,代理协议失败再去查密钥/UUID
//
// 5 秒拨号 + 5 秒握手超时 — 给慢出口余地但不会卡死面板线程。
func (s *OutboundService) TestConnectivity(id int) (*OutboundTestResult, error) {
	o, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	res := &OutboundTestResult{}

	addr := net.JoinHostPort(o.Address, strconv.Itoa(o.Port))
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		res.Message = fmt.Sprintf("TCP 连接失败:%s", err.Error())
		return res, nil
	}
	res.Reachable = true
	res.LatencyMs = time.Since(start).Milliseconds()

	// 解析 streamSettings 拿 security / SNI;失败按 plain 处理。
	var stream struct {
		Security    string `json:"security"`
		TLSSettings struct {
			ServerName string `json:"serverName"`
		} `json:"tlsSettings"`
	}
	if o.StreamSettings != "" {
		_ = json.Unmarshal([]byte(o.StreamSettings), &stream)
	}

	switch stream.Security {
	case "tls":
		sni := stream.TLSSettings.ServerName
		if sni == "" {
			sni = o.Address
		}
		// InsecureSkipVerify=false:用户配的就是要去校验上游证书的链路,
		// 这里要按真实情况报错(自签 / hostname mismatch),否则"看着通了
		// 实际客户端连上根本握不上手"。
		tlsConn := tls.Client(conn, &tls.Config{ServerName: sni})
		_ = tlsConn.SetDeadline(time.Now().Add(5 * time.Second))
		if hErr := tlsConn.Handshake(); hErr != nil {
			res.TLSError = hErr.Error()
			res.Message = fmt.Sprintf("TCP OK(%dms),TLS 握手失败:%s", res.LatencyMs, hErr.Error())
		} else {
			res.TLSOK = true
			res.Message = fmt.Sprintf("TCP+TLS 通(%dms,SNI=%s)", res.LatencyMs, sni)
		}
		_ = tlsConn.Close()
	case "reality":
		// Reality 是伪装 TLS:握手时服务端按真实站点行为返回证书,客户端
		// 用 publicKey 派生会话密钥。Go 的 tls.Client 不知道 Reality 协议,
		// 强行握手只能验到伪装站的证书,跟 Reality 实际可用性脱节,徒增噪音。
		// 这里仅给 TCP 通过的信号,Reality 真实可用性等用户走真客户端验。
		res.Message = fmt.Sprintf("TCP 通(%dms)— Reality 不做 TLS 握手(协议伪装层),客户端真连后再验", res.LatencyMs)
		_ = conn.Close()
	default:
		res.Message = fmt.Sprintf("TCP 通(%dms,无 TLS)", res.LatencyMs)
		_ = conn.Close()
	}
	return res, nil
}

// GenXrayRoutingRulesForInbounds 给所有"绑了出站"的入站生成对应 routing 规则
// {inboundTag:[<inbound.tag>], outboundTag:<inbound.OutboundTag>, type:"field"}。
// 调用方拿到后插入到 xrayConfig.RouterConfig.rules 里。
//
// 不在这里面 SELECT inbound:服务层避免循环依赖(InboundService 已经持有
// 出站的 tag 引用足够),所以让调用方传入 inbound 列表。
func (s *OutboundService) GenXrayRoutingRulesForInbounds(inbounds []*model.Inbound) []map[string]interface{} {
	rules := make([]map[string]interface{}, 0)
	for _, in := range inbounds {
		if !in.Enable || in.OutboundTag == "" || in.Tag == "" {
			continue
		}
		rules = append(rules, map[string]interface{}{
			"type":        "field",
			"inboundTag":  []string{in.Tag},
			"outboundTag": in.OutboundTag,
		})
	}
	return rules
}
