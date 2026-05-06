package database

import (
	"io/fs"
	"os"
	"path"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"nexcore-x-ui/config"
)

var (
	db           *gorm.DB
	savedDBPath  string
)

func InitDB(dbPath string) error {
	savedDBPath = dbPath
	dir := path.Dir(dbPath)
	err := os.MkdirAll(dir, fs.ModeDir)
	if err != nil {
		return err
	}

	var gormLogger logger.Interface
	if config.IsDebug() {
		gormLogger = logger.Default
	} else {
		gormLogger = logger.Discard
	}

	db, err = gorm.Open(sqlite.Open(dbPath), &gorm.Config{Logger: gormLogger})
	if err != nil {
		return err
	}
	return runMigrations(db)
}

func GetDB() *gorm.DB {
	return db
}

func IsNotFound(err error) bool {
	return err == gorm.ErrRecordNotFound
}
