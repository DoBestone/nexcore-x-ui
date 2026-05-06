// Package secret provides transparent at-rest encryption for the
// handful of sensitive values the panel persists in the settings table:
// the Telegram bot token, the legacy single API token, and the cookie
// HMAC secret. The DB itself is sqlite — anyone with read on the file
// (or a full backup tarball) gets every row in plaintext, which the
// audit flagged as a HIGH risk.
//
// Design:
//   - 32-byte AES-256-GCM key derived from a host-bound seed via HKDF.
//   - Seed is /etc/machine-id when available (preserved across reboots,
//     unique per host). When absent, fall back to a random 32 bytes
//     written to <data-dir>/.secret-key with mode 0600 — same effect
//     as long as the data dir survives.
//   - Stored values use the prefix "enc:v1:" + base64(nonce||ct||tag).
//     Anything without the prefix is treated as legacy plaintext and
//     returned as-is, so this drops in next to existing rows. On the
//     first write back the value is re-encrypted automatically.
//
// Threat model: protects DB-leak / backup-leak. It does NOT protect
// against an attacker with code execution on the panel host (they can
// read /etc/machine-id), which is by design — the panel runs as root
// and that's a separate threat already covered elsewhere.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/hkdf"
)

const (
	prefix     = "enc:v1:"
	hkdfSalt   = "nexcore-x-ui-secret-v1"
	hkdfInfo   = "settings-aead-v1"
	keyLen     = 32 // AES-256
	seedLen    = 32 // bytes of host-bound entropy
	machineID  = "/etc/machine-id"
	dbusMachID = "/var/lib/dbus/machine-id"
)

var (
	once   sync.Once
	cached cipher.AEAD
	initEr error
)

// dataDirForFallback is set by InitFromDataDir and used as the location
// for the .secret-key fallback file when /etc/machine-id is unavailable.
// Tests / non-Linux runs that don't call InitFromDataDir get a temp dir.
var (
	dataDirMu  sync.Mutex
	dataDirVal string
)

// InitFromDataDir tells the package where to put .secret-key when
// /etc/machine-id isn't usable. Called from main.go after the DB path
// is resolved. Safe to call multiple times.
func InitFromDataDir(dir string) {
	dataDirMu.Lock()
	dataDirVal = dir
	dataDirMu.Unlock()
}

func dataDir() string {
	dataDirMu.Lock()
	d := dataDirVal
	dataDirMu.Unlock()
	if d == "" {
		d = os.TempDir()
	}
	return d
}

func aead() (cipher.AEAD, error) {
	once.Do(func() {
		seed, err := loadSeed()
		if err != nil {
			initEr = err
			return
		}
		key := make([]byte, keyLen)
		r := hkdf.New(sha256.New, seed, []byte(hkdfSalt), []byte(hkdfInfo))
		if _, err := io.ReadFull(r, key); err != nil {
			initEr = fmt.Errorf("hkdf: %w", err)
			return
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			initEr = err
			return
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			initEr = err
			return
		}
		cached = gcm
	})
	if initEr != nil {
		return nil, initEr
	}
	return cached, nil
}

func loadSeed() ([]byte, error) {
	for _, p := range []string{machineID, dbusMachID} {
		if b, err := os.ReadFile(p); err == nil {
			s := strings.TrimSpace(string(b))
			if len(s) >= 8 {
				// Hash to a 32-byte seed regardless of input length.
				h := sha256.Sum256([]byte(s))
				return h[:], nil
			}
		}
	}
	// Fallback: random key file in data dir, 0600.
	keyPath := filepath.Join(dataDir(), ".secret-key")
	if b, err := os.ReadFile(keyPath); err == nil && len(b) >= seedLen {
		return b[:seedLen], nil
	}
	if err := os.MkdirAll(dataDir(), 0o700); err != nil {
		return nil, fmt.Errorf("ensure data dir: %w", err)
	}
	seed := make([]byte, seedLen)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	tmp := keyPath + ".tmp"
	if err := os.WriteFile(tmp, seed, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, keyPath); err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	return seed, nil
}

// Encrypt wraps plaintext into the on-disk envelope. Empty input is
// passed through unchanged: storing "enc:v1:..." for an empty value
// would just inflate the row for no security benefit.
func Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	g, err := aead()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := g.Seal(nil, nonce, []byte(plaintext), nil)
	out := make([]byte, 0, len(nonce)+len(ct))
	out = append(out, nonce...)
	out = append(out, ct...)
	return prefix + base64.RawStdEncoding.EncodeToString(out), nil
}

// Decrypt unwraps the on-disk envelope. Values without the prefix are
// returned as-is so legacy plaintext rows keep working until the next
// write rewraps them.
func Decrypt(stored string) (string, error) {
	if !strings.HasPrefix(stored, prefix) {
		return stored, nil
	}
	g, err := aead()
	if err != nil {
		return "", err
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(stored, prefix))
	if err != nil {
		return "", fmt.Errorf("secret: bad base64: %w", err)
	}
	if len(raw) < g.NonceSize()+g.Overhead() {
		return "", errors.New("secret: ciphertext too short")
	}
	nonce, ct := raw[:g.NonceSize()], raw[g.NonceSize():]
	pt, err := g.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("secret: decrypt: %w", err)
	}
	return string(pt), nil
}

// IsEncrypted reports whether stored is in the on-disk envelope format.
// Used by callers that want to opportunistically rewrap on read.
func IsEncrypted(stored string) bool {
	return strings.HasPrefix(stored, prefix)
}
