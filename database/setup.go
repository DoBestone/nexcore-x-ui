package database

import (
	"errors"
	"fmt"
	"os"
	"path"
	"time"

	"gorm.io/gorm"

	"nexcore-x-ui/database/model"
	"nexcore-x-ui/util/crypto"
	"nexcore-x-ui/util/random"
)

const installInfoFilename = "install-info.txt"

// FirstRunInfo describes what happened during a first-run setup pass.
// Generated == false means "already configured, nothing to do".
type FirstRunInfo struct {
	Generated bool
	Username  string
	Password  string // plaintext; only valid for the lifetime of this call
	Port      int
	InfoPath  string
}

// RunFirstRunSetup is called once after migrations on every server start.
// On the very first run (no users + no webPort setting) it generates a
// random username, password and panel port; persists them; and writes a
// human-readable install-info.txt next to the database. The install script
// can grep this file to print the credentials to the operator.
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

	// install-info.txt is a *convenience snapshot* — operators love being
	// able to `cat` the file rather than scrape journalctl. But the actual
	// source of truth is the DB row we just wrote (admin user) and the
	// fact that GetFirstUser will return that user from now on.
	//
	// Earlier versions returned (info, err) on file-write failure, which
	// caused the runWebServer caller to skip the credentials banner
	// entirely — leaving operators with NO way to recover the random
	// password short of running `nexcore-x-ui reset`. We now degrade
	// gracefully: file-write failure is logged but does not poison
	// info.Generated, so the caller still prints the banner to stdout
	// (which systemd captures into journalctl, where it is greppable).
	if err := writeInstallInfo(dbPath, info); err != nil {
		// Caller logs to logger; we annotate the info path so the banner
		// can show "(write to disk failed; this banner is the only copy)"
		// and the operator knows to record it now.
		info.InfoPath = ""
		return info, fmt.Errorf("write install-info.txt failed (banner is the only copy of the password): %w", err)
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

func writeInstallInfo(dbPath string, info *FirstRunInfo) error {
	dir := path.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fp := path.Join(dir, installInfoFilename)
	info.InfoPath = fp
	body := fmt.Sprintf(`NexCore x-ui · install info
generated: %s

panel port: %d
username:   %s
password:   %s

The login URL is http://<server-ip>:%d
You can change all of these from "面板设置" after the first login.
This file is mode 0600 — delete it once you've recorded the values.
`, time.Now().Format(time.RFC3339), info.Port, info.Username, info.Password, info.Port)
	return os.WriteFile(fp, []byte(body), 0o600)
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

// RemoveInstallInfo deletes the install-info.txt snapshot if present. Called
// after operator-driven mutations (password / port change) since the snapshot
// can no longer be reconstructed from a bcrypt hash and is misleading once
// values have diverged. Errors are intentionally swallowed (best-effort).
func RemoveInstallInfo() {
	if savedDBPath == "" {
		return
	}
	fp := path.Join(path.Dir(savedDBPath), installInfoFilename)
	_ = os.Remove(fp)
}

// installInfoMaxAge is the soft TTL of install-info.txt. The file is
// 0600 and only contains the *initial* admin credentials, but operators
// frequently forget to delete it; on a long-lived host the cleartext
// password lingers in the panel's data directory indefinitely. After
// the TTL elapses we delete the file on the next boot — by then the
// admin has either logged in (in which case they know the password) or
// the panel is unattended and a stale file is more dangerous than
// helpful.
//
// 24h is a deliberate compromise: long enough that an operator can run
// the installer, walk away, come back the next morning, and still find
// their initial credentials; short enough that "I'll deal with it later"
// doesn't turn into "still there a year later".
const installInfoMaxAge = 24 * time.Hour

// MaybeExpireInstallInfo deletes install-info.txt if it is older than
// installInfoMaxAge. Called once at startup right after first-run setup.
// Quietly does nothing if the file is missing, fresh, or unreadable —
// the file is best-effort to begin with.
func MaybeExpireInstallInfo(dbPath string) {
	if dbPath == "" {
		return
	}
	fp := path.Join(path.Dir(dbPath), installInfoFilename)
	info, err := os.Stat(fp)
	if err != nil {
		return
	}
	if time.Since(info.ModTime()) < installInfoMaxAge {
		return
	}
	_ = os.Remove(fp)
}

// PreserveFirstRunInfoOnce reads install-info.txt back so a tool like the
// install.sh wrapper can echo the credentials after the binary started.
// Returns nil, nil when the file does not exist.
func PreserveFirstRunInfoOnce(dbPath string) (*FirstRunInfo, error) {
	fp := path.Join(path.Dir(dbPath), installInfoFilename)
	if _, err := os.Stat(fp); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	// We do not parse the file (it is human-formatted). install.sh just
	// cats it.
	return &FirstRunInfo{InfoPath: fp}, nil
}

// Compile-time guard against accidental gorm drift.
var _ *gorm.DB = db
