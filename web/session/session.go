package session

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"

	"nexcore-x-ui/database/model"
)

// What goes into the cookie:
//
//	{ "uid": <int>, "sig": <16-byte hex of SHA256("nx-session|"||bcryptHash)> }
//
// Earlier versions gob-encoded the entire model.User struct (bcrypt hash,
// LoginSecret, etc.) into the cookie. gorilla/sessions cookies are HMAC-
// signed but NOT encrypted, so anyone reading a cookie could dump the
// admin's bcrypt hash for offline cracking. By storing only an ID plus a
// 16-byte fingerprint of the password hash:
//
//   - the cookie reveals no usable secret (id alone is fine, the sig is
//     a one-way fingerprint of an already-hashed password);
//   - changing the password rotates the fingerprint, so all existing
//     cookies fail their `LoadCurrentUser` check on the next request —
//     same "rotate password = log everyone out" property base.go used
//     to enforce by full-bcrypt comparison.
//
// The cache key on gin.Context lets repeated calls to GetLoginUser
// inside the same request (controller/inbound.go etc.) avoid hitting
// SQLite again — checkLogin already paid for the lookup.
const (
	keyUserID  = "uid"
	keyUserSig = "sig"

	cacheKey = "_nx_login_user"
)

// passwordSig fingerprints a bcrypt hash (or any password-derived
// material) into 16 bytes hex. We prefix with a domain string to make
// this hash unusable as input to any other comparison that might one
// day take a SHA256 of `user.Password`.
func passwordSig(passwordHash string) string {
	sum := sha256.Sum256([]byte("nx-session|" + passwordHash))
	return hex.EncodeToString(sum[:16])
}

// SetLoginUser writes the post-login session and primes the per-request
// cache so the same request can call GetLoginUser without a DB roundtrip.
func SetLoginUser(c *gin.Context, user *model.User) error {
	s := sessions.Default(c)
	s.Set(keyUserID, user.Id)
	s.Set(keyUserSig, passwordSig(user.Password))
	if err := s.Save(); err != nil {
		return err
	}
	c.Set(cacheKey, user)
	return nil
}

// SnapshotID exposes just the user id stored in the cookie, without
// touching the DB. Used by logout (which only needs the id for the
// audit log line) so we don't pay for a DB lookup that ClearSession
// would invalidate immediately afterwards anyway.
func SnapshotID(c *gin.Context) (int, bool) {
	id, _, ok := snapshot(c)
	return id, ok
}

// snapshot reads the (id, sig) tuple out of the cookie. Returns
// (0, "", false) if the cookie is empty or the types don't match (which
// can happen after we change the cookie format — the legacy cookie's gob
// blob deserializes to model.User, not int).
func snapshot(c *gin.Context) (int, string, bool) {
	s := sessions.Default(c)
	idV := s.Get(keyUserID)
	sigV := s.Get(keyUserSig)
	if idV == nil || sigV == nil {
		return 0, "", false
	}
	id, ok1 := idV.(int)
	sig, ok2 := sigV.(string)
	if !ok1 || !ok2 {
		return 0, "", false
	}
	return id, sig, true
}

// LoadCurrentUser verifies the cookie against the live admin row and,
// on success, caches the row on gin.Context. Called once per request
// from base.go::checkLogin; downstream handlers use GetLoginUser to
// read the cached value without re-querying the DB.
//
// `getFirstUser` is injected so the session package doesn't have to
// import the service layer (avoids a cycle).
func LoadCurrentUser(c *gin.Context, getFirstUser func() (*model.User, error)) *model.User {
	if cached, ok := c.Get(cacheKey); ok {
		if u, ok2 := cached.(*model.User); ok2 {
			return u
		}
	}
	id, sig, ok := snapshot(c)
	if !ok {
		return nil
	}
	cur, err := getFirstUser()
	if err != nil || cur == nil || cur.Id != id {
		return nil
	}
	if passwordSig(cur.Password) != sig {
		return nil
	}
	c.Set(cacheKey, cur)
	return cur
}

// GetLoginUser returns the cached user populated by LoadCurrentUser
// (which checkLogin runs as middleware on every authenticated route).
// If the cache is empty — handler reachable without checkLogin in front
// of it — returns nil; callers must handle nil rather than dereferencing.
func GetLoginUser(c *gin.Context) *model.User {
	if cached, ok := c.Get(cacheKey); ok {
		if u, ok2 := cached.(*model.User); ok2 {
			return u
		}
	}
	return nil
}

func IsLogin(c *gin.Context) bool {
	return GetLoginUser(c) != nil
}

func ClearSession(c *gin.Context) {
	s := sessions.Default(c)
	s.Clear()
	s.Options(sessions.Options{
		Path:   "/",
		MaxAge: -1,
	})
	_ = s.Save()
	c.Set(cacheKey, nil)
}
