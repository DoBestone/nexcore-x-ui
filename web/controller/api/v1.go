package api

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"nexcore-x-ui/database/model"
	"nexcore-x-ui/logger"
	"nexcore-x-ui/web/entity"
	"nexcore-x-ui/web/global"
	"nexcore-x-ui/web/service"
	"nexcore-x-ui/xray"
)

// mapInboundErr maps service-layer typed errors into stable API error codes
// so business systems can switch on them. Falls through to the caller's
// fallback for unknown errors (port collision, db write failure, etc.).
func mapInboundErr(err error, fallback string) string {
	switch {
	case errors.Is(err, service.ErrXrayConfigInvalid):
		return "xray_config_invalid"
	case errors.Is(err, service.ErrProtocolSingleton):
		return "protocol_singleton"
	default:
		return fallback
	}
}

// mapOutboundErr does the same for OutboundService typed errors. The codes
// are deliberately stable (callers may switch on them); fallback covers
// anything we haven't classified — e.g. a raw GORM write failure.
func mapOutboundErr(err error, fallback string) string {
	switch {
	case errors.Is(err, service.ErrOutboundNotFound):
		return "outbound_not_found"
	case errors.Is(err, service.ErrOutboundTagInvalid):
		return "tag_invalid"
	case errors.Is(err, service.ErrOutboundTagDuplicate):
		return "tag_duplicate"
	case errors.Is(err, service.ErrOutboundTagReserved):
		return "tag_reserved"
	case errors.Is(err, service.ErrOutboundProtocolUnsup):
		return "protocol_unsupported"
	case errors.Is(err, service.ErrOutboundInvalidJSON):
		return "invalid_json"
	default:
		return fallback
	}
}

// V1Controller exposes node-control endpoints under /api/v1.
type V1Controller struct {
	inboundService  service.InboundService
	outboundService service.OutboundService
	xrayService     service.XrayService
	serverService   service.ServerService
	settingService  service.SettingService
	userService     service.UserService
	tokenService    service.APITokenService
	clientService   service.ClientService
	shareService    service.ShareService
	certService     service.CertService
	systemService   service.SystemService
	magicService    service.MagicTokenService
	apiLogService        service.APILogService
	updateService        service.UpdateService
	blockRuleService     service.BlockRuleService
	clientTrafficService service.ClientTrafficService

	// serverStatusLast 是 /server/status 上一拍的样本,用来给当前请求
	// 算瞬时网络速率(NetIO.Up/Down)。无前一拍 → ServerService.GetStatus
	// 拿不出差值 → 速率永远 0,这是 v2.2.0 之前 API 返回 0 速率的根因。
	// 跨请求共享 — gin handler 并发跑,必须 mutex。
	serverStatusMu   sync.Mutex
	serverStatusLast *service.Status
}

func NewV1Controller(g *gin.RouterGroup) *V1Controller {
	c := &V1Controller{}
	c.register(g)
	return c
}

