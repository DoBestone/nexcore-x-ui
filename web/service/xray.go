package service

import (
	"encoding/json"
	"errors"
	"sync"
	"nexcore-x-ui/database/model"
	"nexcore-x-ui/logger"
	"nexcore-x-ui/util/json_util"
	"nexcore-x-ui/xray"

	"go.uber.org/atomic"
)

var p *xray.Process
var lock sync.Mutex
var isNeedXrayRestart atomic.Bool
var result string

type XrayService struct {
	inboundService   InboundService
	settingService   SettingService
	blockRuleService BlockRuleService
	outboundService  OutboundService
}

func (s *XrayService) IsXrayRunning() bool {
	return p != nil && p.IsRunning()
}

func (s *XrayService) GetXrayErr() error {
	if p == nil {
		return nil
	}
	return p.GetErr()
}

func (s *XrayService) GetXrayResult() string {
	// Same `lock` that guards p / result mutations in RestartXray and
	// StopXray. Without holding it here, a concurrent restart can be
	// halfway through `result = ""` (line ~337) while this reader
	// returns the partially-cleared string, or a reader can call
	// p.GetResult() against a *xray.Process that the restart goroutine
	// has just swapped out.
	lock.Lock()
	defer lock.Unlock()
	if result != "" {
		return result
	}
	if p == nil || p.IsRunning() {
		return ""
	}
	result = p.GetResult()
	return result
}

func (s *XrayService) GetXrayVersion() string {
	if p == nil {
		return "Unknown"
	}
	return p.GetVersion()
}

func (s *XrayService) GetXrayConfig() (*xray.Config, error) {
	templateConfig, err := s.settingService.GetXrayConfigTemplate()
	if err != nil {
		return nil, err
	}

	xrayConfig := &xray.Config{}
	err = json.Unmarshal([]byte(templateConfig), xrayConfig)
	if err != nil {
		return nil, err
	}

	inbounds, err := s.inboundService.GetAllInbounds()
	if err != nil {
		return nil, err
	}
	// per-client enable=false 的客户从 inbound.settings.clients[] 里剥掉,
	// 否则 xray 看不到 disabled 标志(那个标志只活在 client_traffics 表里),
	// 用户在 modal 关 toggle / 业务系统 PATCH limits 之后,实际 xray 还在
	// 接受这个 client 的连接 — 历史 bug,v2.0.x 几个版本一直没修。空集合
	// fast-path 跑 0 次过滤,常见情况零成本。
	disabled, derr := (&ClientTrafficService{}).DisabledEmails()
	if derr != nil {
		// DB 失败时退化为不过滤(保守:宁可 disabled 漏拦,也不要因为
		// 这点失败把整 xray 启动卡死)。
		logger.Warning("加载 disabled client 列表失败,跳过过滤:", derr)
		disabled = map[string]struct{}{}
	}
	if len(disabled) > 0 {
		emails := make([]string, 0, len(disabled))
		for e := range disabled {
			emails = append(emails, e)
		}
		logger.Info("xray reload: 将剥离 disabled clients ", emails)
	}
	for _, inbound := range inbounds {
		if !inbound.Enable {
			continue
		}
		filtered := *inbound
		if len(disabled) > 0 {
			filtered.Settings = stripDisabledClientsFromSettings(inbound.Settings, disabled)
		}
		// 多用户协议在所有 client 都被 disable 时 settings.clients[] 变空,
		// xray 启动会拒绝(vmess/vless 至少要 1 个 user)。这种情况整条
		// inbound 跳过 — 等价于"所有用户被踢 → 入站临时下线",符合操作员
		// 的语义预期(也避免 xray 启动失败把整个面板的 xray 拖死)。
		if isMultiUserProtocol(&filtered) && hasNoActiveClients(filtered.Settings) {
			logger.Info("xray reload: inbound id=", filtered.Id, " 所有 clients 被 disable,临时跳过")
			continue
		}
		inboundConfig := filtered.GenXrayInboundConfig()
		xrayConfig.InboundConfigs = append(xrayConfig.InboundConfigs, *inboundConfig)
	}

	// 强制注入 access log 路径，保证在线 IP 统计可用。模板里如果配了
	// loglevel/error 等字段会保留，仅覆盖 access。
	logCfg := map[string]interface{}{}
	if len(xrayConfig.LogConfig) > 0 {
		_ = json.Unmarshal(xrayConfig.LogConfig, &logCfg)
	}
	logCfg["access"] = xray.GetAccessLogPath()
	// loglevel 默认必须 info — xray 的"accepted ... [tag] email: foo"行
	// 是 info 级别,默认 warning 会把它们全部过滤掉,导致 access.log 几乎
	// 为空,在线 IP tail 全员显示离线。代价是 access.log 体积增长,由
	// online_ip_service 周期性 truncate 防止把 1H1G 盘吃满。
	if _, ok := logCfg["loglevel"]; !ok {
		logCfg["loglevel"] = "info"
	}
	if logBytes, err := json.Marshal(logCfg); err == nil {
		xrayConfig.LogConfig = json_util.RawMessage(logBytes)
	}

	// 注入用户在面板配置的"屏蔽规则"。注入到 routing.rules 数组**最前面**，
	// 保证黑名单优先级高于模板中的其它规则。模板里已有的 api / blocked
	// 路由原样保留在后面。失败时静默跳过，不阻塞 xray 启动。
	if err := injectBlockRules(xrayConfig, &s.blockRuleService); err != nil {
		logger.Warning("注入屏蔽规则失败，跳过:", err)
	}

	// 注入用户配置的出站服务器 + 入站→出站路由。出站对象 append 到模板
	// outbounds 数组(放后面,template 自带的 freedom 仍是默认 fallback);
	// 路由规则 prepend 到 routing.rules,保证 inboundTag 命中后立即定向到
	// 用户出站,不会被模板里"非中国 IP → 直连"这种 catch-all 规则吞掉。
	if err := injectUserOutbounds(xrayConfig, &s.outboundService, inbounds); err != nil {
		logger.Warning("注入用户出站失败,跳过:", err)
	}

	return xrayConfig, nil
}

