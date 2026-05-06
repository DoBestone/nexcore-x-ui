package database

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"

	"nexcore-x-ui/database/model"
)

// migrations is the source of truth for schema evolution. Each entry has a
// stable ID (recorded in the `migrations` bookkeeping table) and runs at most
// once per database. Adding new schema changes means appending to this slice;
// never rewriting existing entries.
//
// "0001_init" captures the schema as it stood when AutoMigrate was the only
// migration story. Existing deployments will skip it after the first run
// because gormigrate checks the bookkeeping table.
func runMigrations(db *gorm.DB) error {
	m := gormigrate.New(db, gormigrate.DefaultOptions, []*gormigrate.Migration{
		{
			ID: "0001_init",
			Migrate: func(tx *gorm.DB) error {
				return tx.AutoMigrate(
					&model.User{},
					&model.Inbound{},
					&model.Setting{},
				)
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Migrator().DropTable(
					&model.User{},
					&model.Inbound{},
					&model.Setting{},
				)
			},
		},
		{
			// Replaced by RunFirstRunSetup, which generates random
			// credentials. Kept as a no-op so the migration ID remains
			// stable for deployments that already recorded it.
			ID:       "0002_seed_admin",
			Migrate:  func(tx *gorm.DB) error { return nil },
			Rollback: func(tx *gorm.DB) error { return nil },
		},
		{
			ID: "0003_api_tokens",
			Migrate: func(tx *gorm.DB) error {
				return tx.AutoMigrate(&model.APIToken{})
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Migrator().DropTable(&model.APIToken{})
			},
		},
		{
			ID: "0004_magic_tokens",
			Migrate: func(tx *gorm.DB) error {
				return tx.AutoMigrate(&model.MagicToken{})
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Migrator().DropTable(&model.MagicToken{})
			},
		},
		{
			ID: "0005_api_logs",
			Migrate: func(tx *gorm.DB) error {
				return tx.AutoMigrate(&model.APILog{})
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Migrator().DropTable(&model.APILog{})
			},
		},
	})
	return m.Migrate()
}
