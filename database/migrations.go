package database

import (
	"crypto/sha256"
	"encoding/hex"

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
		{
			// Hash existing API tokens at rest. The Token column now
			// stores hex(SHA256(plaintext)) instead of the plaintext;
			// the auth middleware hashes the incoming value before
			// looking it up. Operators keep using the plaintext they
			// already have — only the DB representation changed.
			//
			// Detection of "already hashed" relies on the new column
			// width (64 hex chars). The legacy random.Seq(48) tokens
			// are 48 chars, so they're easy to distinguish.
			ID: "0006_hash_api_tokens",
			Migrate: func(tx *gorm.DB) error {
				var rows []model.APIToken
				if err := tx.Find(&rows).Error; err != nil {
					return err
				}
				for _, r := range rows {
					if len(r.Token) == 64 && isHex(r.Token) {
						continue // already hashed
					}
					sum := sha256.Sum256([]byte(r.Token))
					if err := tx.Model(&model.APIToken{}).
						Where("id = ?", r.Id).
						Update("token", hex.EncodeToString(sum[:])).Error; err != nil {
						return err
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				// Hashing is one-way; rollback would invalidate every
				// token. Operators that need to revert should rotate
				// tokens after downgrading.
				return nil
			},
		},
		{
			// Add scope column. Existing rows are admin (preserve current
			// behavior); new rows can be created with a narrower scope so
			// a stolen "readonly" token can't restart the panel.
			ID: "0007_token_scope",
			Migrate: func(tx *gorm.DB) error {
				if err := tx.AutoMigrate(&model.APIToken{}); err != nil {
					return err
				}
				return tx.Model(&model.APIToken{}).
					Where("scope IS NULL OR scope = ''").
					Update("scope", "admin").Error
			},
			Rollback: func(tx *gorm.DB) error { return nil },
		},
		{
			// Add expires_at column. Defaults to 0 (never expires) for
			// every existing row so behavior is preserved. New tokens
			// can be issued with a finite TTL — handy for short-lived CI
			// credentials so a leaked CI log doesn't permanently expose
			// the cluster.
			ID: "0008_token_expires_at",
			Migrate: func(tx *gorm.DB) error {
				return tx.AutoMigrate(&model.APIToken{})
			},
			Rollback: func(tx *gorm.DB) error { return nil },
		},
		{
			// BlockRule:面板可视化的"屏蔽规则",最终合并进 Xray routing.rules
			// 路由到 blocked 出站。和 Inbound 解耦,允许全局或按 inboundTag 作用。
			ID: "0009_block_rules",
			Migrate: func(tx *gorm.DB) error {
				return tx.AutoMigrate(&model.BlockRule{})
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Migrator().DropTable(&model.BlockRule{})
			},
		},
	})
	return m.Migrate()
}

func isHex(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}