func (a *V1Controller) register(g *gin.RouterGroup) {
	// Access log middleware is mounted on the whole group so /health is
	// also recorded in case operators want to see liveness churn.
	g.Use(AccessLogMiddleware(&a.apiLogService))

	// Liveness — never authenticated; useful for load balancers and the
	// business system to detect a node before it has a token configured.
	g.GET("/health", a.health)

	// Auth gate. Below this point every request has a verified token.
	authed := g.Group("")
	authed.Use(AuthMiddleware(&a.settingService, &a.tokenService))
	// Rate-limit AFTER auth so anonymous floods on /api/v1/* are
	// rejected at AuthMiddleware (cheap), while authenticated callers
	// share a per-token budget. Mounting before AuthMiddleware would
	// burn rate-limit tokens on every 401 attempt, which is what
	// attackers want.
	authed.Use(RateLimitMiddleware())

	// Three sub-groups by scope. Mount the most permissive endpoints
	// (subscription) under the widest allow-list and tighten from there:
	//   - sub: any scope
	//   - ro:  admin or readonly (state-observing GETs)
	//   - api: admin only (state-changing or sensitive)
	// Tokens default to admin scope on creation, so existing deployments
	// keep working unchanged.
	sub := authed.Group("",
		RequireScope(service.ScopeAdmin, service.ScopeReadOnly, service.ScopeSubscription))
	ro := authed.Group("",
		RequireScope(service.ScopeAdmin, service.ScopeReadOnly))
	api := authed.Group("", RequireScope(service.ScopeAdmin))

	// ---- subscription scope: share/subscription only ----
	sub.GET("/inbounds/:id/links", a.inboundLinks)
	sub.GET("/subscription", a.subscriptionAll)
	sub.GET("/inbounds/:id/subscription", a.subscriptionOne)

	// ---- read-only scope: observation, no mutation ----
	ro.GET("/server/status", a.serverStatus)
	ro.GET("/xray/status", a.xrayStatus)
	ro.GET("/xray/config", a.xrayEffectiveConfig)
	ro.GET("/xray/logs", a.xrayLogs)
	ro.GET("/xray/template", a.xrayTemplateGet)
	ro.GET("/inbounds", a.listInbounds)
	ro.GET("/inbounds/:id", a.getInbound)
	ro.GET("/inbounds/:id/clients", a.listClients)
	ro.GET("/outbounds", a.listOutbounds)
	ro.GET("/outbounds/:id", a.getOutbound)
	ro.GET("/block-rules/:id", a.getBlockRule)
	ro.GET("/traffic", a.dbTraffic)
	ro.GET("/traffic/live", a.liveTraffic)
	ro.GET("/online-ips", a.listOnlineIps)
	ro.GET("/online-ips-by-email", a.listOnlineIpsByEmail)
	ro.GET("/online-ips/:tag", a.listOnlineIpsByTag)
	ro.GET("/block-rules", a.listBlockRules)
	ro.GET("/block-rules/presets", a.listBlockRulePresets)
	ro.GET("/system/listening-ports", a.listeningPorts)
	ro.GET("/system/check-port", a.checkPort)
	ro.GET("/system/update-check", a.updateCheck)
	ro.GET("/access-logs", a.listAccessLogs)
	ro.GET("/certs", a.listCerts)
	ro.GET("/settings", a.getSettings)
	ro.GET("/tokens", a.listTokens)

	// ---- admin scope: everything that mutates state or exposes secrets ----
	api.POST("/xray/restart", a.xrayRestart)
	api.PUT("/xray/template", a.xrayTemplatePut)

	api.POST("/inbounds", a.createInbound)
	api.PUT("/inbounds/:id", a.updateInbound)
	api.PATCH("/inbounds/:id/enable", a.setInboundEnable)
	api.DELETE("/inbounds/:id", a.deleteInbound)
	api.POST("/inbounds/:id/reset-traffic", a.resetInboundTraffic)

	api.POST("/inbounds/bulk", a.bulkCreateInbounds)
	api.PATCH("/inbounds/bulk-enable", a.bulkSetEnable)
	api.POST("/inbounds/bulk-delete", a.bulkDelete)
	api.POST("/inbounds/reset-all-traffic", a.bulkResetTraffic)
	// 一键扫表禁用过期 / 配额耗尽的入站。镜像 /clients/disable-expired,
	// 让业务系统在月初对账或定时任务里把"已经停服但仍 enable=true"的
	// 入站统一关掉,避免下次 xray reload 把超额客户继续放出去。
	api.POST("/inbounds/disable-invalid", a.disableInvalidInbounds)

	// Outbound CRUD — mirrors inbound REST shape. Service layer
	// (OutboundService) already owns validation, tag-uniqueness, and the
	// "clear dangling outbound_tag on delete" side effect — handlers stay
	// thin.
	api.POST("/outbounds", a.createOutbound)
	api.PUT("/outbounds/:id", a.updateOutbound)
	api.PATCH("/outbounds/:id/enable", a.setOutboundEnable)
	api.DELETE("/outbounds/:id", a.deleteOutbound)
	api.POST("/outbounds/bulk-delete", a.bulkDeleteOutbounds)
	// 出站连通测试 — TCP dial + 可选 TLS 握手,不动 xray 进程。前端按
	// OutboundTestResult 直接展示;业务系统也能用来在加完中转节点后做
	// 自动校验。
	api.POST("/outbounds/:id/test", a.testOutbound)

	api.PATCH("/settings", a.patchSettings)
	api.POST("/settings/api-token/rotate", a.rotateAPIToken)

	api.POST("/tokens", a.createToken)
	api.PATCH("/tokens/:id", a.renameToken)
	api.POST("/tokens/:id/revoke", a.revokeToken)
	api.DELETE("/tokens/:id", a.deleteToken)

	api.POST("/inbounds/:id/clients", a.addClient)
	api.PUT("/inbounds/:id/clients/:identifier", a.updateClient)
	api.DELETE("/inbounds/:id/clients/:identifier", a.deleteClient)

	// v1.1.0 per-client traffic / quota / expiry —— 业务系统对接面。
	// 按 email 全局唯一,所以路径以 email 为主,不嵌入 inbound id;
	// 列表查询则按 inbound id 来(入站详情页用)。
	api.GET("/inbounds/:id/client-traffics", a.listClientTraffics)
	api.GET("/clients/:email/traffic", a.getClientTraffic)
	api.POST("/clients/:email/reset-traffic", a.resetClientTraffic)
	api.PATCH("/clients/:email/limits", a.patchClientLimits)
	api.PATCH("/clients/:email/enable", a.setClientEnable) // 对称 PATCH /inbounds/:id/enable
	api.POST("/clients/disable-expired", a.disableExpiredClients)

	// v2.5.0 share-link + QR API —— 业务系统对接面。
	//   GET /inbounds/:id/links/by-email      → {<email>: {link, qrcode}}
	//   GET /inbounds/:id/clients/:email/share → {link, qrcode} 单个客户端
	// qrcode 是完整的 data:image/png;base64,... data URL,可直接喂 <img src>。
	// host 解析顺序:?host= → settings.nodeAddress → c.Request.Host(去 port)。
	// 跟 panel 的 /xui/api/inbounds/:id/links 同款,免去"忘传 host=400"。
	// 仍走 ValidateShareHostSyntactic + subAllowedHosts 白名单。
	sub.GET("/inbounds/:id/links/by-email", a.inboundLinksByEmail)
	sub.GET("/inbounds/:id/clients/:email/share", a.clientShare)

	api.POST("/certs", a.uploadCert)
	api.DELETE("/certs/:name", a.deleteCert)

	// 屏蔽规则:命中流量路由到 outbound `blocked`(黑洞)。变更后会触发 xray 重启。
	api.POST("/block-rules", a.createBlockRule)
	api.PUT("/block-rules/:id", a.updateBlockRule)
	api.PATCH("/block-rules/:id/enable", a.setBlockRuleEnable)
	api.DELETE("/block-rules/:id", a.deleteBlockRule)
	api.POST("/block-rules/apply-preset", a.applyBlockRulePreset)

	api.POST("/system/restart-panel", a.restartPanel)
	api.POST("/login-tokens", a.createMagicToken)
	api.DELETE("/access-logs", a.purgeAccessLogs)
	api.POST("/system/update-apply", a.updateApply)

	// /server/status 的瞬时速率(NetIO.Up/Down)依赖跨拍 lastStatus 做差。
	// 旧设计下 lastStatus 仅靠"上次请求"刷新,业务系统轮询 1–2 分钟一次
	// 时,每次都被 ServerService 判 stale → 永远拿 0。这里挂一条
	// @every 5s 主动 ticker,跟 panel 的 /server/status cron 一个套路:
	// 无论调用方多久来一次,handler 返回的 rate 都基于"最近 5s 窗口",
	// 跟 panel 行为对齐。失败不致命 —— 即使 cron 未注册,handler 仍能
	// 用请求驱动的方式工作(只是慢轮询场景拿 0)。
	if ws := global.GetWebServer(); ws != nil {
		if cr := ws.GetCron(); cr != nil {
			if _, err := cr.AddFunc("@every 5s", func() {
				a.serverStatusMu.Lock()
				a.serverStatusLast = a.serverService.GetStatus(a.serverStatusLast)
				a.serverStatusMu.Unlock()
			}); err != nil {
				logger.Warning("v1 server-status ticker register failed:", err)
			}
		}
	}
}

// ---------- handlers ----------

