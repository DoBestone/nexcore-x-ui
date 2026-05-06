package database

import (
	"os"
	"path"
	"strings"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"nexcore-x-ui/config"
	"nexcore-x-ui/util/secret"
)

var (
	db           *gorm.DB
	savedDBPath  string
)

func InitDB(dbPath string) error {
	savedDBPath = dbPath
	dir := path.Dir(dbPath)
	// Mode 0700 — only the panel user (root in production) should be
	// able to enumerate or read the data dir. The previous fs.ModeDir
	// is a *type bit*, not a permission bits set, so the resulting
	// mode was 0 modulo umask — quiet but real.
	err := os.MkdirAll(dir, 0o700)
	if err != nil {
		return err
	}
	// Best-effort: tighten the dir even when MkdirAll was a no-op
	// because the dir already existed (with looser perms from an old
	// install). chmod returns nil on the common case where we already
	// own the dir, and we don't sweat failures here — systemd
	// ProtectSystem still gates write access.
	_ = os.Chmod(dir, 0o700)
	// Bind the at-rest encryption fallback to the same data dir as the
	// DB. Every binary entry point (web server, CLI subcommands) goes
	// through InitDB, so this is the natural pinch point.
	secret.InitFromDataDir(dir)

	var gormLogger logger.Interface
	if config.IsDebug() {
		gormLogger = logger.Default
	} else {
		gormLogger = logger.Discard
	}

	// DSN tuning:
	//   _journal_mode=WAL  — readers don't block writers and vice versa,
	//                        gives ~3x throughput vs default DELETE journal
	//                        when stats jobs (xray traffic / online IPs)
	//                        update concurrently with API reads
	//   _busy_timeout=5000 — instead of failing immediately on lock, wait
	//                        up to 5s; gormigrate / cron writes serialize
	//                        cleanly without "database is locked" surfacing
	//                        to the user
	//   _foreign_keys=on   — defense-in-depth even though we don't declare
	//                        FKs explicitly; cheap and forward-compatible
	//   cache=shared       — every gorm-opened conn shares the page cache,
	//                        big win for repeated setting reads
	dsn := dbPath
	if !strings.Contains(dsn, "?") {
		dsn += "?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=on&cache=shared"
	}
	db, err = gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormLogger})
	if err != nil {
		return err
	}
	// sqlite creates the file world-readable on some platforms.
	// 0600 makes sure secrets-at-rest (settings table, hashed tokens,
	// magic_tokens) aren't readable by anyone except us.
	_ = os.Chmod(dbPath, 0o600)
	return runMigrations(db)
}

func GetDB() *gorm.DB {
	return db
}

func IsNotFound(err error) bool {
	return err == gorm.ErrRecordNotFound
}
