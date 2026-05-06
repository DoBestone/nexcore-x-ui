package api

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"nexcore-x-ui/database/model"
	"nexcore-x-ui/web/entity"
	"nexcore-x-ui/web/service"
)

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
	apiLogService  service.APILogService
	updateService  service.UpdateService
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

	api := g.Group("")
	api.Use(AuthMiddleware(&a.settingService, &a.tokenService))

	api.GET("/server/status", a.serverStatus)

	api.GET("/xray/status", a.xrayStatus)
	api.POST("/xray/restart", a.xrayRestart)
	api.GET("/xray/config", a.xrayEffectiveConfig)
	api.GET("/xray/logs", a.xrayLogs)
	api.GET("/xray/template", a.xrayTemplateGet)
	api.PUT("/xray/template", a.xrayTemplatePut)

	api.GET("/inbounds", a.listInbounds)
	api.GET("/inbounds/:id", a.getInbound)
	api.POST("/inbounds", a.createInbound)
	api.PUT("/inbounds/:id", a.updateInbound)
	api.PATCH("/inbounds/:id/enable", a.setInboundEnable)
	api.DELETE("/inbounds/:id", a.deleteInbound)
	api.POST("/inbounds/:id/reset-traffic", a.resetInboundTraffic)

	api.POST("/inbounds/bulk", a.bulkCreateInbounds)
	api.PATCH("/inbounds/bulk-enable", a.bulkSetEnable)
	api.POST("/inbounds/bulk-delete", a.bulkDelete)
	api.POST("/inbounds/reset-all-traffic", a.bulkResetTraffic)

	api.GET("/traffic", a.dbTraffic)
	api.GET("/traffic/live", a.liveTraffic)

	api.GET("/settings", a.getSettings)
	api.PATCH("/settings", a.patchSettings)
	api.POST("/settings/api-token/rotate", a.rotateAPIToken)

	api.GET("/tokens", a.listTokens)
	api.POST("/tokens", a.createToken)
	api.PATCH("/tokens/:id", a.renameToken)
	api.POST("/tokens/:id/revoke", a.revokeToken)
	api.DELETE("/tokens/:id", a.deleteToken)

	api.GET("/inbounds/:id/clients", a.listClients)
	api.POST("/inbounds/:id/clients", a.addClient)
	api.PUT("/inbounds/:id/clients/:identifier", a.updateClient)
	api.DELETE("/inbounds/:id/clients/:identifier", a.deleteClient)

	api.GET("/inbounds/:id/links", a.inboundLinks)
	api.GET("/subscription", a.subscriptionAll)
	api.GET("/inbounds/:id/subscription", a.subscriptionOne)

	api.GET("/certs", a.listCerts)
	api.POST("/certs", a.uploadCert)
	api.DELETE("/certs/:name", a.deleteCert)

	api.GET("/system/listening-ports", a.listeningPorts)
	api.GET("/system/check-port", a.checkPort)
	api.POST("/system/restart-panel", a.restartPanel)

	api.POST("/login-tokens", a.createMagicToken)

	api.GET("/access-logs", a.listAccessLogs)
	api.DELETE("/access-logs", a.purgeAccessLogs)

	api.GET("/system/update-check", a.updateCheck)
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
	if q.Page > 0 {
		c.JSON(200, gin.H{
			"data": items,
			"meta": gin.H{"total": total, "page": q.Page, "size": q.Size},
		})
		return
	}
	OK(c, items)
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
		BadRequest(c, "create_failed", err.Error())
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
		BadRequest(c, "update_failed", err.Error())
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
	Name string `json:"name" binding:"required"`
}

type tokenView struct {
	Id         int    `json:"id"`
	Name       string `json:"name"`
	CreatedAt  int64  `json:"createdAt"`
	LastUsedAt int64  `json:"lastUsedAt"`
	Revoked    bool   `json:"revoked"`
}

func tokenToView(t *model.APIToken) tokenView {
	return tokenView{
		Id:         t.Id,
		Name:       t.Name,
		CreatedAt:  t.CreatedAt,
		LastUsedAt: t.LastUsedAt,
		Revoked:    t.Revoked,
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
	t, err := a.tokenService.CreateToken(req.Name)
	if err != nil {
		BadRequest(c, "create_failed", err.Error())
		return
	}
	// Plaintext token returned exactly once.
	Created(c, gin.H{
		"id":        t.Id,
		"name":      t.Name,
		"token":     t.Token,
		"createdAt": t.CreatedAt,
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
		BadRequest(c, "update_failed", err.Error())
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
	user, _ := a.userService.GetFirstUser()
	if user != nil {
		for _, in := range items {
			in.UserId = user.Id
		}
	}
	if err := a.inboundService.AddInbounds(items); err != nil {
		BadRequest(c, "create_failed", err.Error())
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
	n, err := a.inboundService.SetEnableMany(req.IDs, req.Enable)
	if err != nil {
		Internal(c, "update_failed", err)
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
	n, err := a.inboundService.DeleteMany(req.IDs)
	if err != nil {
		Internal(c, "delete_failed", err)
		return
	}
	a.xrayService.SetToNeedRestart()
	OK(c, gin.H{"affected": n})
}

func (a *V1Controller) bulkResetTraffic(c *gin.Context) {
	var req struct {
		IDs []int `json:"ids"`
	}
	_ = c.ShouldBindJSON(&req)
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

func (a *V1Controller) inboundLinks(c *gin.Context) {
	id, ok := parseIntParam(c, "id")
	if !ok {
		return
	}
	host := c.Query("host")
	if host == "" {
		BadRequest(c, "host_required", "?host=<address> is required so we know what to put in the link")
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
	host := c.Query("host")
	if host == "" {
		BadRequest(c, "host_required", "?host=<address> is required")
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
	host := c.Query("host")
	if host == "" {
		BadRequest(c, "host_required", "?host=<address> is required")
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
	t, err := a.magicService.CreateMagicToken(time.Duration(req.TtlSeconds)*time.Second, req.Note)
	if err != nil {
		Internal(c, "magic_token_failed", err)
		return
	}
	// Build a relative URL the caller can prepend the panel host to.
	basePath := c.GetString("base_path")
	if basePath == "" {
		basePath = "/"
	}
	relativeURL := basePath + "panel-login/" + t.Token
	Created(c, gin.H{
		"token":       t.Token,
		"createdAt":   t.CreatedAt,
		"expiresAt":   t.ExpiresAt,
		"note":        t.Note,
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
