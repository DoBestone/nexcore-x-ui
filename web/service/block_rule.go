package service

import (
	"net"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"nexcore-x-ui/database"
	"nexcore-x-ui/database/model"
	"nexcore-x-ui/util/common"
)

// 合法的规则类型集合。前端下拉框要和这里保持一致。
var validBlockRuleTypes = map[string]bool{
	"domain":   true, // 域名 / 关键字 / regexp:xxx / domain:xxx / full:xxx
	"ip":       true, // IP / CIDR
	"geosite":  true, // category-gov-cn / cn / google ...(不需要 geosite: 前缀,服务侧自动加)
	"geoip":    true, // cn / private ...
	"port":     true, // 80 / 443 / 1000-2000
	"protocol": true, // http / tls / bittorrent
	"source":   true, // 来源 IP / CIDR (按客户端 IP 屏蔽)
}

// BlockRulePreset 描述一组开箱即用的预置规则。
type BlockRulePreset struct {
	Key         string             `json:"key"`         // 内部标识
	Name        string             `json:"name"`        // 用户可读名
	Description string             `json:"description"` // 一句话说明
	Rules       []model.BlockRule  `json:"rules"`       // 该预置展开成的规则集(InboundTag/Enable 由调用方填)
}

// 预置模板:常用屏蔽场景。Apply 时把每条规则原样插入数据库,InboundTag/Enable
// 用调用方传入的值。值故意写得"一目了然",方便用户事后增删。
var blockRulePresets = []BlockRulePreset{
	{
		Key:         "cn-gov",
		Name:        "屏蔽国内政府/教育网站",
		Description: "命中 geosite:category-gov-cn / category-edu-cn,以及常见 .gov.cn / .edu.cn 后缀",
		Rules: []model.BlockRule{
			{Type: "geosite", Value: "category-gov-cn", Remark: "国内政府"},
			{Type: "geosite", Value: "category-edu-cn", Remark: "国内教育"},
			{Type: "domain", Value: "domain:gov.cn,domain:edu.cn", Remark: "兜底:.gov.cn / .edu.cn"},
		},
	},
	{
		Key:         "cn-bank",
		Name:        "屏蔽国内银行/支付",
		Description: "国内主流银行 + 支付宝/微信支付/银联,避免风控触发",
		Rules: []model.BlockRule{
			{Type: "geosite", Value: "category-bank-cn", Remark: "国内银行"},
			{Type: "domain", Value: "domain:alipay.com,domain:tenpay.com,domain:unionpay.com", Remark: "支付宝/微信/银联"},
		},
	},
	{
		Key:         "bt",
		Name:        "屏蔽 BT/PT 流量",
		Description: "封锁 bittorrent 协议 + 常见 tracker 域名,避免触发服务器商滥用警告",
		Rules: []model.BlockRule{
			{Type: "protocol", Value: "bittorrent", Remark: "bittorrent 协议"},
			{Type: "geosite", Value: "category-public-tracker", Remark: "公共 tracker"},
		},
	},
	{
		Key:         "ads",
		Name:        "屏蔽广告 / 跟踪",
		Description: "geosite:category-ads-all,覆盖主流广告域名",
		Rules: []model.BlockRule{
			{Type: "geosite", Value: "category-ads-all", Remark: "广告 + 跟踪"},
		},
	},
	{
		Key:         "private",
		Name:        "屏蔽内网 IP 访问",
		Description: "防止用户透过 VPN 访问服务器内网(SSRF 风险)",
		Rules: []model.BlockRule{
			{Type: "geoip", Value: "private", Remark: "私网 IP 段"},
		},
	},
}

type BlockRuleService struct{}

// ListPresets 返回所有可用预置(纯只读,前端拉一次即可)。
func (s *BlockRuleService) ListPresets() []BlockRulePreset {
	return blockRulePresets
}

