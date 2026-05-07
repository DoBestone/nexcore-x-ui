package service

// ClientTrafficService — v1.1.0 引入,per-client 维度的查询/重置/到期管理。
//
// 数据流:
//   1. 操作员通过 API 创建 client(/api/v1/inbounds/:id/clients) →
//      ClientService 写 inbound.settings.clients[] + 这里建 client_traffics 行
//   2. xray 子进程跑业务,gRPC stats 每秒按 email 聚合上下行字节
//   3. xray_traffic_job 拉到 traffics → AddTraffic 按 email 写到 client_traffics
//      (顺手检查到期/触顶 → 自动 enable=false,触发 xray reload)
//   4. 业务系统通过 API 主动调:查询余量、重置(月初清零)、改 expiry/total
//
// 为什么不开独立 cron:面板的产品定位是节点底座,商业逻辑由上游业务系统对接
// API 自己跑 cron,面板内部只在流量写入时顺手检查到期触顶(成本接近 0)。

import (
	"encoding/json"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"nexcore-x-ui/database"
	"nexcore-x-ui/database/model"
	"nexcore-x-ui/xray"
)

// ErrClientTrafficNotFound — 业务系统按 email 查不到 client_traffics 行时返回。
var ErrClientTrafficNotFound = errors.New("client_traffic_not_found")

type ClientTrafficService struct{}

// GetByEmail 按全局唯一 email 取单行。业务系统按"客户"维度操作时常用。
func (s *ClientTrafficService) GetByEmail(email string) (*model.ClientTraffic, error) {
	if email == "" {
		return nil, ErrClientTrafficNotFound
	}
	var ct model.ClientTraffic
	err := database.GetDB().Where("email = ?", email).First(&ct).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrClientTrafficNotFound
	}
	if err != nil {
		return nil, err
	}
	return &ct, nil
}

// DisabledEmails 返回 client_traffics.enable=false 的所有 email 集合。
// XrayService.GetXrayConfig 用它从 inbound.settings.clients[] 里剥掉
// disabled 客户。空集合是合法的(没人被禁),caller 不需要做 nil 判断。
func (s *ClientTrafficService) DisabledEmails() (map[string]struct{}, error) {
	var rows []model.ClientTraffic
	if err := database.GetDB().
		Select("email").
		Where("enable = ?", false).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		if r.Email != "" {
			out[r.Email] = struct{}{}
		}
	}
	return out, nil
}

// ListByInbound — 入站详情页客户端列表用。返回该 inbound 下所有 client 的
// 流量行,顺序按 email 排序便于稳定渲染。
func (s *ClientTrafficService) ListByInbound(inboundID int) ([]model.ClientTraffic, error) {
	var rows []model.ClientTraffic
	err := database.GetDB().
		Where("inbound_id = ?", inboundID).
		Order("email asc").
		Find(&rows).Error
	return rows, err
}

// EnsureRow — 在 ClientService 创建/编辑 client 时调用,保证 client_traffics
// 表里有对应行。已存在则更新 inbound_id(client 改归属时)+ 不动 up/down。
//
// 不暴露给 API,只供 ClientService 内部调用 — API 层创建 client 应该通过
// ClientService.AddClient 一次走完 settings + client_traffics 两边。
func (s *ClientTrafficService) EnsureRow(inboundID int, email string,
	total, expiryTime int64, enable bool) error {
	if email == "" {
		return nil
	}
	db := database.GetDB()
	var existing model.ClientTraffic
	err := db.Where("email = ?", email).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return db.Create(&model.ClientTraffic{
			InboundId:  inboundID,
			Email:      email,
			Total:      total,
			ExpiryTime: expiryTime,
			Enable:     enable,
		}).Error
	}
	if err != nil {
		return err
	}
	// 已存在:同步配置字段,不动 up/down(那是流量计数,绝不能因为编辑客户
	// 配置就被清掉)。enable 的同步语义:UI 这边是单一真相,以本次写入为准。
	existing.InboundId = inboundID
	existing.Total = total
	existing.ExpiryTime = expiryTime
	existing.Enable = enable
	return db.Save(&existing).Error
}

