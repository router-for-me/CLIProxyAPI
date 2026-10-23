package authfile

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// KeySize is the encryption key length in bytes (AES-256).
const KeySize = 32

// sealedPrefix starts every sealed field value:
// "cpa-enc:v1:<key id>:<base64 of nonce and ciphertext>".
const sealedPrefix = "cpa-enc:v1:"

type aesGCMCodec struct {
	aead  cipher.AEAD
	keyID string
}

// NewAESGCMCodec returns a Codec that seals with AES-256-GCM and a random
// 96-bit nonce per value.
func NewAESGCMCodec(key []byte) (Codec, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("authfile: key must be %d bytes, got %d", KeySize, len(key))
	}
	block, errCipher := aes.NewCipher(key)
	if errCipher != nil {
		return nil, fmt.Errorf("authfile: %w", errCipher)
	}
	aead, errGCM := cipher.NewGCM(block)
	if errGCM != nil {
		return nil, fmt.Errorf("authfile: %w", errGCM)
	}
	return &aesGCMCodec{aead: aead, keyID: KeyID(key)}, nil
}

// KeyID returns a short fingerprint of key. It tells keys apart in sealed
// values and logs without revealing the key.
func KeyID(key []byte) string {
	sum := sha256.Sum256(append([]byte("cliproxyapi-auth-key-id:"), key...))
	return hex.EncodeToString(sum[:8])
}

// Seal binds the value to its field name through the AEAD additional data,
// so a sealed value moved to another field fails to open. It is not bound to
// the file: a value copied into another auth file under the same field name
// still opens. That needs write access to the auth directory, which already
// allows swapping whole files, so the file name is left out on purpose and
// renaming an auth file keeps working.
func (c *aesGCMCodec) Seal(plain []byte, field string) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, errRand := rand.Read(nonce); errRand != nil {
		return "", fmt.Errorf("authfile: read nonce: %w", errRand)
	}
	out := c.aead.Seal(nonce, nonce, plain, []byte(sealedPrefix+field))
	return sealedPrefix + c.keyID + ":" + base64.StdEncoding.EncodeToString(out), nil
}

func (c *aesGCMCodec) Open(sealed string, field string) ([]byte, error) {
	rest, ok := strings.CutPrefix(sealed, sealedPrefix)
	if !ok {
		return nil, errors.New("authfile: value is not sealed")
	}
	keyID, payload, ok := strings.Cut(rest, ":")
	if !ok {
		return nil, errors.New("authfile: malformed sealed value")
	}
	if keyID != c.keyID {
		return nil, fmt.Errorf("authfile: value was encrypted with key %s, configured key is %s", keyID, c.keyID)
	}
	raw, errDecode := base64.StdEncoding.DecodeString(payload)
	if errDecode != nil || len(raw) < c.aead.NonceSize() {
		return nil, errors.New("authfile: malformed sealed value")
	}
	nonce, ciphertext := raw[:c.aead.NonceSize()], raw[c.aead.NonceSize():]
	plain, errOpen := c.aead.Open(nil, nonce, ciphertext, []byte(sealedPrefix+field))
	if errOpen != nil {
		return nil, errors.New("authfile: decrypt failed: wrong key or corrupted value")
	}
	return plain, nil
}
