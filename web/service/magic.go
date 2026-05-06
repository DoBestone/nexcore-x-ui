package service

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"x-ui/database"
	"x-ui/database/model"
	"x-ui/util/random"
)

const (
	magicTokenLength    = 40
	magicTokenDefaultTTL = 10 * time.Minute
	magicTokenMaxTTL     = 24 * time.Hour
	magicTokenGCAfter    = 7 * 24 * time.Hour
)

var (
	ErrMagicTokenInvalid = errors.New("magic token invalid or already used")
	ErrMagicTokenExpired = errors.New("magic token expired")
)

type MagicTokenService struct{}

// CreateMagicToken issues a single-use, time-bounded login token. ttl is
// clamped to [1m, 24h]; default 10m. Note is free-form context for audit
// (e.g. "remote support 2026-05-06").
func (s *MagicTokenService) CreateMagicToken(ttl time.Duration, note string) (*model.MagicToken, error) {
	if ttl <= 0 {
		ttl = magicTokenDefaultTTL
	}
	if ttl < time.Minute {
		ttl = time.Minute
	}
	if ttl > magicTokenMaxTTL {
		ttl = magicTokenMaxTTL
	}
	now := time.Now()
	t := &model.MagicToken{
		Token:     random.Seq(magicTokenLength),
		CreatedAt: now.Unix(),
		ExpiresAt: now.Add(ttl).Unix(),
		Note:      note,
	}
	db := database.GetDB()
	if err := db.Create(t).Error; err != nil {
		return nil, err
	}
	return t, nil
}

// ConsumeMagicToken validates and atomically marks the token as consumed.
// Returns ErrMagicTokenInvalid for unknown / already-used tokens, and
// ErrMagicTokenExpired for expired ones (the row is also marked consumed
// so it can't be reused by retrying after expiry).
func (s *MagicTokenService) ConsumeMagicToken(token string) error {
	if token == "" {
		return ErrMagicTokenInvalid
	}
	db := database.GetDB()
	var t model.MagicToken
	err := db.Where("token = ?", token).First(&t).Error
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
	// Always mark consumed — including on expiry — so a reuse attempt
	// after expiry doesn't leak whether the token was previously valid.
	res := db.Model(&model.MagicToken{}).
		Where("id = ? AND consumed_at = 0", t.Id).
		Update("consumed_at", now)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrMagicTokenInvalid
	}
	if expired {
		return ErrMagicTokenExpired
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
