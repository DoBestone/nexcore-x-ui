package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"nexcore-x-ui/database"
	"nexcore-x-ui/database/model"
	"nexcore-x-ui/util/common"
	"nexcore-x-ui/xray"

	"gorm.io/gorm"
)

// validateListenAddress 确保 inbound 的 listen 字段在保存前能落到本机 NIC
// 上,避免 xray 启动时 bind: cannot assign requested address。
//
// 这条错误的杀伤力远超表面 —— 一个 inbound 写错了 listen,xray 整个进程
// 启动失败 / 退出,所有其它 inbound(完全无关的 SS / VLESS / Trojan)同
// 一时间全部不可达。客户端表象就是"软件连接超时",而面板用户根本想
// 不到是某条 vmess 入站把全场拖下水。审计指出的最常见误填:云厂商弹性
// 公网 IP —— 通过 NAT 进来,本机 NIC 上根本没这地址。
//
// 通过条件:
//   - 留空(""):xray 默认监听 0.0.0.0,最常见也最安全。
//   - 0.0.0.0 / ::(unspecified):显式监听全部接口。
//   - IP 字面量且在本机 net.InterfaceAddrs() 里命中。
//
// 拒绝条件返回带上下文的 common.NewError,前端把 message 直接 toast 出来。
func validateListenAddress(listen string) error {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return nil
	}
	want := net.ParseIP(listen)
	if want == nil {
		return common.NewError("listen 必须是 IP 地址(留空表示监听全部接口),不接受域名: ", listen)
	}
	if want.IsUnspecified() {
		return nil
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		// 罕见的内核错误,不在校验失败里把保存阻死。
		return nil
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip != nil && ip.Equal(want) {
			return nil
		}
	}
	return common.NewError("listen 地址不在本机网卡(NIC)上,xray 启动会 bind 失败,届时所有入站(包含其它协议)一起不可达。云厂商公网 IP 通常通过 NAT 映射进来,本机看不到,请改填内网 IP 或留空让 xray 监听全部接口: ", listen)
}

// ErrProtocolSingleton is returned when an operator tries to add a second
// inbound for a protocol that supports multi-user via settings.clients[]
// (vless / vmess / trojan / shadowsocks-2022). Those protocols are designed
// to share one port across many users; allowing two separate inbounds is
// always a mistake — they'd just compete for the same port. The right move
// is to edit the existing inbound and add a client there.
var ErrProtocolSingleton = errors.New("protocol_singleton")

type InboundService struct {
}

// isMultiUserProtocol reports whether the given inbound's protocol supports
// multi-user via settings.clients[]. Shadowsocks is a special case: only the
// 2022-blake3-* AEAD methods support clients[]; legacy AEAD/stream methods
// are single-user (one password = one user) and we let those duplicate.
func isMultiUserProtocol(in *model.Inbound) bool {
	switch in.Protocol {
	case model.VLESS, model.VMess, model.Trojan:
		return true
	case model.Shadowsocks:
		var s struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal([]byte(in.Settings), &s)
		return strings.HasPrefix(s.Method, "2022-blake3-")
	}
	return false
}

// checkProtocolSingleton enforces "one inbound per multi-user protocol".
// ignoreId > 0 means "we're updating that inbound, don't count it against
// itself". Returns ErrProtocolSingleton on conflict.
func (s *InboundService) checkProtocolSingleton(in *model.Inbound, ignoreId int) error {
	if !isMultiUserProtocol(in) {
		return nil
	}
	db := database.GetDB().Model(model.Inbound{}).Where("protocol = ?", string(in.Protocol))
	if ignoreId > 0 {
		db = db.Where("id != ?", ignoreId)
	}
	// For shadowsocks we have to filter further: only 2022-blake3-* peers
	// are singletons. Legacy SS rows in DB stay free to coexist.
	var rows []*model.Inbound
	if err := db.Find(&rows).Error; err != nil {
		return err
	}
	for _, r := range rows {
		if isMultiUserProtocol(r) {
			return fmt.Errorf("%w: protocol %s already has inbound id=%d, edit that one and add a client instead",
				ErrProtocolSingleton, in.Protocol, r.Id)
		}
	}
	return nil
}

