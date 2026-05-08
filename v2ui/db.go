package v2ui

import (
	// 同 database/db.go:用纯 Go 的 glebarez/sqlite,免 cgo 依赖。
	sqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var v2db *gorm.DB

func initDB(dbPath string) error {
	c := &gorm.Config{
		Logger: logger.Discard,
	}
	var err error
	v2db, err = gorm.Open(sqlite.Open(dbPath), c)
	if err != nil {
		return err
	}

	return nil
}

func getV2Inbounds() ([]*V2Inbound, error) {
	inbounds := make([]*V2Inbound, 0)
	err := v2db.Model(V2Inbound{}).Find(&inbounds).Error
	return inbounds, err
}