func (a *V1Controller) health(c *gin.Context) {
	OK(c, gin.H{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

// serverStatus 返回 CPU / 内存 / 磁盘 / 上下行速率 / TCP/UDP 连接数 /
// 累计流量 / xray 运行状态。
//
// 速率(NetIO.Up/Down)需要两拍样本做差才能算出来 — 这个 handler 通过
// serverStatusLast 在跨请求间维护前一拍。首次调用拿到 0 速率(无前
// 一拍可减),第二次开始返回真实速率,值是"两次 API 调用之间的均值"。
// 跨请求样本被 ServerService 内部 30s 上限丢弃,避免长间隔轮询拿到
// "几小时均值"假装"瞬时"。
//
// 累计流量(NetTraffic.Sent/Recv)从首次调用就正确填充 — 业务系统如果
// 自己想精确算特定窗口的速率,记两次累计差自己除即可,不依赖这边的速率字段。
func (a *V1Controller) serverStatus(c *gin.Context) {
	a.serverStatusMu.Lock()
	cur := a.serverService.GetStatus(a.serverStatusLast)
	a.serverStatusLast = cur
	a.serverStatusMu.Unlock()
	OK(c, cur)
}

func (a *V1Controller) xrayStatus(c *gin.Context) {
	running := a.xrayService.IsXrayRunning()
	out := gin.H{
		"running": running,
		"version": a.xrayService.GetXrayVersion(),
	}
	if !running {
		if err := a.xrayService.GetXrayErr(); err != nil {
			out["error"] = err.Error()
		}
		out["lastOutput"] = a.xrayService.GetXrayResult()
	}
	OK(c, out)
}

func (a *V1Controller) xrayRestart(c *gin.Context) {
	if err := a.xrayService.RestartXray(true); err != nil {
		Internal(c, "xray_restart_failed", err)
		return
	}
	OK(c, gin.H{"restarted": true})
}

// listInbounds returns the inbound roster. By default the heavy fields
// (settings/streamSettings — multi-KB JSON blobs each) are stripped:
// callers that just want to list "what's there" pay 80%+ less bytes.
// Pass ?include=settings (or ?full=1) to get the complete row, which
// is what the panel UI's edit modal needs.
//
// Without this, a 100-inbound deployment was shipping 200KB+ JSON on
// every poll of the inbound list, and the dashboard polls every few
// seconds.
func (a *V1Controller) listInbounds(c *gin.Context) {
	q := service.InboundQuery{
		Protocol: c.Query("protocol"),
		Tag:      c.Query("tag"),
		Search:   c.Query("search"),
	}
	if v := c.Query("enable"); v != "" {
		b := v == "true" || v == "1"
		q.Enable = &b
	}
	if v := c.Query("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			q.Page = n
		}
	}
	if v := c.Query("size"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			q.Size = n
		}
	}
	items, total, err := a.inboundService.QueryInbounds(q)
	if err != nil {
		Internal(c, "db_error", err)
		return
	}

	full := c.Query("full") == "1" || strings.Contains(c.Query("include"), "settings")
	body := projectInbounds(items, full)
	if q.Page > 0 {
		c.JSON(200, gin.H{
			"data": body,
			"meta": gin.H{"total": total, "page": q.Page, "size": q.Size, "full": full},
		})
		return
	}
	OK(c, body)
}

// projectInbounds returns either the full rows (full=true) or a slim
// view with the heavy JSON fields elided. Operators that want the
// detail can opt back in via ?full=1; the default optimizes for the
// common dashboard-list case.
func projectInbounds(items []*model.Inbound, full bool) any {
	if full {
		return items
	}
	out := make([]gin.H, 0, len(items))
	for _, in := range items {
		out = append(out, gin.H{
			"id":          in.Id,
			"port":        in.Port,
			"protocol":    in.Protocol,
			"tag":         in.Tag,
			"remark":      in.Remark,
			"enable":      in.Enable,
			"listen":      in.Listen,
			"up":          in.Up,
			"down":        in.Down,
			"total":       in.Total,
			"expiryTime":  in.ExpiryTime,
			// outboundTag 是路由/中转关系的关键字段(空 = 直连;非空 = 入站
			// 的流量在 xray rules 里被定向到对应 outbound)。业务系统拉
			// 入站列表做"哪条入站走哪个出口"的对账时必须有它,否则只能
			// fallback 到 ?full=1 拉巨大的 settings/streamSettings 字节,
			// 跟 slim 视图"省 80% 流量"的初衷冲突。
			"outboundTag": in.OutboundTag,
		})
	}
	return out
}

func (a *V1Controller) getInbound(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	item, err := a.inboundService.GetInboundCtx(c.Request.Context(), id)
	if err != nil {
		NotFound(c, "inbound_not_found", err.Error())
		return
	}
	OK(c, item)
}

func (a *V1Controller) createInbound(c *gin.Context) {
	var in model.Inbound
	if err := c.ShouldBindJSON(&in); err != nil {
		BadRequest(c, "invalid_body", err.Error())
		return
	}
	user, err := a.userService.GetFirstUser()
	if err == nil && user != nil {
		in.UserId = user.Id
	}
	if err := a.inboundService.AddInbound(&in); err != nil {
		BadRequest(c, mapInboundErr(err, "create_failed"), err.Error())
		return
	}
	a.xrayService.SetToNeedRestart()
	Created(c, in)
}

func (a *V1Controller) updateInbound(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	var in model.Inbound
	if err := c.ShouldBindJSON(&in); err != nil {
		BadRequest(c, "invalid_body", err.Error())
		return
	}
	in.Id = id
	if err := a.inboundService.UpdateInbound(&in); err != nil {
		BadRequest(c, mapInboundErr(err, "update_failed"), err.Error())
		return
	}
	a.xrayService.SetToNeedRestart()
	OK(c, in)
}

func (a *V1Controller) deleteInbound(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	if err := a.inboundService.DelInbound(id); err != nil {
		Internal(c, "delete_failed", err)
		return
	}
	a.xrayService.SetToNeedRestart()
	NoContent(c)
}

func (a *V1Controller) resetInboundTraffic(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	in, err := a.inboundService.GetInbound(id)
	if err != nil {
		NotFound(c, "inbound_not_found", err.Error())
		return
	}
	in.Up = 0
	in.Down = 0
	if err := a.inboundService.UpdateInbound(in); err != nil {
		Internal(c, "reset_failed", err)
		return
	}
	OK(c, in)
}

// ---------- outbound CRUD ----------
//
// Thin wrappers over OutboundService. xray must be reloaded after any
// mutation: outbounds and the routing rules that bind them are stitched
// into the live config in XrayService.GetXrayConfig — without
// SetToNeedRestart the change sits in DB but not in the running process.

func (a *V1Controller) listOutbounds(c *gin.Context) {
	rows, err := a.outboundService.List()
	if err != nil {
		Internal(c, "db_error", err)
		return
	}
	OK(c, rows)
}

func (a *V1Controller) getOutbound(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	r, err := a.outboundService.Get(id)
	if err != nil {
		if errors.Is(err, service.ErrOutboundNotFound) {
			NotFound(c, "outbound_not_found", err.Error())
			return
		}
		Internal(c, "db_error", err)
		return
	}
	OK(c, r)
}

func (a *V1Controller) createOutbound(c *gin.Context) {
	var r model.Outbound
	if err := c.ShouldBindJSON(&r); err != nil {
		BadRequest(c, "invalid_body", err.Error())
		return
	}
	if err := a.outboundService.Add(&r); err != nil {
		BadRequest(c, mapOutboundErr(err, "create_failed"), err.Error())
		return
	}
	a.xrayService.SetToNeedRestart()
	Created(c, r)
}

func (a *V1Controller) updateOutbound(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	var r model.Outbound
	if err := c.ShouldBindJSON(&r); err != nil {
		BadRequest(c, "invalid_body", err.Error())
		return
	}
	r.Id = id
	if err := a.outboundService.Update(&r); err != nil {
		// 404 vs 400: NotFound has its own status; everything else is the
		// caller's fault (tag mismatch, bad JSON, reserved tag, …).
		if errors.Is(err, service.ErrOutboundNotFound) {
			NotFound(c, "outbound_not_found", err.Error())
			return
		}
		BadRequest(c, mapOutboundErr(err, "update_failed"), err.Error())
		return
	}
	a.xrayService.SetToNeedRestart()
	OK(c, r)
}

func (a *V1Controller) deleteOutbound(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	if err := a.outboundService.Delete(id); err != nil {
		if errors.Is(err, service.ErrOutboundNotFound) {
			NotFound(c, "outbound_not_found", err.Error())
			return
		}
		Internal(c, "delete_failed", err)
		return
	}
	a.xrayService.SetToNeedRestart()
	NoContent(c)
}

func (a *V1Controller) bulkDeleteOutbounds(c *gin.Context) {
	var req struct {
		IDs []int `json:"ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "invalid_body", err.Error())
		return
	}
	if len(req.IDs) > maxBulkIDs {
		BadRequest(c, "too_many_items", "bulk size exceeds limit (max 1000)")
		return
	}
	n, err := a.outboundService.DeleteMany(req.IDs)
	if err != nil {
		Internal(c, "delete_failed", err)
		return
	}
	a.xrayService.SetToNeedRestart()
	OK(c, gin.H{"affected": n})
}

// setOutboundEnable —— 单点 toggle,避免业务系统为了开/关一个出站
// 必须 PUT 完整 settings/streamSettings。和 PATCH /inbounds/:id/enable 对称。
func (a *V1Controller) setOutboundEnable(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	var body struct {
		Enable *bool `json:"enable"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Enable == nil {
		BadRequest(c, "invalid_body", "enable (boolean) is required")
		return
	}
	if err := a.outboundService.SetEnable(id, *body.Enable); err != nil {
		if errors.Is(err, service.ErrOutboundNotFound) {
			NotFound(c, "outbound_not_found", err.Error())
			return
		}
		Internal(c, "update_failed", err)
		return
	}
	a.xrayService.SetToNeedRestart()
	OK(c, gin.H{"id": id, "enable": *body.Enable})
}

// testOutbound 跑 OutboundService.TestConnectivity:5s TCP 拨号 + 可选
// 5s TLS 握手。Reality 协议只测 TCP(伪装层会让真实 TLS 握手脱节)。
// 不副作用 — 不写 DB,不动 xray,可重复跑。
func (a *V1Controller) testOutbound(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	res, err := a.outboundService.TestConnectivity(id)
	if err != nil {
		if errors.Is(err, service.ErrOutboundNotFound) {
			NotFound(c, "outbound_not_found", err.Error())
			return
		}
		Internal(c, "test_failed", err)
		return
	}
	OK(c, res)
}

func (a *V1Controller) dbTraffic(c *gin.Context) {
	items, err := a.inboundService.GetAllInboundsCtx(c.Request.Context())
	if err != nil {
		Internal(c, "db_error", err)
		return
	}
	out := make([]gin.H, 0, len(items))
	for _, in := range items {
		out = append(out, gin.H{
			"id":     in.Id,
			"tag":    in.Tag,
			"port":   in.Port,
			"up":     in.Up,
			"down":   in.Down,
			"total":  in.Total,
			"enable": in.Enable,
		})
	}
	OK(c, out)
}

// liveTraffic 拉 xray 实时流量(从 stats gRPC API 读)。默认 reset=true
// 跟 panel 内部 XrayTrafficJob 同语义 —— 取出来后清零,下次拿到的是 delta。
//
// **v2.5.2+ 新增 ?reset=false** —— 只读快照不清零,给外部监控 / 主控想
// 高频轮询用。如果跟 XrayTrafficJob 抢消费(高频 reset=true 调这条),会
// "截胡"job 的 delta,DB 累计漏算;reset=false 完全不动 xray 计数器,job
// 仍正常累计。代价:连续 Peek 的值是"自上次 reset 起的累计",不是"两次
// Peek 之间的 delta",外部要算 delta 自己记上次值再减。
func (a *V1Controller) liveTraffic(c *gin.Context) {
	reset := !(c.Query("reset") == "false" || c.Query("reset") == "0")
	var items []*xray.Traffic
	var err error
	if reset {
		items, err = a.xrayService.GetXrayTraffic()
	} else {
		items, err = a.xrayService.PeekXrayTraffic()
	}
	if err != nil {
		Internal(c, "xray_traffic_failed", err)
		return
	}
	OK(c, items)
}

// listOnlineIps 返回所有入站当前活跃的源 IP（按 inbound tag 分组）。数据来源
// 是 Xray access log 的 60s 滑动窗口，纯内存，面板重启即清。业务系统通过
// len(ips[tag]) 判定该入站当前在线设备数。
func (a *V1Controller) listOnlineIps(c *gin.Context) {
	OK(c, service.GetOnlineIPService().GetAllIPs())
}

func (a *V1Controller) listOnlineIpsByTag(c *gin.Context) {
	tag := c.Param("tag")
	if tag == "" {
		BadRequest(c, "invalid_tag", "tag is required")
		return
	}
	OK(c, service.GetOnlineIPService().GetIPs(tag))
}

// listOnlineIpsByEmail 返回 email → 当前活跃 IP 列表的映射。底层是 access.log
// 60s 滑窗,与 /online-ips/:tag 共用同一份内存状态。业务系统拼"某入站下哪个
// client 在线、来自哪些 IP"时不必再 join /online-ips/:tag + clients[],直接
// 查这条即可。
//
// **v2.5.2+ 新增 ?detailed=1** —— 切换到详尽视图,每个 email 返
// {ips, inboundTag, lastSeenAt}(unix 毫秒)。前端 / 业务系统拼"客户挂在
// 哪条入站、最后活动时间多久"用,不需要再 join /online-ips/:tag + clients[]。
// 默认形态(无 detailed 参数)不变,保持向后兼容。
func (a *V1Controller) listOnlineIpsByEmail(c *gin.Context) {
	if c.Query("detailed") == "1" {
		OK(c, service.GetOnlineIPService().GetIPsByEmailDetailed())
		return
	}
	OK(c, service.GetOnlineIPService().GetIPsByEmail())
}

// ---------- block rules ----------

func (a *V1Controller) listBlockRules(c *gin.Context) {
	rows, err := a.blockRuleService.List()
	if err != nil {
		Internal(c, "db_error", err)
		return
	}
	OK(c, rows)
}

func (a *V1Controller) getBlockRule(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	r, err := a.blockRuleService.Get(id)
	if err != nil {
		if errors.Is(err, service.ErrBlockRuleNotFound) {
			NotFound(c, "block_rule_not_found", err.Error())
			return
		}
		Internal(c, "db_error", err)
		return
	}
	OK(c, r)
}

func (a *V1Controller) listBlockRulePresets(c *gin.Context) {
	OK(c, a.blockRuleService.ListPresets())
}

func (a *V1Controller) createBlockRule(c *gin.Context) {
	var r model.BlockRule
	if err := c.ShouldBindJSON(&r); err != nil {
		BadRequest(c, "invalid_body", err.Error())
		return
	}
	if err := a.blockRuleService.Add(&r); err != nil {
		BadRequest(c, "create_failed", err.Error())
		return
	}
	a.xrayService.SetToNeedRestart()
	Created(c, r)
}

func (a *V1Controller) updateBlockRule(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	var r model.BlockRule
	if err := c.ShouldBindJSON(&r); err != nil {
		BadRequest(c, "invalid_body", err.Error())
		return
	}
	r.Id = id
	if err := a.blockRuleService.Update(&r); err != nil {
		if errors.Is(err, service.ErrBlockRuleNotFound) {
			NotFound(c, "block_rule_not_found", err.Error())
			return
		}
		BadRequest(c, "update_failed", err.Error())
		return
	}
	a.xrayService.SetToNeedRestart()
	OK(c, r)
}

func (a *V1Controller) setBlockRuleEnable(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	var body struct {
		Enable bool `json:"enable"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, "invalid_body", err.Error())
		return
	}
	if err := a.blockRuleService.SetEnable(id, body.Enable); err != nil {
		if errors.Is(err, service.ErrBlockRuleNotFound) {
			NotFound(c, "block_rule_not_found", err.Error())
			return
		}
		BadRequest(c, "toggle_failed", err.Error())
		return
	}
	a.xrayService.SetToNeedRestart()
	OK(c, gin.H{"id": id, "enable": body.Enable})
}

func (a *V1Controller) deleteBlockRule(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	if err := a.blockRuleService.Delete(id); err != nil {
		if errors.Is(err, service.ErrBlockRuleNotFound) {
			NotFound(c, "block_rule_not_found", err.Error())
			return
		}
		BadRequest(c, "delete_failed", err.Error())
		return
	}
	a.xrayService.SetToNeedRestart()
	OK(c, gin.H{"id": id, "deleted": true})
}

func (a *V1Controller) applyBlockRulePreset(c *gin.Context) {
	var body struct {
		Key        string `json:"key"`
		InboundTag string `json:"inboundTag"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, "invalid_body", err.Error())
		return
	}
	if body.Key == "" {
		BadRequest(c, "invalid_key", "key is required")
		return
	}
	if err := a.blockRuleService.ApplyPreset(body.Key, body.InboundTag); err != nil {
		BadRequest(c, "apply_failed", err.Error())
		return
	}
	a.xrayService.SetToNeedRestart()
	OK(c, gin.H{"applied": body.Key})
}

func (a *V1Controller) getSettings(c *gin.Context) {
	all, err := a.settingService.GetAllSetting()
	if err != nil {
		Internal(c, "db_error", err)
		return
	}
	OK(c, all)
}

func (a *V1Controller) patchSettings(c *gin.Context) {
	current, err := a.settingService.GetAllSetting()
	if err != nil {
		Internal(c, "db_error", err)
		return
	}
	if err := c.ShouldBindJSON(current); err != nil {
		BadRequest(c, "invalid_body", err.Error())
		return
	}
	if err := a.settingService.UpdateAllSetting(current); err != nil {
		BadRequest(c, "update_failed", err.Error())
		return
	}
	OK(c, current)
}

// rotateAPIToken returns the freshly minted token exactly once. The caller is
// expected to persist it immediately — a subsequent GET will not return it
// again and there is no way to recover it without rotating.
func (a *V1Controller) rotateAPIToken(c *gin.Context) {
	// We can't call EnsureAPIToken here because that returns the existing
	// value if any. Generate explicitly.
	settings := &a.settingService
	token, _, err := generateAndStore(settings)
	if err != nil {
		Internal(c, "rotate_failed", err)
		return
	}
	OK(c, gin.H{"token": token})
}

// generateAndStore mints a 48-char token and overwrites the stored value.
func generateAndStore(s *service.SettingService) (string, bool, error) {
	// SetAPIToken("") clears so EnsureAPIToken regenerates with random.Seq(48).
	if err := s.SetAPIToken(""); err != nil {
		return "", false, err
	}
	return s.EnsureAPIToken()
}

// ---------- token CRUD ----------

type createTokenReq struct {
	Name       string `json:"name" binding:"required"`
	Scope      string `json:"scope"`      // optional; defaults to "admin"
	TTLSeconds int64  `json:"ttlSeconds"` // optional; 0 = never expires
}

type tokenView struct {
	Id         int    `json:"id"`
	Name       string `json:"name"`
	CreatedAt  int64  `json:"createdAt"`
	LastUsedAt int64  `json:"lastUsedAt"`
	Revoked    bool   `json:"revoked"`
	Scope      string `json:"scope"`
	ExpiresAt  int64  `json:"expiresAt"`
}

func tokenToView(t *model.APIToken) tokenView {
	scope := t.Scope
	if scope == "" {
		scope = service.ScopeAdmin
	}
	return tokenView{
		Id:         t.Id,
		Name:       t.Name,
		CreatedAt:  t.CreatedAt,
		LastUsedAt: t.LastUsedAt,
		Revoked:    t.Revoked,
		Scope:      scope,
		ExpiresAt:  t.ExpiresAt,
	}
}

func (a *V1Controller) listTokens(c *gin.Context) {
	tokens, err := a.tokenService.ListTokens()
	if err != nil {
		Internal(c, "db_error", err)
		return
	}
	views := make([]tokenView, 0, len(tokens))
	for _, t := range tokens {
		views = append(views, tokenToView(t))
	}
	OK(c, views)
}

func (a *V1Controller) createToken(c *gin.Context) {
	var req createTokenReq
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "invalid_body", err.Error())
		return
	}
	ttl := time.Duration(req.TTLSeconds) * time.Second
	t, err := a.tokenService.CreateToken(req.Name, req.Scope, ttl)
	if err != nil {
		BadRequest(c, "create_failed", err.Error())
		return
	}
	// The plaintext is surfaced exactly once here; the DB only stores
	// its SHA256, so there is no recovery path if the operator loses it.
	Created(c, gin.H{
		"id":        t.Row.Id,
		"name":      t.Row.Name,
		"token":     t.Plaintext,
		"scope":     t.Row.Scope,
		"createdAt": t.Row.CreatedAt,
		"expiresAt": t.Row.ExpiresAt,
	})
}

