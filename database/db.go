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
	// freshDB is true when the file doesn't exist yet — used below to
	// decide whether to enable auto_vacuum=INCREMENTAL. SQLite only
	// honours auto_vacuum when set BEFORE the first table is created,
	// so we can't retrofit it on existing installs without a full VACUUM
	// (which on a 1GB box can take 30+ seconds). New installs get it
	// for free; old installs stay on the default (manual VACUUM only).
	_, statErr := os.Stat(dbPath)
	freshDB := os.IsNotExist(statErr)
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

	// DSN tuning — targeted at 1C1G VPS where IO is the bottleneck:
	//   _journal_mode=WAL    — readers don't block writers and vice versa,
	//                          ~3x throughput vs default DELETE journal when
	//                          stats jobs (xray traffic / online IPs) update
	//                          concurrently with API reads
	//   _busy_timeout=5000   — instead of failing on lock, wait up to 5s;
	//                          gormigrate / cron writes serialize cleanly
	//                          without surfacing "database is locked"
	//   _foreign_keys=on     — defense-in-depth, cheap, forward-compatible
	//   _synchronous=NORMAL  — under WAL this is durable across crashes (only
	//                          the last in-flight txn can be lost) and skips
	//                          a full fsync per commit. On low-end VPS disks
	//                          this is the single biggest write-throughput
	//                          win — 3-5x faster traffic stats writes.
	//   _cache_size=-20000   — 20MB page cache (negative = KB). Sized for
	//                          1GB-RAM nodes; large enough to keep all hot
	//                          settings/inbound rows in memory, small enough
	//                          to leave headroom for Xray.
	//   _temp_store=MEMORY   — ORDER BY / GROUP BY scratch goes to RAM
	//                          instead of a temp file. Helps the periodic
	//                          traffic aggregation queries.
	//   cache=shared         — every gorm-opened conn shares the page cache,
	//                          big win for repeated setting reads.
	// Deliberately NOT set:
	//   _mmap_size — OS-level mmap on 1GB boxes invites OOM-killer fights
	//                with Xray; the small win isn't worth the risk.
	dsn := dbPath
	if !strings.Contains(dsn, "?") {
		dsn += "?_journal_mode=WAL" +
			"&_busy_timeout=5000" +
			"&_foreign_keys=on" +
			"&_synchronous=NORMAL" +
			"&_cache_size=-20000" +
			"&_temp_store=MEMORY" +
			"&cache=shared"
	}
	db, err = gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormLogger})
	if err != nil {
		return err
	}
	// SQLite is a single-writer database — letting Go open many connections
	// just makes them queue on the same write lock and burns memory. Cap the
	// pool tight: one writable connection, and a short idle TTL so we don't
	// hold file descriptors hostage across reload cycles.
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(0)
	// sqlite creates the file world-readable on some platforms.
	// 0600 makes sure secrets-at-rest (settings table, hashed tokens,
	// magic_tokens) aren't readable by anyone except us.
	_ = os.Chmod(dbPath, 0o600)
	// Enable auto_vacuum=INCREMENTAL on brand-new databases ONLY.
	// Why incremental and not full:
	//   - FULL auto_vacuum runs on every commit, paying for free-page
	//     compaction even when nothing was deleted — overhead 1H1G can't
	//     spare on the streaming traffic-stats hot path.
	//   - INCREMENTAL never auto-runs; we trigger PRAGMA
	//     incremental_vacuum(N) from a cron when we want to reclaim
	//     space (currently we don't, since deletions are tiny — but
	//     having INCREMENTAL set means we CAN later, without a 30s
	//     full VACUUM).
	// Why not retrofit existing DBs: changing auto_vacuum mode requires
	// a full VACUUM (the docs are explicit), which on a 1GB-RAM machine
	// can stall every panel request for 10-60s. Not worth the risk for
	// what is essentially a future-proofing knob.
	if freshDB {
		if err := db.Exec("PRAGMA auto_vacuum = INCREMENTAL").Error; err != nil {
			// Non-fatal: worst case we lose the ability to incremental-
			// vacuum this DB later. Don't tank InitDB over it.
			_ = err
		}
		// auto_vacuum only takes effect after the first VACUUM following
		// the PRAGMA on an empty file. The DB IS empty (we just stat'd
		// no-such-file above) so this is fast, no real data to rewrite.
		_ = db.Exec("VACUUM").Error
	}
	return runMigrations(db)
}

func GetDB() *gorm.DB {
	return db
}

func IsNotFound(err error) bool {
	return err == gorm.ErrRecordNotFound
}