// DeleteByEmail — ClientService 删除 client 时调用。同步从 client_traffics
// 移除,避免幽灵行。失败也只记录 warn(主路径已经删了 settings.clients[],
// 残留 client_traffics 行不会影响 xray 运行,只是数据冗余)。
func (s *ClientTrafficService) DeleteByEmail(email string) error {
	if email == "" {
		return nil
	}
	return database.GetDB().Where("email = ?", email).
		Delete(&model.ClientTraffic{}).Error
}

// ResetTraffic — up/down 清零。total / expiryTime / enable 不变。
// 业务系统月初对账后调,或者操作员手工触发。
func (s *ClientTrafficService) ResetTraffic(email string) error {
	res := database.GetDB().Model(&model.ClientTraffic{}).
		Where("email = ?", email).
		Updates(map[string]interface{}{"up": 0, "down": 0})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrClientTrafficNotFound
	}
	return nil
}

// SetLimits — 业务系统改 client 的流量上限/到期时间/启用状态。任意字段
// 传 nil 表示不改。enable=false 立即生效(下一次 xray reload 后 client
// 不能用),不需要等到流量自然写入路径。
type SetLimitsParams struct {
	Total      *int64 `json:"total"`
	ExpiryTime *int64 `json:"expiryTime"`
	Enable     *bool  `json:"enable"`
}

func (s *ClientTrafficService) SetLimits(email string, p SetLimitsParams) error {
	updates := map[string]interface{}{}
	if p.Total != nil {
		updates["total"] = *p.Total
	}
	if p.ExpiryTime != nil {
		updates["expiry_time"] = *p.ExpiryTime
	}
	if p.Enable != nil {
		updates["enable"] = *p.Enable
	}
	if len(updates) == 0 {
		return nil
	}
	res := database.GetDB().Model(&model.ClientTraffic{}).
		Where("email = ?", email).
		Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrClientTrafficNotFound
	}
	return nil
}