// ApplyPreset 把指定预置展开成多条规则插入数据库。
// inboundTag 留空表示全局规则;非空则只对该入站生效。
//
// 幂等:已经存在 (type, value, inboundTag) 完全相同的规则会跳过(若 disabled
// 则重新启用),不会插入重复行。这样用户多次点同一个预置不会爆出一堆重复。
func (s *BlockRuleService) ApplyPreset(presetKey, inboundTag string) error {
	var preset *BlockRulePreset
	for i := range blockRulePresets {
		if blockRulePresets[i].Key == presetKey {
			// 拷贝出来,避免调用方拿到全局指针后污染预置数据
			p := blockRulePresets[i]
			preset = &p
			break
		}
	}
	if preset == nil {
		return common.NewError("未知预置:", presetKey)
	}
	now := time.Now().Unix()
	db := database.GetDB()
	tx := db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()
	for _, r := range preset.Rules {
		var existing model.BlockRule
		err := tx.Where("type = ? AND value = ? AND inbound_tag = ?",
			r.Type, r.Value, inboundTag).First(&existing).Error
		if err == nil {
			// 已有同样规则:若被禁用则重新启用,跳过插入
			if !existing.Enable {
				if uerr := tx.Model(&existing).Update("enable", true).Error; uerr != nil {
					tx.Rollback()
					return uerr
				}
			}
			continue
		}
		if err != gorm.ErrRecordNotFound {
			tx.Rollback()
			return err
		}
		row := r
		row.Id = 0
		row.InboundTag = inboundTag
		row.Enable = true
		row.CreatedAt = now
		if err := tx.Create(&row).Error; err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit().Error
}

func (s *BlockRuleService) List() ([]*model.BlockRule, error) {
	db := database.GetDB()
	var rows []*model.BlockRule
	err := db.Order("id asc").Find(&rows).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	return rows, nil
}

func (s *BlockRuleService) Get(id int) (*model.BlockRule, error) {
	db := database.GetDB()
	r := &model.BlockRule{}
	err := db.First(r, id).Error
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (s *BlockRuleService) Add(r *model.BlockRule) error {
	if err := s.validate(r); err != nil {
		return err
	}
	r.Id = 0
	r.CreatedAt = time.Now().Unix()
	return database.GetDB().Create(r).Error
}

func (s *BlockRuleService) Update(r *model.BlockRule) error {
	if err := s.validate(r); err != nil {
		return err
	}
	old, err := s.Get(r.Id)
	if err != nil {
		return err
	}
	old.Type = r.Type
	old.Value = r.Value
	old.Remark = r.Remark
	old.InboundTag = r.InboundTag
	old.Enable = r.Enable
	return database.GetDB().Save(old).Error
}

func (s *BlockRuleService) SetEnable(id int, enable bool) error {
	return database.GetDB().Model(&model.BlockRule{}).
		Where("id = ?", id).
		Update("enable", enable).Error
}

func (s *BlockRuleService) Delete(id int) error {
	return database.GetDB().Delete(&model.BlockRule{}, id).Error
}

// 路由 protocol 字段在 Xray 里只支持这几个 sniffing 出来的协议名。
var validBlockRuleProtocols = map[string]bool{
	"http":       true,
	"tls":        true,
	"bittorrent": true,
	"ssh":        true,
	"socks":      true,
	"mqtt":       true,
	"quic":       true,
	"dns":        true,
}

// validate 做基础格式校验,防止用户填错规则把整个 Xray 启动炸掉
// (xray 配置非法 → 启动失败 → 所有用户断流量,而面板侧默默 cron 重启,
// 用户根本不知道哪条规则是凶手)。
func (s *BlockRuleService) validate(r *model.BlockRule) error {
	if !validBlockRuleTypes[r.Type] {
		return common.NewError("无效规则类型:", r.Type)
	}
	v := strings.TrimSpace(r.Value)
	if v == "" {
		return common.NewError("规则值不能为空")
	}
	switch r.Type {
	case "ip", "source":
		for _, item := range strings.Split(v, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if strings.HasPrefix(item, "geoip:") {
				continue // 允许混写 geoip:cn 这种,留给 xray 自己处理
			}
			if _, _, err := net.ParseCIDR(item); err == nil {
				continue
			}
			if net.ParseIP(item) == nil {
				return common.NewError("无效的 IP 或 CIDR: ", item)
			}
		}
	case "port":
		for _, item := range strings.Split(v, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			parts := strings.SplitN(item, "-", 2)
			for _, p := range parts {
				n, err := strconv.Atoi(strings.TrimSpace(p))
				if err != nil || n < 1 || n > 65535 {
					return common.NewError("无效端口(应为 1-65535 或 PORT-PORT): ", item)
				}
			}
		}
	case "protocol":
		for _, item := range strings.Split(v, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if !validBlockRuleProtocols[item] {
				return common.NewError("不支持的协议(仅 http/tls/bittorrent/ssh/socks/mqtt/quic/dns): ", item)
			}
		}
	}
	return nil
}

// GenXrayRoutingRules 生成要注入到 Xray routing.rules 数组的"屏蔽规则"。
// 调用方应把这里返回的规则**插入到现有 routing.rules 数组的最前面**,
// 保证黑名单优先级最高。
//
// 合并策略:同一作用域(InboundTag)同一类型的多条规则合并成一条 routing rule
// (例如所有"全局 + domain"类型的 value 合并到一条规则的 domain 数组),减少
// Xray 总规则数。
func (s *BlockRuleService) GenXrayRoutingRules() ([]map[string]interface{}, error) {
	rows, err := s.List()
	if err != nil {
		return nil, err
	}
	// key: inboundTag + "|" + type → 该桶下的所有 value
	type bucket struct {
		inboundTag string
		ruleType   string
		values     []string
	}
	order := []string{} // 保留稳定顺序,避免每次 GetXrayConfig 输出抖动
	groups := map[string]*bucket{}
	// 桶内 value 集合,用于去重(两条规则碰巧都含 gov.cn 时只保留一份)
	seen := map[string]map[string]bool{}

	for _, r := range rows {
		if !r.Enable {
			continue
		}
		key := r.InboundTag + "|" + r.Type
		b, ok := groups[key]
		if !ok {
			b = &bucket{inboundTag: r.InboundTag, ruleType: r.Type}
			groups[key] = b
			seen[key] = make(map[string]bool)
			order = append(order, key)
		}
		// 用户可以在单条规则的 Value 里用逗号分隔多个值,展开。
		for _, v := range strings.Split(r.Value, ",") {
			v = strings.TrimSpace(v)
			if v == "" || seen[key][v] {
				continue
			}
			seen[key][v] = true
			b.values = append(b.values, v)
		}
	}

	out := make([]map[string]interface{}, 0, len(order))
	for _, key := range order {
		b := groups[key]
		if len(b.values) == 0 {
			continue
		}
		rule := map[string]interface{}{
			"type":        "field",
			"outboundTag": "blocked",
		}
		if b.inboundTag != "" {
			rule["inboundTag"] = []string{b.inboundTag}
		}
		switch b.ruleType {
		case "domain":
			rule["domain"] = b.values
		case "ip":
			rule["ip"] = b.values
		case "geosite":
			rule["domain"] = prefixEach(b.values, "geosite:")
		case "geoip":
			rule["ip"] = prefixEach(b.values, "geoip:")
		case "port":
			rule["port"] = strings.Join(b.values, ",")
		case "protocol":
			rule["protocol"] = b.values
		case "source":
			rule["source"] = b.values
		}
		out = append(out, rule)
	}
	return out, nil
}

// prefixEach 给每个值加前缀(如果它还没有该前缀)。容忍用户在 Value 里手动写
// "geosite:cn"——不会重复成 "geosite:geosite:cn"。
func prefixEach(values []string, prefix string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if strings.HasPrefix(v, prefix) {
			out = append(out, v)
		} else {
			out = append(out, prefix+v)
		}
	}
	return out
}