func (a *V1Controller) renameToken(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	var req createTokenReq
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "invalid_body", err.Error())
		return
	}
	t, err := a.tokenService.RenameToken(id, req.Name)
	if err != nil {
		if err == service.ErrTokenNotFound {
			NotFound(c, "token_not_found", err.Error())
			return
		}
		BadRequest(c, "rename_failed", err.Error())
		return
	}
	OK(c, tokenToView(t))
}

func (a *V1Controller) revokeToken(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	if err := a.tokenService.RevokeToken(id); err != nil {
		if err == service.ErrTokenNotFound {
			NotFound(c, "token_not_found", err.Error())
			return
		}
		Internal(c, "revoke_failed", err)
		return
	}
	OK(c, gin.H{"revoked": true})
}

func (a *V1Controller) deleteToken(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	if err := a.tokenService.DeleteToken(id); err != nil {
		if err == service.ErrTokenNotFound {
			NotFound(c, "token_not_found", err.Error())
			return
		}
		Internal(c, "delete_failed", err)
		return
	}
	NoContent(c)
}

// ---------- enable toggle + bulk inbounds ----------

// maxBulkIDs caps how many inbounds a single bulk call may touch.
// Anything larger is almost certainly a misuse / DoS, and unbounded ids
// arrays can blow up memory and SQLite's IN-list compilation.
const maxBulkIDs = 1000

