// Package auth issues and verifies API tokens.
//
// Token format: kn_<prefix>_<secret>. The prefix (8 base62 chars) is public and used for lookup;
// the secret (32 random bytes, base62) is stored only as sha256. A 256-bit secret makes a slow
// password hash unnecessary and keeps per-request verification cheap.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"math/big"
	"strings"
)

const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

type Token struct {
	Prefix string
	Secret string
}

func (t Token) String() string { return "kn_" + t.Prefix + "_" + t.Secret }

func (t Token) Hash() []byte { return HashSecret(t.Secret) }

func HashSecret(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}

func NewToken() (Token, error) {
	p := make([]byte, 6)
	s := make([]byte, 32)
	if _, err := rand.Read(p); err != nil {
		return Token{}, err
	}
	if _, err := rand.Read(s); err != nil {
		return Token{}, err
	}
	return Token{Prefix: pad(base62(p), 8), Secret: base62(s)}, nil
}

var ErrMalformed = errors.New("malformed token")

func Parse(raw string) (Token, error) {
	rest, ok := strings.CutPrefix(raw, "kn_")
	if !ok {
		return Token{}, ErrMalformed
	}
	prefix, secret, ok := strings.Cut(rest, "_")
	if !ok || len(prefix) != 8 || len(secret) < 32 || !isBase62(prefix) || !isBase62(secret) {
		return Token{}, ErrMalformed
	}
	return Token{Prefix: prefix, Secret: secret}, nil
}

// Verify compares the secret against a stored hash in constant time.
func Verify(secret string, hash []byte) bool {
	return subtle.ConstantTimeCompare(HashSecret(secret), hash) == 1
}

func base62(b []byte) string {
	n := new(big.Int).SetBytes(b)
	if n.Sign() == 0 {
		return "0"
	}
	base := big.NewInt(62)
	mod := new(big.Int)
	var out []byte
	for n.Sign() > 0 {
		n.DivMod(n, base, mod)
		out = append(out, alphabet[mod.Int64()])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

func pad(s string, n int) string {
	if len(s) >= n {
		return s[len(s)-n:]
	}
	return strings.Repeat("0", n-len(s)) + s
}

func isBase62(s string) bool {
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(alphabet, rune(s[i])) {
			return false
		}
	}
	return true
}
