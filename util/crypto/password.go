package crypto

import (
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const bcryptPrefix = "$2"

func IsBcryptHash(s string) bool {
	return strings.HasPrefix(s, bcryptPrefix)
}

func HashPassword(plain string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// VerifyPassword returns (ok, needsRehash).
// needsRehash is true when the stored value is plaintext and matches; the caller
// should upgrade the stored password to a bcrypt hash on a successful login so
// existing deployments migrate transparently.
func VerifyPassword(stored, plain string) (ok bool, needsRehash bool) {
	if stored == "" {
		return false, false
	}
	if IsBcryptHash(stored) {
		err := bcrypt.CompareHashAndPassword([]byte(stored), []byte(plain))
		return err == nil, false
	}
	if stored == plain {
		return true, true
	}
	return false, false
}
