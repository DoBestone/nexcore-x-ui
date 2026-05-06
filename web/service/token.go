package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"nexcore-x-ui/database"
	"nexcore-x-ui/database/model"
	"nexcore-x-ui/util/random"
)

const tokenLength = 48

var (
	ErrTokenNameRequired = errors.New("token name is required")
	ErrTokenNotFound     = errors.New("token not found")
)

// CreatedToken is the one-shot view returned by CreateToken: the row
// stored in the DB carries hex(SHA256(plaintext)) in the Token column,
// so the plaintext exists only here, only once.
type CreatedToken struct {
	Row       *model.APIToken
	Plaintext string
}

type APITokenService struct{}

// hashToken returns the on-disk representation of a plaintext token —
// hex-encoded SHA256. We deliberately use a plain hash instead of bcrypt
// because tokens are 48 chars of high-entropy random output, so rainbow
// tables / GPU brute force are not in the threat model; what matters is
// that a DB leak doesn't immediately compromise live API access.
func hashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// CreateToken mints a new token. The plaintext is returned exactly once
// in CreatedToken.Plaintext; the row stored in the DB carries the SHA256
// hash, never the plaintext.
func (s *APITokenService) CreateToken(name string) (*CreatedToken, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrTokenNameRequired
	}
	plain := random.Seq(tokenLength)
	t := &model.APIToken{
		Name:      name,
		Token:     hashToken(plain),
		CreatedAt: time.Now().Unix(),
	}
	db := database.GetDB()
	if err := db.Create(t).Error; err != nil {
		return nil, err
	}
	return &CreatedToken{Row: t, Plaintext: plain}, nil
}

func (s *APITokenService) ListTokens() ([]*model.APIToken, error) {
	db := database.GetDB()
	var tokens []*model.APIToken
	err := db.Order("id asc").Find(&tokens).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	return tokens, nil
}

func (s *APITokenService) RevokeToken(id int) error {
	db := database.GetDB()
	res := db.Model(&model.APIToken{}).Where("id = ?", id).Update("revoked", true)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrTokenNotFound
	}
	return nil
}

func (s *APITokenService) DeleteToken(id int) error {
	db := database.GetDB()
	res := db.Delete(&model.APIToken{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrTokenNotFound
	}
	return nil
}

func (s *APITokenService) RenameToken(id int, name string) (*model.APIToken, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrTokenNameRequired
	}
	db := database.GetDB()
	res := db.Model(&model.APIToken{}).Where("id = ?", id).Update("name", name)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrTokenNotFound
	}
	out := &model.APIToken{}
	if err := db.First(out, id).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// FindActiveByValue is the hot path called on every authenticated
// request. It hashes the incoming plaintext and looks up the row by
// hash — the plaintext never appears in any query. Returns
// ErrTokenNotFound when no active row matches.
//
// The lookup is by indexed equality on a SHA256 digest, which yields a
// uniform-time DB hit/miss profile: an attacker can't distinguish "no
// such token" from "wrong token" via response time, because both miss
// the unique index identically. (The previous design queried by
// plaintext, which leaks information through query timing on a B-tree.)
func (s *APITokenService) FindActiveByValue(value string) (*model.APIToken, error) {
	if value == "" {
		return nil, ErrTokenNotFound
	}
	db := database.GetDB()
	out := &model.APIToken{}
	err := db.Where("token = ? AND revoked = ?", hashToken(value), false).First(out).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrTokenNotFound
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// TouchLastUsed updates last_used_at without blocking the request handler.
// Failures are intentionally swallowed — a usage-tracking miss is not worth
// failing an authenticated call.
func (s *APITokenService) TouchLastUsed(id int) {
	go func() {
		db := database.GetDB()
		_ = db.Model(&model.APIToken{}).
			Where("id = ?", id).
			Update("last_used_at", time.Now().Unix()).Error
	}()
}
