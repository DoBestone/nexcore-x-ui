package database

import (
	"errors"
	"fmt"
	"os"
	"path"

	"gorm.io/gorm"

	"nexcore-x-ui/database/model"
	"nexcore-x-ui/util/crypto"
	"nexcore-x-ui/util/random"
)

// legacyInstallInfoFilename was the on-disk plaintext credential snapshot
// dropped at <data-dir>/install-info.txt by versions ≤ v2.1.2 on first
// run. Starting v2.1.3 the binary never creates this file: the credentials
// banner is printed to stdout (which systemd captures into journalctl,
// the canonical spot operators look) and that's the single copy. Keeping
// a 0600 file around with the plaintext password — even with a 24h
// auto-expire — was an unforced disclosure surface (backup copies,
// container snapshots, CI logs, BackupShell PITR tools).
//
// CleanupLegacyInstallInfo deletes the file on every startup so an
// in-place upgrade from v2.1.x → v2.1.3 doesn't leave stale plaintext
// behind. The function is best-effort: errors are intentionally swallowed
// because failure here just means the file lives one more boot, never a
// regression in correctness.
const legacyInstallInfoFilename = "install-info.txt"

// FirstRunInfo describes what happened during a first-run setup pass.
// Generated == false means "already configured, nothing to do".
type FirstRunInfo struct {
	Generated bool
	Username  string
	Password  string // plaintext; only valid for the lifetime of this call
	Port      int
}

// RunFirstRunSetup is called once after migrations on every server start.
// On the very first run (no users + no webPort setting) it generates a
// random username, password and panel port and persists them. The caller
// is responsible for surfacing the credentials to the operator —
// runWebServer prints the banner to stdout where systemd routes it into
// journalctl, the only place those credentials live going forward.
//
// On subsequent runs it returns Generated:false and does nothing.
func RunFirstRunSetup(dbPath string) (*FirstRunInfo, error) {
	if db == nil {
		return nil, errors.New("database not initialized")
	}

	var userCount int64
	if err := db.Model(&model.User{}).Count(&userCount).Error; err != nil {
		return nil, err
	}

	portMissing, currentPort := !hasSetting("webPort"), readSettingInt("webPort", 0)

	if userCount > 0 && !portMissing {
		return &FirstRunInfo{Generated: false, Port: currentPort}, nil
	}

	info := &FirstRunInfo{Generated: true}

	if userCount == 0 {
		info.Username = "admin_" + random.Seq(6)
		info.Password = random.Seq(16)
		hash, err := crypto.HashPassword(info.Password)
		if err != nil {
			return nil, err
		}
		if err := db.Create(&model.User{
			Username: info.Username,
			Password: hash,
		}).Error; err != nil {
			return nil, err
		}
	} else {
		info.Username = "(unchanged)"
		info.Password = "(unchanged)"
	}

	if portMissing {
		// Avoid privileged ports and a few common ones that conflict with
		// services the panel host is likely also running.
		info.Port = randomFreePortCandidate()
		if err := db.Create(&model.Setting{
			Key:   "webPort",
			Value: fmt.Sprintf("%d", info.Port),
		}).Error; err != nil {
			return nil, err
		}
	} else {
		info.Port = currentPort
	}

	return info, nil
}

func randomFreePortCandidate() int {
	// Range chosen to skip ephemeral ranges and most well-known apps.
	const lo, hi = 20000, 59999
	skip := map[int]bool{
		22: true, 53: true, 80: true, 443: true,
		3306: true, 5432: true, 6379: true, 27017: true,
		54321: true, // legacy x-ui default
	}
	for i := 0; i < 30; i++ {
		p := lo + random.IntN(hi-lo+1)
		if !skip[p] {
			return p
		}
	}
	return 38421 // deterministic fallback
}

func hasSetting(key string) bool {
	var s model.Setting
	err := db.Where("`key` = ?", key).First(&s).Error
	return err == nil
}

func readSettingInt(key string, fallback int) int {
	var s model.Setting
	if err := db.Where("`key` = ?", key).First(&s).Error; err != nil {
		return fallback
	}
	var n int
	if _, err := fmt.Sscanf(s.Value, "%d", &n); err != nil {
		return fallback
	}
	return n
}

// CleanupLegacyInstallInfo wipes the legacy install-info.txt file dropped
// by versions ≤ v2.1.2. Called once at startup. Best-effort: a failure
// here just means the file survives one more boot, no correctness impact.
//
// Kept as a separate function (vs. inlined in runWebServer) so any future
// caller that wants to scrub the file out-of-band — say a CLI subcommand
// or a forensics audit — has a stable entry point.
func CleanupLegacyInstallInfo(dbPath string) {
	if dbPath == "" {
		return
	}
	fp := path.Join(path.Dir(dbPath), legacyInstallInfoFilename)
	_ = os.Remove(fp)
}

// Compile-time guard against accidental gorm drift.
var _ *gorm.DB = db