// dryRunWithReplacement simulates the persisted inbound list with one
// hypothetical change applied (add new / replace existing / remove by id),
// then asks xray to validate the resulting config.
//
// Mode:
//   - replace == nil && removeId == 0: just validate current state
//   - replace != nil && replace.Id == 0: append as new
//   - replace != nil && replace.Id > 0: substitute the row with that id
//   - removeId > 0: drop the row with that id
func (s *InboundService) dryRunWithReplacement(replace *model.Inbound, removeId int) error {
	all, err := s.GetAllInbounds()
	if err != nil {
		return err
	}
	candidate := make([]*model.Inbound, 0, len(all)+1)
	replaced := false
	for _, in := range all {
		if removeId > 0 && in.Id == removeId {
			continue
		}
		if replace != nil && replace.Id > 0 && in.Id == replace.Id {
			candidate = append(candidate, replace)
			replaced = true
			continue
		}
		candidate = append(candidate, in)
	}
	if replace != nil && !replaced && replace.Id == 0 {
		candidate = append(candidate, replace)
	}
	return GetXrayServiceForDryRun().DryRunInbounds(candidate)
}

func (s *InboundService) GetInbounds(userId int) ([]*model.Inbound, error) {
	db := database.GetDB()
	var inbounds []*model.Inbound
	err := db.Model(model.Inbound{}).Where("user_id = ?", userId).Find(&inbounds).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	return inbounds, nil
}

func (s *InboundService) GetAllInbounds() ([]*model.Inbound, error) {
	db := database.GetDB()
	var inbounds []*model.Inbound
	err := db.Model(model.Inbound{}).Find(&inbounds).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	return inbounds, nil
}

func (s *InboundService) checkPortExist(port int, ignoreId int) (bool, error) {
	db := database.GetDB()
	db = db.Model(model.Inbound{}).Where("port = ?", port)
	if ignoreId > 0 {
		db = db.Where("id != ?", ignoreId)
	}
	var count int64
	err := db.Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *InboundService) AddInbound(inbound *model.Inbound) error {
	if err := validateListenAddress(inbound.Listen); err != nil {
		return err
	}
	exist, err := s.checkPortExist(inbound.Port, 0)
	if err != nil {
		return err
	}
	if exist {
		return common.NewError("端口已存在:", inbound.Port)
	}
	if err := s.checkProtocolSingleton(inbound, 0); err != nil {
		return err
	}
	// xray dry-run BEFORE writing the DB. If we wrote first and xray then
	// rejected the resulting config, the panel would still hold a row that
	// breaks every other inbound on the next reload. The Id is 0 here, so
	// dryRunWithReplacement appends `inbound` as a new candidate.
	if inbound.Enable {
		if err := s.dryRunWithReplacement(inbound, 0); err != nil {
			return err
		}
	}
	db := database.GetDB()
	if err := db.Save(inbound).Error; err != nil {
		return err
	}
	// v1.1.0+ 多用户协议:把 settings.clients[].email 同步到 client_traffics,
	// 否则 modal 客户列表(读 client_traffics)和入站卡 badge(数 settings.
	// clients.length)会对不上。原来这步只在 ClientService.AddClient 走
	// "+ 添加客户端" 那条路时跑,通过"添加入站"表单内嵌的初始 client 一直
	// 漏掉。inbound 级的 total/expiry/enable 当作初始默认,后续可单独 PATCH。
	syncEmbeddedClientTraffics(inbound)
	return nil
}