type bulkIDReq struct {
	IDs    []int `json:"ids" binding:"required"`
	Enable bool  `json:"enable"`
}

func (a *V1Controller) setInboundEnable(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	var body struct {
		Enable bool `json:"enable"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, "invalid_body", err.Error())
		return
	}
	in, err := a.inboundService.GetInbound(id)
	if err != nil {
		NotFound(c, "inbound_not_found", err.Error())
		return
	}
	in.Enable = body.Enable
	if err := a.inboundService.UpdateInbound(in); err != nil {
		BadRequest(c, mapInboundErr(err, "update_failed"), err.Error())
		return
	}
	a.xrayService.SetToNeedRestart()
	OK(c, in)
}

func (a *V1Controller) bulkCreateInbounds(c *gin.Context) {
	var items []*model.Inbound
	if err := c.ShouldBindJSON(&items); err != nil {
		BadRequest(c, "invalid_body", err.Error())
		return
	}
	if len(items) > maxBulkIDs {
		BadRequest(c, "too_many_items", "bulk size exceeds limit (max 1000)")
		return
	}
	user, _ := a.userService.GetFirstUser()
	if user != nil {
		for _, in := range items {
			in.UserId = user.Id
		}
	}
	if err := a.inboundService.AddInbounds(items); err != nil {
		BadRequest(c, mapInboundErr(err, "create_failed"), err.Error())
		return
	}
	a.xrayService.SetToNeedRestart()
	Created(c, gin.H{"created": len(items), "items": items})
}

func (a *V1Controller) bulkSetEnable(c *gin.Context) {
	var req bulkIDReq
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "invalid_body", err.Error())
		return
	}
	if len(req.IDs) > maxBulkIDs {
		BadRequest(c, "too_many_items", "bulk size exceeds limit (max 1000)")
		return
	}
	n, err := a.inboundService.SetEnableMany(req.IDs, req.Enable)
	if err != nil {
		// dry-run rejection arrives as ErrXrayConfigInvalid; surface that
		// distinct code so business systems can switch on it instead of
		// re-parsing the message string.
		BadRequest(c, mapInboundErr(err, "update_failed"), err.Error())
		return
	}
	a.xrayService.SetToNeedRestart()
	OK(c, gin.H{"affected": n})
}

func (a *V1Controller) bulkDelete(c *gin.Context) {
	var req struct {
		IDs []int `json:"ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "invalid_body", err.Error())
		return
	}
	if len(req.IDs) > maxBulkIDs {
		BadRequest(c, "too_many_items", "bulk size exceeds limit (max 1000)")
		return
	}
	n, err := a.inboundService.DeleteMany(req.IDs)
	if err != nil {
		Internal(c, "delete_failed", err)
		return
	}
	a.xrayService.SetToNeedRestart()
	OK(c, gin.H{"affected": n})
}

