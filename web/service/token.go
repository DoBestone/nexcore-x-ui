package service

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"x-ui/database"
	"x-ui/database/model"
	"x-ui/util/random"
)

const tokenLength = 48

var (
	ErrTokenNameRequired = errors.New("token name is required")
	ErrTokenNotFound     = errors.New("token not found")
)

type APITokenService struct{}

// CreateToken mints a new token. The plaintext value is returned exactly once
// here — the caller must surface it to the operator immediately.
func (s *APITokenService) CreateToken(name string) (*model.APIToken, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrTokenNameRequired
	}
	t := &model.APIToken{
		Name:      name,
		Token:     random.Seq(tokenLength),
		CreatedAt: time.Now().Unix(),
	}
	db := database.GetDB()
	if err := db.Create(t).Error; err != nil {
		return nil, err
	}
	return t, nil
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

// FindActiveByValue is the hot path called on every authenticated request.
// It returns ErrTokenNotFound when no active row matches; this is also the
// signal for the auth middleware to fall back to the legacy single-token
// stored in the settings table.
func (s *APITokenService) FindActiveByValue(value string) (*model.APIToken, error) {
	if value == "" {
		return nil, ErrTokenNotFound
	}
	db := database.GetDB()
	out := &model.APIToken{}
	err := db.Where("token = ? AND revoked = ?", value, false).First(out).Error
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