// hasNoActiveClients 判断多用户协议的 settings.clients[] 是否已为空。
// 调用方先做了 isMultiUserProtocol 判断,这里只看 clients 数组长度;
// 解析失败按"非空"处理(不轻易把 inbound 跳过)。
func hasNoActiveClients(settings string) bool {
	if settings == "" {
		return true
	}
	var probe struct {
		Clients []map[string]interface{} `json:"clients"`
	}
	if err := json.Unmarshal([]byte(settings), &probe); err != nil {
		return false
	}
	return len(probe.Clients) == 0
}

// stripDisabledClientsFromSettings 解析 inbound.Settings,把 clients[] 里
// email 命中 disabled 集合的条目剥掉,返回新的 settings JSON 字符串。
//
//   - VLESS / VMess / Trojan / SS-2022 multi-user 都把客户列表存在
//     `clients` 数组里(每条至少有 email 字段),走同一过滤路径
//   - SS-legacy / Socks / HTTP / Dokodemo 不在 clients[] 模型里 → JSON 里
//     根本没 clients 数组,过滤跑空,原样返回
//   - 解析失败 / 字段类型异常 → 静默退回原 settings,避免 1 个坏数据全栈卡死
//
// settings 里其它字段(decryption / fallbacks / disableInsecureEncryption /
// SS-2022 顶层 method+password)原样保留。
func stripDisabledClientsFromSettings(settings string, disabled map[string]struct{}) string {
	if settings == "" || len(disabled) == 0 {
		return settings
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(settings), &parsed); err != nil {
		return settings
	}
	rawClients, ok := parsed["clients"].([]interface{})
	if !ok || len(rawClients) == 0 {
		return settings
	}
	kept := make([]interface{}, 0, len(rawClients))
	stripped := false
	for _, c := range rawClients {
		m, ok := c.(map[string]interface{})
		if !ok {
			kept = append(kept, c)
			continue
		}
		email, _ := m["email"].(string)
		if email != "" {
			if _, dis := disabled[email]; dis {
				stripped = true
				continue
			}
		}
		kept = append(kept, m)
	}
	if !stripped {
		return settings
	}
	parsed["clients"] = kept
	out, err := json.Marshal(parsed)
	if err != nil {
		return settings
	}
	return string(out)
}