// SyncAllClientTraffics 扫描所有 inbound,把 settings.clients[].email 同步
// 到 client_traffics 表。一次性兜底:升级到带 syncEmbeddedClientTraffics
// 之前创建的旧 inbound 内嵌 client 没有 client_traffics 行,modal 看不到
// 它们。startTask() 启动时调一次,后续 AddInbound/UpdateInbound 会自动维护。
// EnsureRow 是幂等的(写过就 update,没写过就 insert),重复调用零代价。
func (s *InboundService) SyncAllClientTraffics() error {
	all, err := s.GetAllInbounds()
	if err != nil {
		return err
	}
	for _, in := range all {
		syncEmbeddedClientTraffics(in)
	}
	return nil
}

// syncEmbeddedClientTraffics scans inbound.settings for clients[].email and
// ensures a client_traffics row exists for each. Idempotent — callable from
// both AddInbound and UpdateInbound. Email-less protocols (socks/http/dokodemo
// /SS-legacy) just no-op since their settings don't carry clients[].
func syncEmbeddedClientTraffics(inbound *model.Inbound) {
	if inbound == nil || inbound.Settings == "" {
		return
	}
	var s struct {
		Clients []map[string]any `json:"clients"`
	}
	if err := json.Unmarshal([]byte(inbound.Settings), &s); err != nil {
		return
	}
	cts := &ClientTrafficService{}
	for _, c := range s.Clients {
		email, _ := c["email"].(string)
		if email == "" {
			continue
		}
		_ = cts.EnsureRow(inbound.Id, email, inbound.Total, inbound.ExpiryTime, inbound.Enable)
	}
}

