// Package secret provides transparent at-rest encryption for the
// handful of sensitive values the panel persists in the settings table:
// the Telegram bot token, the legacy single API token, and the cookie
// HMAC secret. The DB itself is sqlite — anyone with read on the file
// (or a full backup tarball) gets every row in plaintext, which the
// audit flagged as a HIGH risk.
//
// Design (post-2026-05-07 audit P2 hardening):
//   - 32-byte AES-256-GCM key derived from a host-bound seed via HKDF.
//   - Seed is sha256(machine-id || secret-key-bytes), where:
//       * machine-id comes from /etc/machine-id (or the dbus fallback)
//         — preserved across reboots, unique per host, but typically
//         0644 / world-readable on most distros.
//       * secret-key-bytes lives at <data-dir>/.secret-key, 32 random
//         bytes generated on first run, file mode 0600 — readable only
//         by the panel user (root in production).
//     The two-salt construction means a read-only attacker who can grab
//     /etc/machine-id (e.g. an SFTP-only user, monitoring agent) still
//     can't decrypt a leaked DB backup unless they also got the
//     0600-protected .secret-key. The old "machine-id alone" seed is
//     still trusted for backward compatibility — see Decrypt.
//   - Stored values use the prefix "enc:v1:" or "enc:v2:" + base64
//     (nonce||ct||tag). v1 uses the legacy machine-id-only seed; v2
//     uses the dual-salt seed. Encrypt always emits v2; Decrypt accepts
//     both (and falls back to "no prefix = legacy plaintext").
//
// Threat model: protects DB-leak / backup-leak even when the attacker
// also has read on /etc/machine-id but NOT on .secret-key (the typical
// "shared host with multiple unprivileged readers" scenario). It does
// not protect against an attacker with code execution as the panel user
// — they can read .secret-key directly.
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
	prefixV1   = "enc:v1:" // legacy: machine-id-only seed
	prefixV2   = "enc:v2:" // current: machine-id + .secret-key dual-salt
	hkdfSalt   = "nexcore-x-ui-secret-v1"
	hkdfInfo   = "settings-aead-v1"
	keyLen     = 32 // AES-256
	seedLen    = 32 // bytes of host-bound entropy
	machineID  = "/etc/machine-id"
	dbusMachID = "/var/lib/dbus/machine-id"
)

// aeadCache memoizes the GCM cipher for each version. Both are safe to
// build lazily on first use.
type aeadCache struct {
	once sync.Once
	gcm  cipher.AEAD
	err  error
}

var (
	v1AEAD aeadCache // legacy seed (machine-id only)
	v2AEAD aeadCache // dual-salt seed
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

// readMachineID returns the trimmed machine-id contents or "" when both
// canonical paths are missing/short. We use sha256 below to bring any
// length to 32 bytes, so the raw value just needs to be SOMETHING.
func readMachineID() string {
	for _, p := range []string{machineID, dbusMachID} {
		if b, err := os.ReadFile(p); err == nil {
			s := strings.TrimSpace(string(b))
			if len(s) >= 8 {
				return s
			}
		}
	}
	return ""
}

// readOrCreateSecretKey returns the contents of <data-dir>/.secret-key,
// creating it (0600, 32 random bytes) if it doesn't exist. Always
// returns 32 bytes on success — the file MAY be longer, in which case
// we trim down so the dual-salt input is a fixed size.
func readOrCreateSecretKey() ([]byte, error) {
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

// loadSeedV1 — legacy seed material: sha256(machine-id) when available,
// otherwise the .secret-key file contents (the v1.x fallback path).
// Used ONLY for decrypting values written before the dual-salt change.
func loadSeedV1() ([]byte, error) {
	if id := readMachineID(); id != "" {
		h := sha256.Sum256([]byte(id))
		return h[:], nil
	}
	// On a host without machine-id the legacy code already used the
	// .secret-key file as the sole seed; preserve that exactly.
	return readOrCreateSecretKey()
}

// loadSeedV2 — dual-salt seed: sha256(machine-id || secret-key-bytes).
// The secret-key file is always present (created on first call); the
// machine-id half MAY be empty on bizarre hosts, in which case we fall
// back to secret-key-only (still better than v1 because secret-key is
// 0600).
func loadSeedV2() ([]byte, error) {
	skey, err := readOrCreateSecretKey()
	if err != nil {
		return nil, err
	}
	id := readMachineID()
	mat := make([]byte, 0, len(id)+seedLen)
	mat = append(mat, []byte(id)...)
	mat = append(mat, skey...)
	h := sha256.Sum256(mat)
	return h[:], nil
}

// gcmFor builds an AES-256-GCM cipher from the supplied seed using
// HKDF-SHA256 to derive a key. Pure mechanical — version-independent.
func gcmFor(seed []byte) (cipher.AEAD, error) {
	key := make([]byte, keyLen)
	r := hkdf.New(sha256.New, seed, []byte(hkdfSalt), []byte(hkdfInfo))
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("hkdf: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func aeadV1() (cipher.AEAD, error) {
	v1AEAD.once.Do(func() {
		seed, err := loadSeedV1()
		if err != nil {
			v1AEAD.err = err
			return
		}
		v1AEAD.gcm, v1AEAD.err = gcmFor(seed)
	})
	return v1AEAD.gcm, v1AEAD.err
}

func aeadV2() (cipher.AEAD, error) {
	v2AEAD.once.Do(func() {
		seed, err := loadSeedV2()
		if err != nil {
			v2AEAD.err = err
			return
		}
		v2AEAD.gcm, v2AEAD.err = gcmFor(seed)
	})
	return v2AEAD.gcm, v2AEAD.err
}

// Encrypt wraps plaintext into the on-disk envelope. Empty input is
// passed through unchanged: storing "enc:v2:..." for an empty value
// would just inflate the row for no security benefit. New writes
// always emit v2 — Decrypt will read both v1 and v2.
func Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	g, err := aeadV2()
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
	return prefixV2 + base64.RawStdEncoding.EncodeToString(out), nil
}

// Decrypt unwraps the on-disk envelope. Values without a known prefix
// are returned as-is so legacy plaintext rows keep working until the
// next write rewraps them. v1 envelopes are still readable; v2 is the
// current write format.
func Decrypt(stored string) (string, error) {
	switch {
	case strings.HasPrefix(stored, prefixV2):
		return decryptWith(stored[len(prefixV2):], aeadV2)
	case strings.HasPrefix(stored, prefixV1):
		return decryptWith(stored[len(prefixV1):], aeadV1)
	default:
		// Legacy plaintext / unencrypted value.
		return stored, nil
	}
}

func decryptWith(b64 string, build func() (cipher.AEAD, error)) (string, error) {
	g, err := build()
	if err != nil {
		return "", err
	}
	raw, err := base64.RawStdEncoding.DecodeString(b64)
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

// IsEncrypted reports whether stored is in any of the known on-disk
// envelope formats. Used by callers that want to opportunistically
// rewrap on read.
func IsEncrypted(stored string) bool {
	return strings.HasPrefix(stored, prefixV1) || strings.HasPrefix(stored, prefixV2)
}
