package database

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

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
		{
			// v1.1.0:per-client 流量/到期 维度。建表 + 回填:把现有
			// inbound.settings.clients[] 里所有 email 解析出来,在
			// client_traffics 表里建对应行(up/down=0,total/expiry/enable
			// 沿用 inbound 级配置 — 因为老数据没有 per-client 字段)。
			//
			// 不存在 email 的 client(socks/http accounts、ss legacy 单
			// 密码)跳过 — 这些协议不属于"订阅 client"模型。
			//
			// migration 是 once-only,后续新增 client 走 ClientTrafficService。
			ID: "0010_client_traffics",
			Migrate: func(tx *gorm.DB) error {
				if err := tx.AutoMigrate(&model.ClientTraffic{}); err != nil {
					return err
				}
				return backfillClientTraffics(tx)
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Migrator().DropTable(&model.ClientTraffic{})
			},
		},
	})
	return m.Migrate()
}

// backfillClientTraffics 解析每个 inbound.settings.clients[],为每条
// 带 email 的 client 在 client_traffics 表里建行。同 email 已存在时
// 跳过(幂等,允许 migration 在意外被重跑后不破坏数据)。
func backfillClientTraffics(tx *gorm.DB) error {
	var inbounds []model.Inbound
	if err := tx.Find(&inbounds).Error; err != nil {
		return err
	}
	for _, in := range inbounds {
		// settings 是 raw JSON 字符串,parse 出 clients 数组。
		var settings struct {
			Clients []map[string]interface{} `json:"clients"`
		}
		if in.Settings == "" {
			continue
		}
		if err := jsonUnmarshal([]byte(in.Settings), &settings); err != nil {
			// 非订阅类协议(socks/http/ss legacy)settings 没有 clients,
			// json.Unmarshal 失败 OR clients=nil,都跳过。
			continue
		}
		for _, c := range settings.Clients {
			email, _ := c["email"].(string)
			if email == "" {
				continue
			}
			// 幂等:已存在就跳过。
			var n int64
			if err := tx.Model(&model.ClientTraffic{}).
				Where("email = ?", email).Count(&n).Error; err != nil {
				return err
			}
			if n > 0 {
				continue
			}
			row := &model.ClientTraffic{
				InboundId:  in.Id,
				Email:      email,
				Up:         0,
				Down:       0,
				Total:      in.Total,      // 老数据没 per-client 上限,继承 inbound 级
				ExpiryTime: in.ExpiryTime, // 同上
				Enable:     in.Enable,
			}
			if err := tx.Create(row).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// jsonUnmarshal — alias for clarity in backfill code; mostly so a future
// migration can swap the parser without grepping.
func jsonUnmarshal(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
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
