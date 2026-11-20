// Package session seals the user's API token into a cookie with AES-256-GCM.
// The token never reaches browser JavaScript, and sessions survive UI restarts
// because the key comes from a file (deploy/secrets/ui_session_key).
package session

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

type Data struct {
	Token     string    `json:"t"`
	Principal string    `json:"p"`
	Expires   time.Time `json:"e"`
}

type Sealer struct {
	aead cipher.AEAD
}

// NewSealer derives a 256-bit key from arbitrary secret material.
func NewSealer(secret []byte) (*Sealer, error) {
	if len(secret) < 16 {
		return nil, errors.New("session key must be at least 16 bytes")
	}
	key := sha256.Sum256(secret)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

func (s *Sealer) Seal(d Data) (string, error) {
	plain, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(s.aead.Seal(nonce, nonce, plain, nil)), nil
}

var ErrInvalid = errors.New("invalid or expired session")

func (s *Sealer) Open(value string) (Data, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) < s.aead.NonceSize() {
		return Data{}, ErrInvalid
	}
	n := s.aead.NonceSize()
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return Data{}, ErrInvalid
	}
	var d Data
	if json.Unmarshal(plain, &d) != nil || d.Token == "" || time.Now().After(d.Expires) {
		return Data{}, ErrInvalid
	}
	return d, nil
}