// injectUserOutbounds — 把 OutboundService.GenXrayOutboundConfigs() 生成的
// 出站对象 append 到 xrayConfig.OutboundConfigs (raw JSON);把
// GenXrayRoutingRulesForInbounds() 生成的路由规则 prepend 到
// xrayConfig.RouterConfig.rules。两个数组都是 RawMessage,需要 unmarshal
// → 改 → marshal。失败保留原 config 不动。
func injectUserOutbounds(
	cfg *xray.Config,
	svc *OutboundService,
	inbounds []*model.Inbound,
) error {
	userOutbounds, err := svc.GenXrayOutboundConfigs()
	if err != nil {
		return err
	}
	rules := svc.GenXrayRoutingRulesForInbounds(inbounds)
	if len(userOutbounds) == 0 && len(rules) == 0 {
		return nil
	}

	if len(userOutbounds) > 0 {
		var existing []interface{}
		if len(cfg.OutboundConfigs) > 0 {
			if err := json.Unmarshal(cfg.OutboundConfigs, &existing); err != nil {
				return err
			}
		}
		merged := existing
		for _, ob := range userOutbounds {
			merged = append(merged, ob)
		}
		out, err := json.Marshal(merged)
		if err != nil {
			return err
		}
		cfg.OutboundConfigs = json_util.RawMessage(out)
	}

	if len(rules) > 0 {
		router := map[string]interface{}{}
		if len(cfg.RouterConfig) > 0 {
			if err := json.Unmarshal(cfg.RouterConfig, &router); err != nil {
				return err
			}
		}
		existing, _ := router["rules"].([]interface{})
		merged := make([]interface{}, 0, len(rules)+len(existing))
		for _, r := range rules {
			merged = append(merged, r)
		}
		merged = append(merged, existing...)
		router["rules"] = merged
		out, err := json.Marshal(router)
		if err != nil {
			return err
		}
		cfg.RouterConfig = json_util.RawMessage(out)
	}
	return nil
}

// injectBlockRules 把 BlockRuleService 生成的规则插入到 xrayConfig.RouterConfig
// 的 rules 数组开头。RouterConfig 是 raw JSON，unmarshal → 改 → marshal。
func injectBlockRules(xrayConfig *xray.Config, svc *BlockRuleService) error {
	blockRules, err := svc.GenXrayRoutingRules()
	if err != nil {
		return err
	}
	if len(blockRules) == 0 {
		return nil
	}

	router := map[string]interface{}{}
	if len(xrayConfig.RouterConfig) > 0 {
		if err := json.Unmarshal(xrayConfig.RouterConfig, &router); err != nil {
			return err
		}
	}

	existing, _ := router["rules"].([]interface{})
	merged := make([]interface{}, 0, len(blockRules)+len(existing))
	for _, br := range blockRules {
		merged = append(merged, br)
	}
	merged = append(merged, existing...)
	router["rules"] = merged

	out, err := json.Marshal(router)
	if err != nil {
		return err
	}
	xrayConfig.RouterConfig = json_util.RawMessage(out)
	return nil
}

func (s *XrayService) GetXrayTraffic() ([]*xray.Traffic, error) {
	if !s.IsXrayRunning() {
		return nil, errors.New("xray is not running")
	}
	return p.GetTraffic(true)
}

func (s *XrayService) RestartXray(isForce bool) error {
	lock.Lock()
	defer lock.Unlock()
	logger.Debug("restart xray, force:", isForce)

	xrayConfig, err := s.GetXrayConfig()
	if err != nil {
		return err
	}

	if p != nil && p.IsRunning() {
		if !isForce && p.GetConfig().Equals(xrayConfig) {
			logger.Debug("not need to restart xray")
			return nil
		}
		p.Stop()
	}

	p = xray.NewProcess(xrayConfig)
	result = ""
	if err := p.Start(); err != nil {
		return err
	}
	// 启动 / 重置在线 IP 统计：首次幂等启动后台 tail，重启时清空旧状态
	// 并通知 tailer 重新打开新的 access.log。
	onlineSvc := GetOnlineIPService()
	onlineSvc.Start()
	onlineSvc.OnXrayRestart()
	// webhook worker 也跟着 OnlineIPService 一起起,Start() 幂等。
	// URL 没配的话 worker 跑空 tick,几乎零成本。
	GetOnlineWebhookService().Start()
	return nil
}

func (s *XrayService) StopXray() error {
	lock.Lock()
	defer lock.Unlock()
	logger.Debug("stop xray")
	if s.IsXrayRunning() {
		return p.Stop()
	}
	return errors.New("xray is not running")
}

func (s *XrayService) SetToNeedRestart() {
	isNeedXrayRestart.Store(true)
}

func (s *XrayService) IsNeedRestartAndSetFalse() bool {
	return isNeedXrayRestart.CAS(true, false)
}

// GetEffectiveConfig returns the JSON config that would be (or was) handed to
// the xray subprocess. Use it to diagnose what xray actually sees vs what is
// in the database.
func (s *XrayService) GetEffectiveConfig() (string, error) {
	cfg, err := s.GetXrayConfig()
	if err != nil {
		return "", err
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	return string(out), err
}

// GetRecentLogs returns up to 100 most recent stdout/stderr lines from xray.
// The buffer is kept in memory by the running process; lines older than the
// last restart are gone.
func (s *XrayService) GetRecentLogs() string {
	if p == nil {
		return ""
	}
	return p.GetResult()
}
