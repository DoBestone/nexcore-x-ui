package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"gorm.io/gorm"

	"nexcore-x-ui/database"
	"nexcore-x-ui/database/model"
	"nexcore-x-ui/util/random"
)

const (
	magicTokenLength     = 40
	magicTokenDefaultTTL = 10 * time.Minute
	magicTokenMaxTTL     = 24 * time.Hour
	magicTokenGCAfter    = 7 * 24 * time.Hour
)

var (
	// ErrMagicTokenInvalid is the only error ConsumeMagicToken returns
	// to the HTTP layer. The expired path is folded into "invalid" so
	// an attacker can't distinguish "this token existed once and timed
	// out" from "this token never existed" via the response. The
	// audit-log distinction lives inside the consume routine and the
	// magic_tokens row's expires_at column.
	ErrMagicTokenInvalid = errors.New("magic token invalid or already used")
)

// CreatedMagicToken carries the plaintext returned to the operator at
// creation time. Plaintext is intentionally only available here — the
// stored row carries the SHA256 hash, never the secret. Same model as
// APITokenService.
type CreatedMagicToken struct {
	Row       *model.MagicToken
	Plaintext string
}

type MagicTokenService struct{}

// hashMagicToken hex-encodes SHA256(plain). We store this in
// magic_tokens.token instead of the plaintext so a leaked DB doesn't
// give an attacker a 10-minute login window for every unused row. Plain
// SHA256 is sufficient because the plaintext is 40 chars of high-entropy
// random output (random.Seq) — bcrypt cost would be a no-op against that
// search space and slow down every link click.
func hashMagicToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// CreateMagicToken issues a single-use, time-bounded login token. ttl is
// clamped to [1m, 24h]; default 10m. Note is free-form context for audit
// (e.g. "remote support 2026-05-06"). The plaintext is returned only via
// CreatedMagicToken.Plaintext — the row stored in the DB carries the hash.
func (s *MagicTokenService) CreateMagicToken(ttl time.Duration, note string) (*CreatedMagicToken, error) {
	if ttl <= 0 {
		ttl = magicTokenDefaultTTL
	}
	if ttl < time.Minute {
		ttl = time.Minute
	}
	if ttl > magicTokenMaxTTL {
		ttl = magicTokenMaxTTL
	}
	plain := random.Seq(magicTokenLength)
	now := time.Now()
	t := &model.MagicToken{
		Token:     hashMagicToken(plain),
		CreatedAt: now.Unix(),
		ExpiresAt: now.Add(ttl).Unix(),
		Note:      note,
	}
	db := database.GetDB()
	if err := db.Create(t).Error; err != nil {
		return nil, err
	}
	return &CreatedMagicToken{Row: t, Plaintext: plain}, nil
}

// ConsumeMagicToken validates and atomically marks the token as
// consumed. Always returns ErrMagicTokenInvalid on any failure mode so
// the HTTP layer can't leak "this token existed once" vs "this token
// never existed" via the response. The internal expiry path still
// marks the row consumed so a reuse attempt won't reveal the prior
// state via a different code path.
//
// `plain` is the URL-side plaintext token. We hash it before lookup so
// the DB never sees the plaintext.
func (s *MagicTokenService) ConsumeMagicToken(plain string) error {
	if plain == "" {
		return ErrMagicTokenInvalid
	}
	hashed := hashMagicToken(plain)
	db := database.GetDB()
	var t model.MagicToken
	err := db.Where("token = ?", hashed).First(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrMagicTokenInvalid
	}
	if err != nil {
		return err
	}
	if t.ConsumedAt != 0 {
		return ErrMagicTokenInvalid
	}
	now := time.Now().Unix()
	expired := now > t.ExpiresAt
	res := db.Model(&model.MagicToken{}).
		Where("id = ? AND consumed_at = 0", t.Id).
		Update("consumed_at", now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 || expired {
		// expired tokens are now reported as invalid externally; the
		// internal logger.Info call in handleMagicLogin can still
		// distinguish if needed for audit.
		return ErrMagicTokenInvalid
	}
	return nil
}

// PurgeOldMagicTokens deletes consumed/expired rows older than magicTokenGCAfter.
// Called from the cron loop so the table doesn't grow unboundedly.
func (s *MagicTokenService) PurgeOldMagicTokens() (int64, error) {
	cutoff := time.Now().Add(-magicTokenGCAfter).Unix()
	res := database.GetDB().
		Where("consumed_at != 0 AND consumed_at < ?", cutoff).
		Or("expires_at < ?", cutoff).
		Delete(&model.MagicToken{})
	return res.RowsAffected, res.Error
}
