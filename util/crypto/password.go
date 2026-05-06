package crypto

import (
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const bcryptPrefix = "$2"

// passwordCost is the bcrypt cost factor for new hashes. Higher than
// bcrypt.DefaultCost (10) — modern hardware can brute-force cost-10
// hashes meaningfully fast on weak passwords. 12 still fits well within
// our login budget (~150ms on a typical VPS) and triples the work
// factor relative to the default. Existing hashes at lower cost still
// verify; VerifyPassword's needsRehash flag lets callers transparently
// upgrade them on a successful login.
const passwordCost = 12

func IsBcryptHash(s string) bool {
	return strings.HasPrefix(s, bcryptPrefix)
}

func HashPassword(plain string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), passwordCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// VerifyPassword returns (ok, needsRehash). needsRehash is true when:
//   - the stored value is plaintext and matches (legacy migration), or
//   - the stored bcrypt hash uses a cost factor below passwordCost
//     (so a successful login transparently upgrades the work factor).
// The caller is expected to call HashPassword(plain) and persist the
// new hash on the success path.
func VerifyPassword(stored, plain string) (ok bool, needsRehash bool) {
	if stored == "" {
		return false, false
	}
	if IsBcryptHash(stored) {
		if err := bcrypt.CompareHashAndPassword([]byte(stored), []byte(plain)); err != nil {
			return false, false
		}
		// Cost lookup is cheap and only runs on a successful match.
		if cost, err := bcrypt.Cost([]byte(stored)); err == nil && cost < passwordCost {
			return true, true
		}
		return true, false
	}
	if stored == plain {
		return true, true
	}
	return false, false
}
