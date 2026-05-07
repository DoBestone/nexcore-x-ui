package service

import (
	_ "embed"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"nexcore-x-ui/database"
	"nexcore-x-ui/database/model"
	"nexcore-x-ui/logger"
	"nexcore-x-ui/util/common"
	"nexcore-x-ui/util/random"
	"nexcore-x-ui/util/reflect_util"
	"nexcore-x-ui/util/secret"
	"nexcore-x-ui/web/entity"
)

// sensitiveSettingKeys are the setting rows that get AES-GCM-wrapped at
// rest. The wrapping is transparent to higher-level callers: getString
// decrypts on read, setString encrypts on write. Legacy plaintext rows
// (from before this change) decrypt as-is and rewrap on the next write.
//
// Tokens stored in the multi-token api_tokens table are SHA256-hashed
// instead — they do not appear here.
var sensitiveSettingKeys = map[string]bool{
	"secret":              true, // gorilla/sessions HMAC key for cookie auth
	"apiToken":            true, // legacy single-token (multi-token table is hashed)
	"tgBotToken":          true, // Telegram bot token, can post to operator chat
	"onlineWebhookSecret": true, // HMAC key signing webhook bodies sent to ops backend
	"cfApiToken":          true, // Cloudflare API token, can edit DNS for any zone it scopes
}

// settingCache holds an in-memory copy of every setting row keyed by
// name. Hits are answered from the map; misses fall through to the DB
// and populate the cache. Writes go to DB first, then update the cache.
//
// Why cache: hot paths (CSRF, share host validation, base path lookup
// in the request middleware) call getString multiple times per
// request, each round-tripping to sqlite. The audit measured this at
// ~15-20 QPS of pointless reads for a quiet panel.
//
// Why this is safe with sensitive keys: the cache stores the plaintext
// (post-Decrypt). The on-disk form is still encrypted; the cache
// reflects what callers actually need. The cache is process-local so a
// DB-leak attacker doesn't gain anything from the cache existing.
var (
	settingCacheMu sync.RWMutex
	settingCache   = map[string]settingCacheEntry{}
)

type settingCacheEntry struct {
	value   string
	present bool // distinguishes "DB row exists with empty value" from "no row"
}

func settingCacheGet(key string) (settingCacheEntry, bool) {
	settingCacheMu.RLock()
	e, ok := settingCache[key]
	settingCacheMu.RUnlock()
	return e, ok
}

func settingCachePut(key string, e settingCacheEntry) {
	settingCacheMu.Lock()
	settingCache[key] = e
	settingCacheMu.Unlock()
}

// settingCacheInvalidate drops a single key. Called from saveSetting
// after a successful DB write so the next read sees the new value.
// We invalidate (rather than overwrite with the new value) for keys
// like "secret" where the cached value would be the plaintext but the
// caller passed in already-encrypted data — keeps the round-trip
// consistent with the read path.
func settingCacheInvalidate(key string) {
	settingCacheMu.Lock()
	delete(settingCache, key)
	settingCacheMu.Unlock()
}

// SettingsCacheReset clears the entire cache. Used on test setup and on
// settings table reset (ResetSettings).
func SettingsCacheReset() {
	settingCacheMu.Lock()
	settingCache = map[string]settingCacheEntry{}
	settingCacheMu.Unlock()
}

//go:embed config.json
var xrayTemplateConfig string