// bulkResetTraffic resets up/down counters. To avoid an accidental "wipe
// every node's traffic" via empty body, the all-rows path requires an
// explicit {"all": true} flag. {"ids": [...]} stays untouched.
func (a *V1Controller) bulkResetTraffic(c *gin.Context) {
	var req struct {
		IDs []int `json:"ids"`
		All bool  `json:"all"`
	}
	_ = c.ShouldBindJSON(&req)
	if len(req.IDs) > maxBulkIDs {
		BadRequest(c, "too_many_items", "bulk size exceeds limit (max 1000)")
		return
	}
	if len(req.IDs) == 0 && !req.All {
		BadRequest(c, "confirmation_required",
			`pass {"ids": [...]} to scope or {"all": true} to reset every inbound`)
		return
	}
	n, err := a.inboundService.ResetTrafficMany(req.IDs)
	if err != nil {
		Internal(c, "reset_failed", err)
		return
	}
	OK(c, gin.H{"affected": n})
}

// ---------- client management ----------

func (a *V1Controller) listClients(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	clients, err := a.clientService.ListClients(id)
	if err != nil {
		NotFound(c, "inbound_not_found", err.Error())
		return
	}
	OK(c, clients)
}

func (a *V1Controller) addClient(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	var client map[string]any
	if err := c.ShouldBindJSON(&client); err != nil {
		BadRequest(c, "invalid_body", err.Error())
		return
	}
	in, err := a.clientService.AddClient(id, client)
	if err != nil {
		BadRequest(c, mapClientErr(err), err.Error())
		return
	}
	Created(c, in)
}

func (a *V1Controller) updateClient(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	identifier := c.Param("identifier")
	var client map[string]any
	if err := c.ShouldBindJSON(&client); err != nil {
		BadRequest(c, "invalid_body", err.Error())
		return
	}
	in, err := a.clientService.UpdateClient(id, identifier, client)
	if err != nil {
		BadRequest(c, mapClientErr(err), err.Error())
		return
	}
	OK(c, in)
}

func (a *V1Controller) deleteClient(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	identifier := c.Param("identifier")
	if _, err := a.clientService.DeleteClient(id, identifier); err != nil {
		BadRequest(c, mapClientErr(err), err.Error())
		return
	}
	NoContent(c)
}

func mapClientErr(err error) string {
	switch err {
	case service.ErrClientIdentifierRequired:
		return "client_identifier_required"
	case service.ErrClientNotFound:
		return "client_not_found"
	case service.ErrClientDuplicate:
		return "client_duplicate"
	case service.ErrUnsupportedProtocol:
		return "unsupported_protocol"
	default:
		return "client_op_failed"
	}
}

// ---------- per-client traffic (v1.1.0) ----------
// API 层是业务系统对接面,所以错误码 / 入参语义都以"email 全局唯一"为
// 中心,而不是 inbound + identifier 的二级路径。

func (a *V1Controller) listClientTraffics(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	rows, err := a.clientTrafficService.ListByInbound(id)
	if err != nil {
		Internal(c, "db_error", err)
		return
	}
	OK(c, rows)
}

func (a *V1Controller) getClientTraffic(c *gin.Context) {
	email := c.Param("email")
	row, err := a.clientTrafficService.GetByEmail(email)
	if err != nil {
		if errors.Is(err, service.ErrClientTrafficNotFound) {
			NotFound(c, "client_traffic_not_found", "no client_traffics row for email "+email)
			return
		}
		Internal(c, "db_error", err)
		return
	}
	OK(c, row)
}

func (a *V1Controller) resetClientTraffic(c *gin.Context) {
	email := c.Param("email")
	if err := a.clientTrafficService.ResetTraffic(email); err != nil {
		if errors.Is(err, service.ErrClientTrafficNotFound) {
			NotFound(c, "client_traffic_not_found", err.Error())
			return
		}
		Internal(c, "reset_failed", err)
		return
	}
	OK(c, gin.H{"reset": true, "email": email})
}

func (a *V1Controller) patchClientLimits(c *gin.Context) {
	email := c.Param("email")
	var body service.SetLimitsParams
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, "invalid_body", err.Error())
		return
	}
	if err := a.clientTrafficService.SetLimits(email, body); err != nil {
		if errors.Is(err, service.ErrClientTrafficNotFound) {
			NotFound(c, "client_traffic_not_found", err.Error())
			return
		}
		Internal(c, "update_failed", err)
		return
	}
	// SetLimits 只改 client_traffics,xray 自身配置(settings.clients[])
	// 不变 — 但 enable=false 需要 xray reload 才能踢下线,所以无脑标 dirty。
	a.xrayService.SetToNeedRestart()
	row, _ := a.clientTrafficService.GetByEmail(email)
	OK(c, row)
}

