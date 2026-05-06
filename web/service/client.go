package service

import (
	"encoding/json"
	"errors"
	"fmt"

	"nexcore-x-ui/database/model"
)

var (
	ErrClientIdentifierRequired = errors.New("client must have a non-empty identifier (email or password)")
	ErrClientNotFound           = errors.New("client not found")
	ErrClientDuplicate          = errors.New("client with same identifier already exists")
	ErrUnsupportedProtocol      = errors.New("client management is not supported for this protocol")
)

// clientFieldByProtocol returns the JSON field that uniquely identifies a
// client inside settings.clients[] for the given xray protocol. We use the
// canonical xray naming (email for VLESS/VMess/Trojan; password for SS multi-
// user mode). The same field is used as both the lookup key and the human-
// readable label exposed by the API.
func clientIdField(p model.Protocol) string {
	switch p {
	case model.VLESS, model.VMess, model.Trojan:
		return "email"
	case model.Shadowsocks:
		// shadowsocks-2022 multi-user mode uses an `email` field too in
		// recent xray; fall back to that to keep a single code path.
		return "email"
	default:
		return ""
	}
}

type ClientService struct {
	inboundService InboundService
	xrayService    XrayService
}

// ListClients returns the raw client objects from settings.clients[]. The
// shape varies per protocol — the caller should treat it as opaque except
// for the identifier field.
func (s *ClientService) ListClients(inboundID int) ([]map[string]any, error) {
	in, err := s.inboundService.GetInbound(inboundID)
	if err != nil {
		return nil, err
	}
	clients, _, err := readClients(in)
	if err != nil {
		return nil, err
	}
	return clients, nil
}

// AddClient appends a client. The caller-provided object must include the
// protocol's identifier field and must not collide with an existing client.
func (s *ClientService) AddClient(inboundID int, client map[string]any) (*model.Inbound, error) {
	in, err := s.inboundService.GetInbound(inboundID)
	if err != nil {
		return nil, err
	}
	idField := clientIdField(in.Protocol)
	if idField == "" {
		return nil, ErrUnsupportedProtocol
	}
	id, _ := client[idField].(string)
	if id == "" {
		return nil, ErrClientIdentifierRequired
	}

	clients, settings, err := readClients(in)
	if err != nil {
		return nil, err
	}
	for _, existing := range clients {
		if v, _ := existing[idField].(string); v == id {
			return nil, ErrClientDuplicate
		}
	}
	clients = append(clients, client)

	if err := writeClients(in, settings, clients); err != nil {
		return nil, err
	}
	if err := s.inboundService.UpdateInbound(in); err != nil {
		return nil, err
	}
	// v1.1.0:同步给 client_traffics 建行(per-client 流量/到期跟踪)。
	// email 字段是 stats key,从 client object 里拿。新建 client 默认
	// 继承 inbound 级 total/expiry 作为初始上限,后续业务系统可通过
	// /api/v1/clients/:email/limits PATCH 单独改。
	if email, _ := client["email"].(string); email != "" {
		_ = (&ClientTrafficService{}).EnsureRow(inboundID, email,
			in.Total, in.ExpiryTime, in.Enable)
	}
	s.xrayService.SetToNeedRestart()
	return in, nil
}

// UpdateClient replaces a single client matched by identifier. The patch is a
// full client object — fields not present are removed (PUT semantics).
func (s *ClientService) UpdateClient(inboundID int, identifier string, patch map[string]any) (*model.Inbound, error) {
	in, err := s.inboundService.GetInbound(inboundID)
	if err != nil {
		return nil, err
	}
	idField := clientIdField(in.Protocol)
	if idField == "" {
		return nil, ErrUnsupportedProtocol
	}
	if identifier == "" {
		return nil, ErrClientIdentifierRequired
	}
	clients, settings, err := readClients(in)
	if err != nil {
		return nil, err
	}
	idx := -1
	for i, c := range clients {
		if v, _ := c[idField].(string); v == identifier {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, ErrClientNotFound
	}
	if v, _ := patch[idField].(string); v == "" {
		patch[idField] = identifier
	}
	clients[idx] = patch

	if err := writeClients(in, settings, clients); err != nil {
		return nil, err
	}
	if err := s.inboundService.UpdateInbound(in); err != nil {
		return nil, err
	}
	// v1.1.0:同步 client_traffics 行 — 处理两种情况:
	//  · email 没改:identifier == new email,EnsureRow 走 update 分支
	//  · email 改了:旧行还在(以 identifier 命名),新行需要建 → 调
	//    DeleteByEmail(identifier) 删旧 + EnsureRow(new) 建新
	if newEmail, _ := patch["email"].(string); newEmail != "" {
		cts := &ClientTrafficService{}
		if newEmail != identifier {
			_ = cts.DeleteByEmail(identifier)
		}
		_ = cts.EnsureRow(inboundID, newEmail, in.Total, in.ExpiryTime, in.Enable)
	}
	s.xrayService.SetToNeedRestart()
	return in, nil
}

func (s *ClientService) DeleteClient(inboundID int, identifier string) (*model.Inbound, error) {
	in, err := s.inboundService.GetInbound(inboundID)
	if err != nil {
		return nil, err
	}
	idField := clientIdField(in.Protocol)
	if idField == "" {
		return nil, ErrUnsupportedProtocol
	}
	clients, settings, err := readClients(in)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(clients))
	found := false
	for _, c := range clients {
		if v, _ := c[idField].(string); v == identifier {
			found = true
			continue
		}
		out = append(out, c)
	}
	if !found {
		return nil, ErrClientNotFound
	}
	if err := writeClients(in, settings, out); err != nil {
		return nil, err
	}
	if err := s.inboundService.UpdateInbound(in); err != nil {
		return nil, err
	}
	// v1.1.0:client_traffics 行也清理掉。identifier 可能是 email
	// (vless/vmess/trojan/ss-2022 这几个走 email 路径),也可能是别
	// 的字段;不管怎样安全删 — DeleteByEmail 不存在就 no-op。
	_ = (&ClientTrafficService{}).DeleteByEmail(identifier)
	s.xrayService.SetToNeedRestart()
	return in, nil
}

// readClients parses Inbound.Settings into a generic map and pulls out the
// clients slice. The full settings map is returned so callers can write back
// without losing other fields (decryption, fallback, etc.).
func readClients(in *model.Inbound) ([]map[string]any, map[string]any, error) {
	settings := map[string]any{}
	if in.Settings != "" {
		if err := json.Unmarshal([]byte(in.Settings), &settings); err != nil {
			return nil, nil, fmt.Errorf("parse inbound settings: %w", err)
		}
	}
	raw, _ := settings["clients"].([]any)
	clients := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		if m, ok := r.(map[string]any); ok {
			clients = append(clients, m)
		}
	}
	return clients, settings, nil
}

// writeClients serializes the clients back into Inbound.Settings JSON.
func writeClients(in *model.Inbound, settings map[string]any, clients []map[string]any) error {
	if settings == nil {
		settings = map[string]any{}
	}
	asAny := make([]any, len(clients))
	for i, c := range clients {
		asAny[i] = c
	}
	settings["clients"] = asAny
	out, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	in.Settings = string(out)
	return nil
}