var defaultValueMap = map[string]string{
	"xrayTemplateConfig": xrayTemplateConfig,
	"webListen":          "",
	"webPort":            "54321",
	"webCertFile":        "",
	"webKeyFile":         "",
	"secret":             random.Seq(32),
	"webBasePath":        "/",
	"timeLocation":       "Asia/Shanghai",
	"tgBotEnable":        "false",
	"tgBotToken":         "",
	"tgBotChatId":        "0",
	"tgRunTime":          "",
	"apiToken":           "",
	// Comma-separated allow-list for ?host=... in /inbounds/:id/links
	// and /subscription. Empty = "any syntactically valid host accepted"
	// (legacy behavior). Operators that want to lock down their nodes to
	// known panel domains set this from the settings UI.
	"subAllowedHosts": "",
	// Online IP webhook — pushes per-email IP set deltas to a business
	// system that aggregates across nodes (multi-node device cap). Empty
	// URL disables. See OnlineWebhookService for protocol/signing.
	"onlineWebhookUrl":    "",
	"onlineWebhookSecret": "",
	"onlineWebhookNodeId": "",
	// 节点名称(e.g. "香港节点1")。share link 的 ps/remarks 字段以
	// "[<nodeName>] <email>" 的形式注入,客户端导入订阅时一眼能看出
	// 这一条来自哪个节点。被某个出站绑定的入站会改用出站名称作为前缀,
	// 见 ShareService 链接生成。
	"nodeName": "",
	// 安全入口:启用后面板只在 webBasePath + secureEntryPath/ 下应答,
	// 其它路径全部 404 — 端口扫描看不到任何登录界面,显著提升被自动化
	// 工具发现的门槛。默认关闭,启用前提示用户记下完整 URL,否则改完
	// 重启自己也进不来。
	"secureEntryEnabled": "false",
	"secureEntryPath":    "",
	// 分享链接里写的节点地址。空 = 用浏览器访问面板的 Host(老行为)。
	// CF 橙云代理场景:面板挂在 example.com(CF 代理 80/443),但 xray
	// 跑在 10000 这种端口,CF 不代理 → 客户端连超时。把 nodeAddress 设成
	// 直连 IP 或者另一个 DNS-only 子域,分享链接里走那条路。
	"nodeAddress": "",
	// CF API token(Zone:DNS:Edit 权限),用于:
	//   - 域名绑定 DNS-01 模式取证书 / 续费
	//   - 面板内一键切换橙云/灰云(代理状态)
	// 加密存(走 sensitiveSettingKeys),前端只能写不能回读。
	"cfApiToken": "",
}

type SettingService struct {
}

func (s *SettingService) GetAllSetting() (*entity.AllSetting, error) {
	db := database.GetDB()
	settings := make([]*model.Setting, 0)
	err := db.Model(model.Setting{}).Find(&settings).Error
	if err != nil {
		return nil, err
	}
	allSetting := &entity.AllSetting{}
	t := reflect.TypeOf(allSetting).Elem()
	v := reflect.ValueOf(allSetting).Elem()
	fields := reflect_util.GetFields(t)

	setSetting := func(key, value string) (err error) {
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				err = errors.New(fmt.Sprint(panicErr))
			}
		}()

		var found bool
		var field reflect.StructField
		for _, f := range fields {
			if f.Tag.Get("json") == key {
				field = f
				found = true
				break
			}
		}

		if !found {
			// 有些设置自动生成，不需要返回到前端给用户修改
			return nil
		}

		fieldV := v.FieldByName(field.Name)
		switch t := fieldV.Interface().(type) {
		case int:
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return err
			}
			fieldV.SetInt(n)
		case string:
			fieldV.SetString(value)
		case bool:
			fieldV.SetBool(value == "true")
		default:
			return common.NewErrorf("unknown field %v type %v", key, t)
		}
		return
	}

	keyMap := map[string]bool{}
	for _, setting := range settings {
		err := setSetting(setting.Key, setting.Value)
		if err != nil {
			return nil, err
		}
		keyMap[setting.Key] = true
	}

	for key, value := range defaultValueMap {
		if keyMap[key] {
			continue
		}
		err := setSetting(key, value)
		if err != nil {
			return nil, err
		}
	}

	// 安全:加密 token 类字段不回传明文 — 哪怕 panel session 已认证,
	// 浏览器内存 / Network 面板 / response 缓存都可能拿到;前端表单组件
	// 应该显示成"已配置"或空 password input,用户不输入就保留旧值
	// (UpdateAllSetting 里 skipIfEmptyKeys 兜了这条语义)。
	for key := range skipIfEmptyKeys {
		for _, f := range fields {
			if f.Tag.Get("json") == key {
				if fv := v.FieldByName(f.Name); fv.Kind() == reflect.String {
					fv.SetString("")
				}
				break
			}
		}
	}

	return allSetting, nil
}