func (s *InboundService) AddInbounds(inbounds []*model.Inbound) error {
	// Single round-trip port collision check: pull every existing port
	// in one SELECT, build an in-memory set, then validate the batch
	// (also catches duplicates *within* the batch). Replaces the prior
	// O(N) checkPortExist loop which fired one COUNT(*) per item — at
	// 100 inbounds in a bulk import that's 100 wasted DB calls.
	db := database.GetDB()
	wantPorts := make([]int, 0, len(inbounds))
	for _, in := range inbounds {
		wantPorts = append(wantPorts, in.Port)
	}
	var taken []int
	if len(wantPorts) > 0 {
		if err := db.Model(&model.Inbound{}).
			Where("port IN ?", wantPorts).
			Pluck("port", &taken).Error; err != nil {
			return err
		}
	}
	exists := make(map[int]struct{}, len(taken))
	for _, p := range taken {
		exists[p] = struct{}{}
	}
	seen := make(map[int]struct{}, len(inbounds))
	seenSingleton := map[model.Protocol]bool{}
	for _, in := range inbounds {
		if err := validateListenAddress(in.Listen); err != nil {
			return err
		}
		if _, dup := seen[in.Port]; dup {
			return common.NewError("批次内端口重复:", in.Port)
		}
		seen[in.Port] = struct{}{}
		if _, taken := exists[in.Port]; taken {
			return common.NewError("端口已存在:", in.Port)
		}
		if err := s.checkProtocolSingleton(in, 0); err != nil {
			return err
		}
		// Two new VLESS rows in one batch is also illegal.
		if isMultiUserProtocol(in) {
			if seenSingleton[in.Protocol] {
				return fmt.Errorf("%w: protocol %s appears twice in this batch",
					ErrProtocolSingleton, in.Protocol)
			}
			seenSingleton[in.Protocol] = true
		}
	}

	// Dry-run with the entire batch applied. Cross-inbound issues (tag
	// collisions inside the template, etc.) only show up when xray sees
	// them all at once — per-row validation isn't enough.
	all, err := s.GetAllInbounds()
	if err != nil {
		return err
	}
	candidate := append([]*model.Inbound{}, all...)
	for _, in := range inbounds {
		if in.Enable {
			candidate = append(candidate, in)
		}
	}
	if err := GetXrayServiceForDryRun().DryRunInbounds(candidate); err != nil {
		return err
	}

	tx := db.Begin()
	defer func() {
		if err == nil {
			tx.Commit()
		} else {
			tx.Rollback()
		}
	}()
	for _, inbound := range inbounds {
		err = tx.Save(inbound).Error
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *InboundService) DelInbound(id int) error {
	db := database.GetDB()
	return db.Delete(model.Inbound{}, id).Error
}

// DeleteAll — 一次性清空所有 inbound + 它们绑定的 client_traffics 行。
// 走单事务,要么全成要么全回滚,避免 inbounds 没了但 client_traffics
// 还残留(后者用 email 当主索引,没了 inbound 就成孤儿行)。GORM 在
// 没 WHERE 的 Delete 默认会拒掉,所以用 Where("1 = 1") 显式表态。
func (s *InboundService) DeleteAll() (int64, error) {
	db := database.GetDB()
	var n int64
	err := db.Transaction(func(tx *gorm.DB) error {
		if r := tx.Where("1 = 1").Delete(&model.ClientTraffic{}); r.Error != nil {
			return r.Error
		}
		r := tx.Where("1 = 1").Delete(&model.Inbound{})
		if r.Error != nil {
			return r.Error
		}
		n = r.RowsAffected
		return nil
	})
	return n, err
}

func (s *InboundService) GetInbound(id int) (*model.Inbound, error) {
	db := database.GetDB()
	inbound := &model.Inbound{}
	err := db.Model(model.Inbound{}).First(inbound, id).Error
	if err != nil {
		return nil, err
	}
	return inbound, nil
}

func (s *InboundService) UpdateInbound(inbound *model.Inbound) error {
	if err := validateListenAddress(inbound.Listen); err != nil {
		return err
	}
	exist, err := s.checkPortExist(inbound.Port, inbound.Id)
	if err != nil {
		return err
	}
	if exist {
		return common.NewError("端口已存在:", inbound.Port)
	}
	if err := s.checkProtocolSingleton(inbound, inbound.Id); err != nil {
		return err
	}

	oldInbound, err := s.GetInbound(inbound.Id)
	if err != nil {
		return err
	}
	oldInbound.Up = inbound.Up
	oldInbound.Down = inbound.Down
	oldInbound.Total = inbound.Total
	oldInbound.Remark = inbound.Remark
	oldInbound.Enable = inbound.Enable
	oldInbound.ExpiryTime = inbound.ExpiryTime
	oldInbound.Listen = inbound.Listen
	oldInbound.Port = inbound.Port
	oldInbound.Protocol = inbound.Protocol
	oldInbound.Settings = inbound.Settings
	oldInbound.StreamSettings = inbound.StreamSettings
	oldInbound.Sniffing = inbound.Sniffing
	oldInbound.Tag = fmt.Sprintf("inbound-%v", inbound.Port)

	// xray dry-run with this inbound REPLACING the old row. If xray
	// rejects the candidate (bad reality keys, etc.) we never touch the
	// DB — the running xray process keeps the old, valid row.
	if oldInbound.Enable {
		if err := s.dryRunWithReplacement(oldInbound, 0); err != nil {
			return err
		}
	}

	db := database.GetDB()
	if err := db.Save(oldInbound).Error; err != nil {
		return err
	}
	// 跟 AddInbound 对称:settings.clients[] 变更时同步 client_traffics,
	// 否则 modal(读 client_traffics)和入站卡 badge(数 clients[])对不上。
	syncEmbeddedClientTraffics(oldInbound)
	return nil
}

func (s *InboundService) AddTraffic(traffics []*xray.Traffic) (err error) {
	if len(traffics) == 0 {
		return nil
	}
	db := database.GetDB()
	db = db.Model(model.Inbound{})
	tx := db.Begin()
	defer func() {
		if err != nil {
			tx.Rollback()
		} else {
			tx.Commit()
		}
	}()
	for _, traffic := range traffics {
		if traffic.IsInbound {
			err = tx.Where("tag = ?", traffic.Tag).
				UpdateColumn("up", gorm.Expr("up + ?", traffic.Up)).
				UpdateColumn("down", gorm.Expr("down + ?", traffic.Down)).
				Error
			if err != nil {
				return
			}
		}
	}
	return
}

// InboundQuery describes optional filters for paginated inbound listing.
type InboundQuery struct {
	Protocol string
	Enable   *bool
	Tag      string
	Search   string // matches remark/tag substring
	Page     int    // 1-based; 0 means no pagination
	Size     int    // page size; capped at 200
}

// QueryInbounds is the read path used by the API listing endpoint. Returns
// items + total count so the caller can build pagination metadata. Behavior
// when Page == 0: returns everything matching filters (no pagination).
func (s *InboundService) QueryInbounds(q InboundQuery) ([]*model.Inbound, int64, error) {
	db := database.GetDB().Model(model.Inbound{})
	if q.Protocol != "" {
		db = db.Where("protocol = ?", q.Protocol)
	}
	if q.Tag != "" {
		db = db.Where("tag = ?", q.Tag)
	}
	if q.Enable != nil {
		db = db.Where("enable = ?", *q.Enable)
	}
	if q.Search != "" {
		like := "%" + q.Search + "%"
		db = db.Where("remark LIKE ? OR tag LIKE ?", like, like)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if q.Page > 0 {
		size := q.Size
		if size <= 0 {
			size = 50
		}
		if size > 200 {
			size = 200
		}
		db = db.Offset((q.Page - 1) * size).Limit(size)
	}
	var items []*model.Inbound
	if err := db.Order("id asc").Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// SetEnableMany flips the enable flag for a set of inbound ids in one
// transaction. Returns the number of rows affected.
func (s *InboundService) SetEnableMany(ids []int, enable bool) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	// Disabling never increases attack surface for xray (fewer inbounds
	// = strictly less to fail), so skip dry-run on the off path.
	if !enable {
		res := database.GetDB().Model(model.Inbound{}).
			Where("id IN ?", ids).
			Update("enable", false)
		return res.RowsAffected, res.Error
	}

	// Enabling could promote a previously-disabled, broken inbound into
	// the running config — exactly the failure mode 3x-ui has. Simulate
	// the post-update state and dry-run xray before touching the DB.
	all, err := s.GetAllInbounds()
	if err != nil {
		return 0, err
	}
	want := make(map[int]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	candidate := make([]*model.Inbound, 0, len(all))
	for _, in := range all {
		if want[in.Id] {
			copy := *in
			copy.Enable = true
			candidate = append(candidate, &copy)
		} else if in.Enable {
			candidate = append(candidate, in)
		}
	}
	if err := GetXrayServiceForDryRun().DryRunInbounds(candidate); err != nil {
		return 0, err
	}
	res := database.GetDB().Model(model.Inbound{}).
		Where("id IN ?", ids).
		Update("enable", true)
	return res.RowsAffected, res.Error
}

// DeleteMany removes inbounds in one transaction.
func (s *InboundService) DeleteMany(ids []int) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res := database.GetDB().Where("id IN ?", ids).Delete(&model.Inbound{})
	return res.RowsAffected, res.Error
}

// ResetTrafficMany zeroes up/down counters for the given inbounds.
// nil ids means "reset all".
func (s *InboundService) ResetTrafficMany(ids []int) (int64, error) {
	db := database.GetDB().Model(model.Inbound{})
	if len(ids) > 0 {
		db = db.Where("id IN ?", ids)
	}
	res := db.Updates(map[string]any{"up": 0, "down": 0})
	return res.RowsAffected, res.Error
}

func (s *InboundService) DisableInvalidInbounds() (int64, error) {
	db := database.GetDB()
	now := time.Now().Unix() * 1000
	result := db.Model(model.Inbound{}).
		Where("((total > 0 and up + down >= total) or (expiry_time > 0 and expiry_time <= ?)) and enable = ?", now, true).
		Update("enable", false)
	err := result.Error
	count := result.RowsAffected
	return count, err
}
