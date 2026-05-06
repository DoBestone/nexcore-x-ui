// Package random provides random string/integer generators backed by
// crypto/rand. It is the single source of randomness for every secret in
// the panel — session secrets, API tokens, magic tokens, install-time
// credentials, the random panel port — so callers never have to think
// about which RNG to pick.
//
// All functions panic if crypto/rand fails, because a system that can't
// produce secure randomness has no business minting credentials.
package random

import (
	"crypto/rand"
	"encoding/binary"
	"math/big"
)

const (
	digits     = "0123456789"
	lowerCase  = "abcdefghijklmnopqrstuvwxyz"
	upperCase  = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	allCharset = digits + lowerCase + upperCase
)

// Seq returns an n-character string drawn uniformly from [0-9A-Za-z] using
// crypto/rand. Use this for every panel-issued secret (cookie key, API
// token, magic-link token, randomized initial credentials).
func Seq(n int) string {
	return seqFrom(n, allCharset)
}

// SeqDigits returns an n-character numeric string. Used for the random
// panel port lookup loop.
func SeqDigits(n int) string {
	return seqFrom(n, digits)
}

func seqFrom(n int, charset string) string {
	if n <= 0 {
		return ""
	}
	out := make([]byte, n)
	max := big.NewInt(int64(len(charset)))
	for i := 0; i < n; i++ {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic("random: crypto/rand failed: " + err.Error())
		}
		out[i] = charset[idx.Int64()]
	}
	return string(out)
}

// IntN returns a uniformly distributed integer in [0, n). Panics on n<=0
// or rand failure.
func IntN(n int) int {
	if n <= 0 {
		panic("random: IntN with non-positive bound")
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		panic("random: crypto/rand failed: " + err.Error())
	}
	return int(v.Int64())
}

// Bytes returns n cryptographically random bytes.
func Bytes(n int) []byte {
	if n <= 0 {
		return nil
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("random: crypto/rand failed: " + err.Error())
	}
	return b
}

// Uint64 returns a cryptographically random uint64.
func Uint64() uint64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("random: crypto/rand failed: " + err.Error())
	}
	return binary.LittleEndian.Uint64(b[:])
}