func (s *SettingService) ResetSettings() error {
	db := database.GetDB()
	if err := db.Where("1 = 1").Delete(model.Setting{}).Error; err != nil {
		return err
	}
	SettingsCacheReset()
	return nil
}

func (s *SettingService) getSetting(key string) (*model.Setting, error) {
	db := database.GetDB()
	setting := &model.Setting{}
	err := db.Model(model.Setting{}).Where("key = ?", key).First(setting).Error
	if err != nil {
		return nil, err
	}
	return setting, nil
}

func (s *SettingService) saveSetting(key string, value string) error {
	stored := value
	if sensitiveSettingKeys[key] && value != "" {
		enc, err := secret.Encrypt(value)
		if err != nil {
			return fmt.Errorf("encrypt setting %q: %w", key, err)
		}
		stored = enc
	}
	setting, err := s.getSetting(key)
	db := database.GetDB()
	if database.IsNotFound(err) {
		if err := db.Create(&model.Setting{
			Key:   key,
			Value: stored,
		}).Error; err != nil {
			return err
		}
		settingCacheInvalidate(key)
		return nil
	} else if err != nil {
		return err
	}
	setting.Key = key
	setting.Value = stored
	if err := db.Save(setting).Error; err != nil {
		return err
	}
	settingCacheInvalidate(key)
	return nil
}

func (s *SettingService) getString(key string) (string, error) {
	if e, ok := settingCacheGet(key); ok {
		if !e.present {
			// Cached "no DB row" — fall through to defaults below.
			if v, defOk := defaultValueMap[key]; defOk {
				return v, nil
			}
			return "", common.NewErrorf("key <%v> not in defaultValueMap", key)
		}
		return e.value, nil
	}
	setting, err := s.getSetting(key)
	if database.IsNotFound(err) {
		settingCachePut(key, settingCacheEntry{present: false})
		value, ok := defaultValueMap[key]
		if !ok {
			return "", common.NewErrorf("key <%v> not in defaultValueMap", key)
		}
		return value, nil
	} else if err != nil {
		return "", err
	}
	value := setting.Value
	if sensitiveSettingKeys[key] {
		// Transparent: legacy plaintext rows (no envelope prefix) come
		// back unchanged; encrypted rows decrypt to plaintext. The next
		// setString call rewraps the legacy rows automatically.
		dec, derr := secret.Decrypt(setting.Value)
		if derr != nil {
			return "", fmt.Errorf("decrypt setting %q: %w", key, derr)
		}
		value = dec
	}
	settingCachePut(key, settingCacheEntry{value: value, present: true})
	return value, nil
}

func (s *SettingService) setString(key string, value string) error {
	return s.saveSetting(key, value)
}

func (s *SettingService) getBool(key string) (bool, error) {
	str, err := s.getString(key)
	if err != nil {
		return false, err
	}
	return strconv.ParseBool(str)
}

func (s *SettingService) setBool(key string, value bool) error {
	return s.setString(key, strconv.FormatBool(value))
}

func (s *SettingService) getInt(key string) (int, error) {
	str, err := s.getString(key)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(str)
}

func (s *SettingService) setInt(key string, value int) error {
	return s.setString(key, strconv.Itoa(value))
}

func (s *SettingService) GetXrayConfigTemplate() (string, error) {
	return s.getString("xrayTemplateConfig")
}