// AddTrafficByEmail — 把 xray gRPC stats 拉到的 user(email)级流量累加到
// client_traffics,顺手检查到期/触顶 → 自动 enable=false。
//
// 触顶判断用 (up + down) >= total,total=0 跳过(不限)。
// 到期判断用 expiryTime > 0 && now >= expiryTime。
//
// 自动 disable 的写入用同一事务:既更新 up/down,也写 enable=false。
// 这样 xray_traffic_job 看到 RowsAffected 增加就能调 SetToNeedRestart()
// 触发 xray reload,把刚刚耗尽的 client 踢下线。
//
// 返回:本次因到期/触顶被自动 disable 的 email 列表 — caller 可用来日志/
// 通知,也可以直接给 xray reload 信号。
func (s *ClientTrafficService) AddTrafficByEmail(traffics []*xray.Traffic, nowMs int64) ([]string, error) {
	disabled := []string{}
	if len(traffics) == 0 {
		return disabled, nil
	}

	// Step 1: aggregate deltas per email IN MEMORY, dedup the input slice.
	// xray's gRPC stats can report the same email twice in one tick under
	// load; folding them up front lets us issue exactly one UPDATE per
	// email below regardless of how many Traffic rows the caller passed.
	type delta struct {
		up   int64
		down int64
	}
	deltas := make(map[string]*delta, len(traffics))
	emails := make([]string, 0, len(traffics))
	for _, t := range traffics {
		if t.IsInbound {
			continue // 这里只处理 user 级;inbound 级走 InboundService.AddTraffic
		}
		if t.Tag == "" {
			continue // stats key 解析失败,没 email 无法定位 client
		}
		if d, ok := deltas[t.Tag]; ok {
			d.up += t.Up
			d.down += t.Down
		} else {
			deltas[t.Tag] = &delta{up: t.Up, down: t.Down}
			emails = append(emails, t.Tag)
		}
	}
	if len(emails) == 0 {
		return disabled, nil
	}

	db := database.GetDB()
	tx := db.Begin()
	var err error
	defer func() {
		if err != nil {
			tx.Rollback()
		} else {
			tx.Commit()
		}
	}()

	// Step 2: ONE SELECT for every email this tick, replacing the per-row
	// `First()` from the old impl. That `First` was an N+1 hot path: for a
	// 50-client panel busy enough to flush every 10s, it issued ~50 extra
	// indexed queries per tick on top of the UPDATEs. Loading the rows up
	// front lets the to-disable decision happen in memory; the only DB
	// writes left are one UPDATE per email.
	var rows []model.ClientTraffic
	if err = tx.Where("email IN ?", emails).Find(&rows).Error; err != nil {
		return disabled, err
	}
	rowByEmail := make(map[string]*model.ClientTraffic, len(rows))
	for i := range rows {
		rowByEmail[rows[i].Email] = &rows[i]
	}

	// Step 3: one UPDATE per email, with up / down / (optional) enable
	// folded into a single statement. We also skip the UPDATE entirely
	// for the "stats reported zero new bytes AND nothing to disable" path
	// — common for idle email entries that xray still emits keys for.
	for email, d := range deltas {
		ct, ok := rowByEmail[email]
		if !ok {
			// stats 给了个 DB 里不存在的 email — 通常是 client 刚被删
			// 但 xray 还没 reload。这一轮的字节数被丢弃,等下一轮 xray
			// 用新 inbound 重启就消停了。原实现也是这个语义。
			continue
		}
		newUp := ct.Up + d.up
		newDown := ct.Down + d.down
		shouldDisable := false
		if ct.Enable {
			if ct.Total > 0 && (newUp+newDown) >= ct.Total {
				shouldDisable = true
			}
			if ct.ExpiryTime > 0 && nowMs >= ct.ExpiryTime {
				shouldDisable = true
			}
		}
		if d.up == 0 && d.down == 0 && !shouldDisable {
			continue
		}
		updates := map[string]interface{}{
			"up":   newUp,
			"down": newDown,
		}
		if shouldDisable {
			updates["enable"] = false
		}
		if err = tx.Model(&model.ClientTraffic{}).
			Where("id = ?", ct.Id).
			Updates(updates).Error; err != nil {
			return disabled, err
		}
		if shouldDisable {
			disabled = append(disabled, email)
		}
	}
	return disabled, nil
}

// DisableExpired — 业务系统调 API 触发的批量到期检查。返回被 disable 的
// email 数组。和 AddTrafficByEmail 里的顺手 check 互补 —— 这条是主动扫描,
// 不依赖流量写入触发(零流量 client 也会被到期 disable)。
func (s *ClientTrafficService) DisableExpired(nowMs int64) ([]string, error) {
	db := database.GetDB()
	var rows []model.ClientTraffic
	err := db.Where("enable = ? AND expiry_time > 0 AND expiry_time <= ?",
		true, nowMs).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if err := db.Model(&model.ClientTraffic{}).
			Where("id = ?", r.Id).
			Update("enable", false).Error; err != nil {
			return out, err
		}
		out = append(out, r.Email)
	}
	return out, nil
}

// extractClientEmails — helper:从 inbound.settings JSON 解析 clients[]
// 数组里所有 email。供 InboundService 在 Add/Delete inbound 时跟 client_
// traffics 表做联动(整 inbound 删了的话,所有相关 client_traffics 行也清掉)。
func extractClientEmails(settingsJSON string) []string {
	if settingsJSON == "" {
		return nil
	}
	var s struct {
		Clients []map[string]interface{} `json:"clients"`
	}
	if err := json.Unmarshal([]byte(settingsJSON), &s); err != nil {
		return nil
	}
	out := make([]string, 0, len(s.Clients))
	for _, c := range s.Clients {
		if e, ok := c["email"].(string); ok && e != "" {
			out = append(out, e)
		}
	}
	return out
}

// _silenceUnusedImport — 防止 fmt 在我们暂时不用时被 lint 报 unused;
// migrations / future debug logging 会用到,留着。
var _ = fmt.Sprint
