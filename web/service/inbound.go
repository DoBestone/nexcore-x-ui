package service

import (
	"fmt"
	"time"
	"nexcore-x-ui/database"
	"nexcore-x-ui/database/model"
	"nexcore-x-ui/util/common"
	"nexcore-x-ui/xray"

	"gorm.io/gorm"
)

type InboundService struct {
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
	exist, err := s.checkPortExist(inbound.Port, 0)
	if err != nil {
		return err
	}
	if exist {
		return common.NewError("端口已存在:", inbound.Port)
	}
	db := database.GetDB()
	return db.Save(inbound).Error
}

func (s *InboundService) AddInbounds(inbounds []*model.Inbound) error {
	for _, inbound := range inbounds {
		exist, err := s.checkPortExist(inbound.Port, 0)
		if err != nil {
			return err
		}
		if exist {
			return common.NewError("端口已存在:", inbound.Port)
		}
	}

	db := database.GetDB()
	tx := db.Begin()
	var err error
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
	exist, err := s.checkPortExist(inbound.Port, inbound.Id)
	if err != nil {
		return err
	}
	if exist {
		return common.NewError("端口已存在:", inbound.Port)
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

	db := database.GetDB()
	return db.Save(oldInbound).Error
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
	res := database.GetDB().Model(model.Inbound{}).
		Where("id IN ?", ids).
		Update("enable", enable)
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
