package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

// sealVersion is the prefix tag carried on every sealed payload so the sealer
// can distinguish its own ciphertext from plaintext (legacy rows) on read.
const sealVersion = "v1"

// Sealer applies symmetric authenticated encryption (AES-256-GCM) to short
// string fields at rest. It is used to protect the api_key_principal column
// in usage_events: the plaintext principal is the raw API key secret and
// must not be readable from the database directly.
//
// A nil Sealer means encryption is disabled: Seal and Open become identity
// pass-throughs so existing deployments without a configured passphrase keep
// working with plaintext columns.
type Sealer struct {
	gcm cipher.AEAD
}

// NewSealer derives a 32-byte AES key from the supplied passphrase via
// SHA-256 so any-length passphrase (including a raw 32-byte key) is accepted.
// A nil or empty passphrase returns a nil sealer (encryption disabled).
func NewSealer(passphrase []byte) (*Sealer, error) {
	if len(passphrase) == 0 {
		return nil, nil
	}
	sum := sha256.Sum256(passphrase)
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, fmt.Errorf("store: build aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("store: build gcm: %w", err)
	}
	return &Sealer{gcm: gcm}, nil
}

// Seal encrypts the supplied plaintext and returns a self-describing payload
// of the form "v1:<base64(nonce || ciphertext || tag)>".
//
//   - Empty input returns an empty string (no work performed, no row bloat).
//   - A nil Sealer returns the plaintext unchanged so configurations without
//     a passphrase stay in plaintext mode.
func (s *Sealer) Seal(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	if s == nil {
		return plaintext, nil
	}
	nonce := make([]byte, s.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("store: seal nonce: %w", err)
	}
	ciphertext := s.gcm.Seal(nil, nonce, []byte(plaintext), nil)
	payload := append(nonce, ciphertext...)
	return sealVersion + ":" + base64.StdEncoding.EncodeToString(payload), nil
}

// Open reverses Seal. It tolerates legacy plaintext payloads (those lacking
// the "v1:" prefix) by returning them unchanged: this keeps historical rows
// readable while new writes switch to ciphertext. A nil Sealer also returns
// the payload unchanged.
func (s *Sealer) Open(payload string) (string, error) {
	if payload == "" {
		return "", nil
	}
	if s == nil {
		return payload, nil
	}
	if !strings.HasPrefix(payload, sealVersion+":") {
		// Legacy plaintext row — pass through untouched. Forward-only
		// encryption relies on this so deployments adopting a passphrase
		// after the fact don't break reads of pre-existing rows.
		return payload, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(payload, sealVersion+":"))
	if err != nil {
		return "", fmt.Errorf("store: open payload base64: %w", err)
	}
	nonceSize := s.gcm.NonceSize()
	if len(raw) < nonceSize+s.gcm.Overhead() {
		return "", errors.New("store: open payload too short")
	}
	nonce, ciphertext := raw[:nonceSize], raw[nonceSize:]
	plaintext, err := s.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("store: open gcm: %w", err)
	}
	return string(plaintext), nil
}

// Enabled reports whether the sealer will actually encrypt. Returns false for
// a nil receiver so callers can decide whether to expose plaintext fields.
func (s *Sealer) Enabled() bool {
	return s != nil && s.gcm != nil
}
