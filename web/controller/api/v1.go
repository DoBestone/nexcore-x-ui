package api

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"nexcore-x-ui/database/model"
	"nexcore-x-ui/web/entity"
	"nexcore-x-ui/web/service"
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

// V1Controller exposes node-control endpoints under /api/v1.
type V1Controller struct {
	inboundService service.InboundService
	xrayService    service.XrayService
	serverService  service.ServerService
	settingService service.SettingService
	userService    service.UserService
	tokenService   service.APITokenService
	clientService  service.ClientService
	shareService   service.ShareService
	certService    service.CertService
	systemService  service.SystemService
	magicService   service.MagicTokenService
	apiLogService        service.APILogService
	updateService        service.UpdateService
	blockRuleService     service.BlockRuleService
	clientTrafficService service.ClientTrafficService
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
	ro.GET("/traffic", a.dbTraffic)
	ro.GET("/traffic/live", a.liveTraffic)
	ro.GET("/online-ips", a.listOnlineIps)
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
	api.POST("/clients/disable-expired", a.disableExpiredClients)

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
}

// ---------- handlers ----------

func (a *V1Controller) health(c *gin.Context) {
	OK(c, gin.H{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

func (a *V1Controller) serverStatus(c *gin.Context) {
	OK(c, a.serverService.GetStatus(nil))
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
			"id":         in.Id,
			"port":       in.Port,
			"protocol":   in.Protocol,
			"tag":        in.Tag,
			"remark":     in.Remark,
			"enable":     in.Enable,
			"listen":     in.Listen,
			"up":         in.Up,
			"down":       in.Down,
			"total":      in.Total,
			"expiryTime": in.ExpiryTime,
		})
	}
	return out
}

func (a *V1Controller) getInbound(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	item, err := a.inboundService.GetInbound(id)
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

func (a *V1Controller) dbTraffic(c *gin.Context) {
	items, err := a.inboundService.GetAllInbounds()
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

func (a *V1Controller) liveTraffic(c *gin.Context) {
	items, err := a.xrayService.GetXrayTraffic()
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

// ---------- block rules ----------

func (a *V1Controller) listBlockRules(c *gin.Context) {
	rows, err := a.blockRuleService.List()
	if err != nil {
		Internal(c, "db_error", err)
		return
	}
	OK(c, rows)
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

func (a *V1Controller) xrayLogs(c *gin.Context) {
	OK(c, gin.H{"logs": a.xrayService.GetRecentLogs()})
}

func (a *V1Controller) xrayTemplateGet(c *gin.Context) {
	tpl, err := a.settingService.GetXrayConfigTemplate()
	if err != nil {
		Internal(c, "db_error", err)
		return
	}
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.String(200, tpl)
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

func (a *V1Controller) inboundLinks(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	host, reason := a.validateShareHost(c.Query("host"))
	if host == "" {
		BadRequest(c, "invalid_host", reason)
		return
	}
	links, err := a.shareService.LinksForInbound(id, host)
	if err != nil {
		BadRequest(c, mapClientErr(err), err.Error())
		return
	}
	OK(c, links)
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
	sub, err := a.shareService.SubscriptionForInbound(id, host)
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
	sub, err := a.shareService.SubscriptionForAll(host)
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
