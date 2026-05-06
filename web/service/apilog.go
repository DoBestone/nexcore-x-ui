package service

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"x-ui/database"
	"x-ui/database/model"
)

const apiLogGCAfter = 14 * 24 * time.Hour

type APILogService struct{}

// Record inserts a row asynchronously. Failures are swallowed because we
// must never let logging break a real request.
func (s *APILogService) Record(entry *model.APILog) {
	if entry == nil {
		return
	}
	if entry.At == 0 {
		entry.At = time.Now().Unix()
	}
	go func(e *model.APILog) {
		_ = database.GetDB().Create(e).Error
	}(entry)
}

// Query returns paginated rows newest-first.
type APILogQuery struct {
	Path      string
	Method    string
	TokenName string
	Status    int
	Page      int
	Size      int
}

func (s *APILogService) Query(q APILogQuery) ([]*model.APILog, int64, error) {
	db := database.GetDB().Model(&model.APILog{})
	if q.Path != "" {
		db = db.Where("path LIKE ?", "%"+q.Path+"%")
	}
	if q.Method != "" {
		db = db.Where("method = ?", q.Method)
	}
	if q.TokenName != "" {
		db = db.Where("token_name = ?", q.TokenName)
	}
	if q.Status != 0 {
		db = db.Where("status = ?", q.Status)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page := q.Page
	if page <= 0 {
		page = 1
	}
	size := q.Size
	if size <= 0 {
		size = 50
	}
	if size > 200 {
		size = 200
	}
	var items []*model.APILog
	if err := db.Order("at desc").
		Offset((page - 1) * size).Limit(size).
		Find(&items).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, 0, err
	}
	return items, total, nil
}

// Purge removes log rows older than apiLogGCAfter; called from cron.
func (s *APILogService) Purge() (int64, error) {
	cutoff := time.Now().Add(-apiLogGCAfter).Unix()
	res := database.GetDB().Where("at < ?", cutoff).Delete(&model.APILog{})
	return res.RowsAffected, res.Error
}
