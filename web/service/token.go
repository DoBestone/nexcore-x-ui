package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"nexcore-x-ui/database"
	"nexcore-x-ui/database/model"
	"nexcore-x-ui/util/random"
)

const tokenLength = 48

// Scope values stored in api_tokens.scope. Anything outside this set
// is treated as "no access" by middleware — fail-closed.
const (
	ScopeAdmin        = "admin"
	ScopeReadOnly     = "readonly"
	ScopeSubscription = "subscription"
)

func isValidScope(s string) bool {
	switch s {
	case ScopeAdmin, ScopeReadOnly, ScopeSubscription:
		return true
	}
	return false
}

var (
	ErrTokenNameRequired = errors.New("token name is required")
	ErrTokenNotFound     = errors.New("token not found")
	ErrInvalidScope      = errors.New("invalid token scope (allowed: admin, readonly, subscription)")
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

// CreateToken mints a new token. scope defaults to ScopeAdmin when
// empty so legacy callers that pre-date the scope concept keep working.
// ttl is the lifetime; pass 0 for "never expires" (preserves the old
// default). The plaintext is returned exactly once in
// CreatedToken.Plaintext; the row stored in the DB carries the SHA256
// hash, never the plaintext.
func (s *APITokenService) CreateToken(name, scope string, ttl time.Duration) (*CreatedToken, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, ErrTokenNameRequired
	}
	if scope == "" {
		scope = ScopeAdmin
	}
	if !isValidScope(scope) {
		return nil, ErrInvalidScope
	}
	if ttl < 0 {
		ttl = 0
	}
	plain := random.Seq(tokenLength)
	now := time.Now()
	expires := int64(0)
	if ttl > 0 {
		expires = now.Add(ttl).Unix()
	}
	t := &model.APIToken{
		Name:      name,
		Token:     hashToken(plain),
		CreatedAt: now.Unix(),
		Scope:     scope,
		ExpiresAt: expires,
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
// ErrTokenNotFound when no active row matches OR when the row has
// expired; the auth middleware can't tell the two apart, which is by
// design (no oracle for "this token used to exist").
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
	if out.ExpiresAt > 0 && out.ExpiresAt < time.Now().Unix() {
		return nil, ErrTokenNotFound
	}
	return out, nil
}

// touchBatch coalesces last_used_at writes. Each authenticated request
// only stamps an in-memory map; one background worker flushes to the DB
// every flushInterval seconds. The previous design forked a goroutine
// per request — at 1000 QPS that's 1000 goroutines/second creating
// 1000 DB writes/second, all of which can be collapsed to a single
// batch UPDATE per id with no loss of useful information (LastUsedAt
// resolution is seconds anyway).
var (
	touchOnce sync.Once
	touchMu   sync.Mutex
	// touchPending: map[token id] -> latest unix-seconds timestamp seen
	// for that token in the current batch window.
	touchPending = map[int]int64{}
)

const touchFlushInterval = 30 * time.Second

// TouchLastUsed records the current timestamp for the given token id.
// Cheap and non-blocking: just a map insert under a tiny mutex. The
// background flusher coalesces concurrent updates into a single batch
// UPDATE per token per window.
func (s *APITokenService) TouchLastUsed(id int) {
	touchOnce.Do(startTouchFlusher)
	now := time.Now().Unix()
	touchMu.Lock()
	if prev := touchPending[id]; prev < now {
		touchPending[id] = now
	}
	touchMu.Unlock()
}

func startTouchFlusher() {
	go func() {
		t := time.NewTicker(touchFlushInterval)
		defer t.Stop()
		for range t.C {
			flushTouchBatch()
		}
	}()
}

func flushTouchBatch() {
	touchMu.Lock()
	if len(touchPending) == 0 {
		touchMu.Unlock()
		return
	}
	batch := touchPending
	touchPending = make(map[int]int64, len(batch))
	touchMu.Unlock()

	db := database.GetDB()
	// Group by timestamp so we collapse runs of the same second into a
	// single UPDATE WHERE id IN (...). The inner map is small (one
	// entry per distinct second seen in the window), so the resulting
	// statement count is O(window seconds) and effectively O(1) for
	// realistic loads.
	byTs := map[int64][]int{}
	for id, ts := range batch {
		byTs[ts] = append(byTs[ts], id)
	}
	for ts, ids := range byTs {
		_ = db.Model(&model.APIToken{}).
			Where("id IN ?", ids).
			Update("last_used_at", ts).Error
	}
}