// GetNodeName 返回面板配置的节点名称(e.g. "香港节点1")。空字符串表示
// 用户没设过 — share link 的 ps 字段就退回到 email/remark,跟旧行为一致。
// 失败时也返回空,share link 流程不应被一次 settings 读取失败拖垮。
func (s *SettingService) GetNodeName() string {
	v, err := s.getString("nodeName")
	if err != nil {
		return ""
	}
	return v
}

// GetSecureEntryEnabled / GetSecureEntryPath — 安全入口。当 enabled = true 且
// path 非空时,initRouter 把 path 拼到 webBasePath 后作为生效前缀,
// 任何不在该前缀下的请求一律 404。失败统一退回"未启用",否则一次 DB
// 抖动可能把用户彻底锁死在外面。
func (s *SettingService) GetSecureEntryEnabled() bool {
	v, err := s.getBool("secureEntryEnabled")
	if err != nil {
		return false
	}
	return v
}

func (s *SettingService) GetSecureEntryPath() string {
	v, err := s.getString("secureEntryPath")
	if err != nil {
		return ""
	}
	return v
}

// GetNodeAddress 返回操作员配置的"节点地址"(分享链接里写的 host)。
// 空字符串 = 退回到浏览器访问面板的 Host。CF 橙云 + 面板域名场景下这是
// "客户端连得通"的关键 — CF 不代理 xray 跑的非标端口,链接 host 必须
// 指向直连 origin 才行。
func (s *SettingService) GetNodeAddress() string {
	v, err := s.getString("nodeAddress")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// GetCfApiToken 返回明文 CF API token(setString/getString 自动解密)。
// 仅服务侧使用 — 前端 AllSetting 字段是 string 但保存时如果传空字符串
// 会被忽略(避免回读时拿到空字符串导致前端"误清"),只有显式给非空才覆盖。
// 详见 SettingController.update 的清洗逻辑。
func (s *SettingService) GetCfApiToken() string {
	v, err := s.getString("cfApiToken")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

func (s *SettingService) GetListen() (string, error) {
	return s.getString("webListen")
}

func (s *SettingService) GetTgBotToken() (string, error) {
	return s.getString("tgBotToken")
}

func (s *SettingService) SetTgBotToken(token string) error {
	return s.setString("tgBotToken", token)
}

func (s *SettingService) GetTgBotChatId() (int, error) {
	return s.getInt("tgBotChatId")
}

func (s *SettingService) SetTgBotChatId(chatId int) error {
	return s.setInt("tgBotChatId", chatId)
}

func (s *SettingService) SetTgbotenabled(value bool) error {
	return s.setBool("tgBotEnable", value)
}

func (s *SettingService) GetTgbotenabled() (bool, error) {
	return s.getBool("tgBotEnable")
}

func (s *SettingService) SetTgbotRuntime(time string) error {
	return s.setString("tgRunTime", time)
}

func (s *SettingService) GetTgbotRuntime() (string, error) {
	return s.getString("tgRunTime")
}

func (s *SettingService) GetPort() (int, error) {
	return s.getInt("webPort")
}

func (s *SettingService) SetPort(port int) error {
	if err := s.setInt("webPort", port); err != nil {
		return err
	}
	// Same reasoning as UserService: the install-info.txt port value is now stale.
	database.RemoveInstallInfo()
	return nil
}

func (s *SettingService) GetCertFile() (string, error) {
	return s.getString("webCertFile")
}

func (s *SettingService) GetKeyFile() (string, error) {
	return s.getString("webKeyFile")
}

func (s *SettingService) GetSecret() ([]byte, error) {
	secret, err := s.getString("secret")
	if secret == defaultValueMap["secret"] {
		err := s.saveSetting("secret", secret)
		if err != nil {
			logger.Warning("save secret failed:", err)
		}
	}
	return []byte(secret), err
}

func (s *SettingService) GetBasePath() (string, error) {
	basePath, err := s.getString("webBasePath")
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	if !strings.HasSuffix(basePath, "/") {
		basePath += "/"
	}
	return basePath, nil
}

func (s *SettingService) GetAPIToken() (string, error) {
	return s.getString("apiToken")
}

func (s *SettingService) SetAPIToken(token string) error {
	return s.setString("apiToken", token)
}

// 在线 IP webhook — 给上游业务系统跨节点聚合在线 IP 用,见 OnlineWebhookService。
func (s *SettingService) GetOnlineWebhookUrl() (string, error) {
	return s.getString("onlineWebhookUrl")
}

func (s *SettingService) SetOnlineWebhookUrl(url string) error {
	return s.setString("onlineWebhookUrl", url)
}

func (s *SettingService) GetOnlineWebhookSecret() (string, error) {
	return s.getString("onlineWebhookSecret")
}

func (s *SettingService) SetOnlineWebhookSecret(v string) error {
	return s.setString("onlineWebhookSecret", v)
}

func (s *SettingService) GetOnlineWebhookNodeId() (string, error) {
	return s.getString("onlineWebhookNodeId")
}

func (s *SettingService) SetOnlineWebhookNodeId(v string) error {
	return s.setString("onlineWebhookNodeId", v)
}

// EnsureAPIToken returns the current API token, generating and persisting a
// fresh one if none exists. Called once at server start so that operators see
// a token in the logs of a fresh deployment.
func (s *SettingService) EnsureAPIToken() (string, bool, error) {
	token, err := s.GetAPIToken()
	if err != nil {
		return "", false, err
	}
	if token != "" {
		return token, false, nil
	}
	token = random.Seq(48)
	if err := s.SetAPIToken(token); err != nil {
		return "", false, err
	}
	return token, true, nil
}

// GetSubAllowedHosts returns the comma-separated allow-list of hosts
// accepted by the share/subscription endpoints. Empty string means
// "any syntactically valid host" — see validateShareHost.
func (s *SettingService) GetSubAllowedHosts() (string, error) {
	return s.getString("subAllowedHosts")
}

func (s *SettingService) GetTimeLocation() (*time.Location, error) {
	l, err := s.getString("timeLocation")
	if err != nil {
		return nil, err
	}
	location, err := time.LoadLocation(l)
	if err != nil {
		defaultLocation := defaultValueMap["timeLocation"]
		logger.Errorf("location <%v> not exist, using default location: %v", l, defaultLocation)
		return time.LoadLocation(defaultLocation)
	}
	return location, nil
}

// skipIfEmptyKeys 是"前端不回读、空值视作未改"的设置 key 集合 — 主要是
// 各种 token / secret。表单 password input 默认显示空 placeholder,用户
// 不输入就提交时,不应该把 DB 里存好的 token 清空。
var skipIfEmptyKeys = map[string]bool{
	"tgBotToken":          true,
	"onlineWebhookSecret": true,
	"cfApiToken":          true,
}

func (s *SettingService) UpdateAllSetting(allSetting *entity.AllSetting) error {
	if err := allSetting.CheckValid(); err != nil {
		return err
	}

	v := reflect.ValueOf(allSetting).Elem()
	t := reflect.TypeOf(allSetting).Elem()
	fields := reflect_util.GetFields(t)
	errs := make([]error, 0)
	for _, field := range fields {
		key := field.Tag.Get("json")
		fieldV := v.FieldByName(field.Name)
		value := fmt.Sprint(fieldV.Interface())
		if skipIfEmptyKeys[key] && strings.TrimSpace(value) == "" {
			continue
		}
		err := s.saveSetting(key, value)
		if err != nil {
			errs = append(errs, err)
		}
	}
	// Operator touched panel settings — assume webPort may have moved; the
	// install-info.txt snapshot is therefore unreliable.
	database.RemoveInstallInfo()
	return common.Combine(errs...)
}