// setClientEnable — 业务侧最高频的 client 操作就是"暂停 / 恢复",所以单独
// 给一条对称路由(参照 PATCH /inbounds/:id/enable),避免用户每次借道
// /limits 端点 — 后者要求知道 SetLimitsParams 形状,且语义上"改配额"和
// "纯启停"是两件事。后端实现仍然复用 SetLimits(只填 Enable 指针),不引
// 入新 service 方法。
func (a *V1Controller) setClientEnable(c *gin.Context) {
	email := c.Param("email")
	var body struct {
		Enable bool `json:"enable"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, "invalid_body", err.Error())
		return
	}
	enable := body.Enable
	if err := a.clientTrafficService.SetLimits(email, service.SetLimitsParams{Enable: &enable}); err != nil {
		if errors.Is(err, service.ErrClientTrafficNotFound) {
			NotFound(c, "client_traffic_not_found", err.Error())
			return
		}
		Internal(c, "update_failed", err)
		return
	}
	a.xrayService.SetToNeedRestart()
	OK(c, gin.H{"email": email, "enable": enable})
}

// disableExpiredClients —— 业务系统月初对账 / 定时任务后调,扫描所有
// 已到期但还 enable 的 client,批量置为 enable=false 并触发 xray reload。
func (a *V1Controller) disableExpiredClients(c *gin.Context) {
	disabled, err := a.clientTrafficService.DisableExpired(time.Now().UnixMilli())
	if err != nil {
		Internal(c, "update_failed", err)
		return
	}
	if len(disabled) > 0 {
		a.xrayService.SetToNeedRestart()
	}
	OK(c, gin.H{"disabled": disabled, "count": len(disabled)})
}

// disableInvalidInbounds —— 入站层面的过期/超额一键回收。扫整张表,
// 把同时满足 (enable=true) 且 (流量超限 OR 到期) 的入站置为 enable=false。
// 跟 disableExpiredClients 互补:client-level 关单个用户,inbound-level 关
// 整条入站(整端口下线)。返回受影响行数。
func (a *V1Controller) disableInvalidInbounds(c *gin.Context) {
	n, err := a.inboundService.DisableInvalidInbounds()
	if err != nil {
		Internal(c, "update_failed", err)
		return
	}
	if n > 0 {
		a.xrayService.SetToNeedRestart()
	}
	OK(c, gin.H{"affected": n})
}

// ---------- xray config / template / logs ----------

func (a *V1Controller) xrayEffectiveConfig(c *gin.Context) {
	cfg, err := a.xrayService.GetEffectiveConfig()
	if err != nil {
		Internal(c, "xray_config_failed", err)
		return
	}
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.String(200, cfg)
}

// xrayLogs 默认返 xray subprocess 的 stdout/stderr 缓冲(进程 ring buffer,
// 重启即清),覆盖 xray 自身的启动/警告/错误信息。
//
// **v2.5.2+ 新增 ?kind=access|error|all**(默认 all = 沿用旧行为):
//   - kind=error / 缺省 / kind=all → 旧 stderr 缓冲(进程内存)
//   - kind=access                  → bin/access.log 末尾 100 行
//     (xray 写文件,OnlineIPService 周期 truncate 防爆盘)
//
// 排查"客户连不上"看 access(谁在 connect、走哪条 inbound、email),"xray
// 自身报错"看 error,两路独立。响应里加 kind 字段告诉调用方拿到的是哪一路。
func (a *V1Controller) xrayLogs(c *gin.Context) {
	kind := c.Query("kind")
	switch kind {
	case "access":
		body, err := a.xrayService.GetRecentAccessLog(100)
		if err != nil {
			Internal(c, "access_log_read_failed", err)
			return
		}
		OK(c, gin.H{"logs": body, "kind": "access"})
	case "", "all", "error":
		// 默认 / all / error 都走 stderr 缓冲。error 跟 all 同义 —— 当前实现
		// xray 模板没单独配 error log file,error 写进 stderr 跟一般日志合流,
		// 单分一路意义不大;留这个名字给未来真支持分流时无缝替换。
		out := "all"
		if kind != "" {
			out = kind
		}
		OK(c, gin.H{"logs": a.xrayService.GetRecentLogs(), "kind": out})
	default:
		BadRequest(c, "invalid_kind", "kind must be access, error, or all")
	}
}

// xrayTemplateGet 返 xray config 模板。**v2.5.2+ 起改用 {data: <obj>} envelope**,
// 跟其他 v1 端点一致;此前是 raw JSON text 不带壳,对接的人要写两套解析。
//
// 模板内容是 xray 接受的 JSON,GET 出来直接是 object;PUT 仍接收 raw 字节
// (PUT 走 c.GetRawData,避免 json.Decode 改字段顺序)。GET → 改 → PUT 的
// 流程 = 解 obj → 改字段 → JSON.stringify → PUT。
func (a *V1Controller) xrayTemplateGet(c *gin.Context) {
	tpl, err := a.settingService.GetXrayConfigTemplate()
	if err != nil {
		Internal(c, "db_error", err)
		return
	}
	var parsed any
	if err := json.Unmarshal([]byte(tpl), &parsed); err != nil {
		// 模板被损坏(用户手动改 DB / 旧版本 schema 不兼容)→ 仍把原文返回,
		// 但加个 parseError 字段告诉调用方"格式有问题",免得静默丢回 200
		// 让对方去 JSON.parse 一团糟。
		OK(c, gin.H{"template": tpl, "parseError": err.Error()})
		return
	}
	OK(c, parsed)
}

func (a *V1Controller) xrayTemplatePut(c *gin.Context) {
	body, err := c.GetRawData()
	if err != nil || len(body) == 0 {
		BadRequest(c, "invalid_body", "expected raw JSON body")
		return
	}
	// Round-trip through json.Decode to validate before persisting.
	var probe any
	if err := json.Unmarshal(body, &probe); err != nil {
		BadRequest(c, "invalid_json", err.Error())
		return
	}
	all, err := a.settingService.GetAllSetting()
	if err != nil {
		Internal(c, "db_error", err)
		return
	}
	all.XrayTemplateConfig = string(body)
	if err := a.settingService.UpdateAllSetting(all); err != nil {
		BadRequest(c, "update_failed", err.Error())
		return
	}
	a.xrayService.SetToNeedRestart()
	OK(c, gin.H{"updated": true})
}

// ---------- share / subscription ----------

// validateShareHost enforces a syntactic check on the user-supplied
// ?host=... parameter and, if the operator configured a comma-separated
// allow-list in settings.subAllowedHosts, also enforces that. Returns
// the validated host or "" + a reason on rejection. Without a whitelist
// any syntactically valid host is accepted, but malformed input (CRLF,
// scheme, query-string, length-bomb) is always rejected.
//
// Why we still need this with token auth: a stolen subscription-scope
// token is the easiest way to mint a "valid-looking" subscription URL
// pointing at attacker.example. Constraining host to known names is a
// cheap way to neutralize that.
func (a *V1Controller) validateShareHost(host string) (string, string) {
	cleaned, reason := service.ValidateShareHostSyntactic(host)
	if reason != "" {
		return "", reason
	}
	// Optional whitelist via setting. Empty / missing = "any valid host".
	if allowed, _ := a.settingService.GetSubAllowedHosts(); allowed != "" {
		want := strings.ToLower(cleaned)
		for _, h := range strings.Split(allowed, ",") {
			if strings.ToLower(strings.TrimSpace(h)) == want {
				return cleaned, ""
			}
		}
		return "", "host not in subAllowedHosts whitelist"
	}
	return cleaned, ""
}

// resolveShareHost 决定 share/QR 类 endpoint 用哪个 host 拼链接。优先级:
//
//  1. ?host= 显式参数 —— 业务系统拼跨节点订阅 / 一份 token 多个出口时用
//  2. settings.nodeAddress —— 操作员配的"对外节点地址",CF 橙云 / NAT 反代
//     场景必须用,否则链接 host 是面板的代理域,客户端打非标端口超时
//  3. c.Request.Host —— 老行为兜底,兼容没配 nodeAddress 的旧部署
//
// 跟 panel 路径 (api_panel.go::inboundLinks) 完全一致,消除"v1 必须显式传
// host 否则 400"这条陷阱。最终结果仍走 validateShareHost(语法 + 白名单)。
func (a *V1Controller) resolveShareHost(c *gin.Context) (string, string) {
	host := strings.TrimSpace(c.Query("host"))
	if host == "" {
		host = a.settingService.GetNodeAddress()
	}
	if host == "" {
		host = c.Request.Host
		if i := strings.IndexByte(host, ':'); i >= 0 {
			host = host[:i]
		}
	}
	return a.validateShareHost(host)
}

func (a *V1Controller) inboundLinks(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	host, reason := a.resolveShareHost(c)
	if host == "" {
		BadRequest(c, "invalid_host", reason)
		return
	}
	links, err := a.shareService.LinksForInboundCtx(c.Request.Context(), id, host)
	if err != nil {
		BadRequest(c, mapClientErr(err), err.Error())
		return
	}
	OK(c, links)
}

// inboundLinksByEmail 返回该入站下每个 email 客户端的 {link, qrcode} 映射,
// 给业务系统一次拉齐用。host 走 resolveShareHost 兜底。qrcode 是完整 PNG
// data URL(data:image/png;base64,...),可直接 <img src> 或塞响应里下发。
func (a *V1Controller) inboundLinksByEmail(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	host, reason := a.resolveShareHost(c)
	if host == "" {
		BadRequest(c, "invalid_host", reason)
		return
	}
	out, err := a.shareService.LinksByEmailWithQRCtx(c.Request.Context(), id, host)
	if err != nil {
		BadRequest(c, mapClientErr(err), err.Error())
		return
	}
	OK(c, out)
}

// clientShare 单个 email 客户端的 {link, qrcode} 查询。/links/by-email 的
// 单点版本,免拉整张表 + 业务系统按 email 取一条更直观。404 当且仅当
// 入站存在但该 email 不在 settings.clients[](或者协议不支持 share link)。
func (a *V1Controller) clientShare(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	email := strings.TrimSpace(c.Param("email"))
	if email == "" {
		BadRequest(c, "missing_email", "email path parameter required")
		return
	}
	host, reason := a.resolveShareHost(c)
	if host == "" {
		BadRequest(c, "invalid_host", reason)
		return
	}
	all, err := a.shareService.LinksByEmailWithQRCtx(c.Request.Context(), id, host)
	if err != nil {
		BadRequest(c, mapClientErr(err), err.Error())
		return
	}
	share, found := all[email]
	if !found {
		NotFound(c, "client_not_found", "email not in this inbound or protocol has no share link")
		return
	}
	OK(c, share)
}

func (a *V1Controller) subscriptionOne(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	host, reason := a.validateShareHost(c.Query("host"))
	if host == "" {
		BadRequest(c, "invalid_host", reason)
		return
	}
	sub, err := a.shareService.SubscriptionForInboundCtx(c.Request.Context(), id, host)
	if err != nil {
		BadRequest(c, mapClientErr(err), err.Error())
		return
	}
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.String(200, sub)
}

func (a *V1Controller) subscriptionAll(c *gin.Context) {
	host, reason := a.validateShareHost(c.Query("host"))
	if host == "" {
		BadRequest(c, "invalid_host", reason)
		return
	}
	sub, err := a.shareService.SubscriptionForAllCtx(c.Request.Context(), host)
	if err != nil {
		Internal(c, "subscription_failed", err)
		return
	}
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.String(200, sub)
}

// ---------- certs ----------

type uploadCertReq struct {
	Name string `json:"name" binding:"required"`
	Cert string `json:"cert" binding:"required"`
	Key  string `json:"key"  binding:"required"`
}

func (a *V1Controller) listCerts(c *gin.Context) {
	items, err := a.certService.List()
	if err != nil {
		Internal(c, "cert_list_failed", err)
		return
	}
	OK(c, items)
}

func (a *V1Controller) uploadCert(c *gin.Context) {
	var req uploadCertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		BadRequest(c, "invalid_body", err.Error())
		return
	}
	entry, err := a.certService.Save(req.Name, req.Cert, req.Key)
	if err != nil {
		BadRequest(c, "cert_save_failed", err.Error())
		return
	}
	Created(c, entry)
}

func (a *V1Controller) deleteCert(c *gin.Context) {
	name := c.Param("name")
	if err := a.certService.Delete(name); err != nil {
		BadRequest(c, "cert_delete_failed", err.Error())
		return
	}
	NoContent(c)
}

// ---------- self-update ----------

func (a *V1Controller) updateCheck(c *gin.Context) {
	out, err := a.updateService.CheckLatest()
	if err != nil {
		Internal(c, "update_check_failed", err)
		return
	}
	OK(c, out)
}

func (a *V1Controller) updateApply(c *gin.Context) {
	var body struct {
		Version string `json:"version"`
	}
	_ = c.ShouldBindJSON(&body)
	out, err := a.updateService.ApplyLatest(body.Version)
	if err != nil {
		Internal(c, "update_apply_failed", err)
		return
	}
	OK(c, out)
}

// ---------- access logs ----------

func (a *V1Controller) listAccessLogs(c *gin.Context) {
	q := service.APILogQuery{
		Path:      c.Query("path"),
		Method:    c.Query("method"),
		TokenName: c.Query("token"),
	}
	if v := c.Query("status"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			q.Status = n
		}
	}
	if v := c.Query("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			q.Page = n
		}
	}
	if v := c.Query("size"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			q.Size = n
		}
	}
	items, total, err := a.apiLogService.Query(q)
	if err != nil {
		Internal(c, "db_error", err)
		return
	}
	c.JSON(200, gin.H{
		"data": items,
		"meta": gin.H{"total": total, "page": q.Page, "size": q.Size},
	})
}

func (a *V1Controller) purgeAccessLogs(c *gin.Context) {
	n, err := a.apiLogService.Purge()
	if err != nil {
		Internal(c, "purge_failed", err)
		return
	}
	OK(c, gin.H{"deleted": n})
}

// ---------- magic-link login ----------

type magicTokenReq struct {
	TtlSeconds int    `json:"ttlSeconds"`
	Note       string `json:"note"`
}

func (a *V1Controller) createMagicToken(c *gin.Context) {
	var req magicTokenReq
	_ = c.ShouldBindJSON(&req) // empty body is allowed
	created, err := a.magicService.CreateMagicToken(time.Duration(req.TtlSeconds)*time.Second, req.Note)
	if err != nil {
		Internal(c, "magic_token_failed", err)
		return
	}
	// Plaintext is returned exactly once; the DB row carries SHA256.
	// Token in URL fragment, NOT path — keeps it out of access logs,
	// proxy logs, and Referer. See web/web.go::handleMagicConsume.
	basePath := c.GetString("base_path")
	if basePath == "" {
		basePath = "/"
	}
	relativeURL := basePath + "magic-login#tk=" + created.Plaintext
	Created(c, gin.H{
		"token":       created.Plaintext,
		"createdAt":   created.Row.CreatedAt,
		"expiresAt":   created.Row.ExpiresAt,
		"note":        created.Row.Note,
		"relativeUrl": relativeURL,
		"hint":        "prepend the panel's public origin (scheme + host[:port]) to relativeUrl",
	})
}

// ---------- system ----------

func (a *V1Controller) listeningPorts(c *gin.Context) {
	items, err := a.systemService.ListeningPorts()
	if err != nil {
		Internal(c, "system_query_failed", err)
		return
	}
	OK(c, items)
}

// checkPort answers "is port N already taken on this host?". Used by the
// business system before suggesting a port for a new inbound.
func (a *V1Controller) checkPort(c *gin.Context) {
	port, err := strconv.Atoi(c.Query("port"))
	if err != nil || port <= 0 || port > 65535 {
		BadRequest(c, "invalid_port", "?port=1..65535 required")
		return
	}
	items, err := a.systemService.ListeningPorts()
	if err != nil {
		Internal(c, "system_query_failed", err)
		return
	}
	for _, lp := range items {
		if int(lp.Port) == port {
			OK(c, gin.H{"available": false, "owner": lp})
			return
		}
	}
	OK(c, gin.H{"available": true})
}

// restartPanel sends SIGHUP to ourselves; main.go reloads the web server. The
// caller's connection drops immediately, so business systems must reconnect.
func (a *V1Controller) restartPanel(c *gin.Context) {
	OK(c, gin.H{"restarting": true})
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = sendSelfSIGHUP()
	}()
}

// ---------- helpers ----------

func parseIntParam(c *gin.Context, key string) (int, bool) {
	raw := c.Param(key)
	id, err := strconv.Atoi(raw)
	if err != nil {
		BadRequest(c, "invalid_id", "id must be an integer")
		return 0, false
	}
	return id, true
}

// silence unused import when entity becomes only referenced by other files.
var _ = entity.AllSetting{}
