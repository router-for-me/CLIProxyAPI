package common

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

const passwordEncryptionKeyBits = 2048

var ErrPasswordEncryptionInvalid = errors.New("password encryption payload is invalid")

var passwordEncryptionState struct {
	sync.RWMutex
	privateKey *rsa.PrivateKey
	publicKey  string
	keyID      string
}

// GeneratePasswordEncryptionPrivateKey creates the server key used to decrypt
// browser login passwords. The caller is responsible for persisting the PEM.
func GeneratePasswordEncryptionPrivateKey() (string, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, passwordEncryptionKeyBits)
	if err != nil {
		return "", fmt.Errorf("generate password encryption key: %w", err)
	}
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return "", fmt.Errorf("marshal password encryption key: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: privateKeyDER,
	})), nil
}

// LoadPasswordEncryptionPrivateKey validates a persisted key before replacing
// the active in-memory key used by request handlers.
func LoadPasswordEncryptionPrivateKey(privateKeyPEM string) error {
	block, rest := pem.Decode([]byte(privateKeyPEM))
	if block == nil || block.Type != "PRIVATE KEY" || strings.TrimSpace(string(rest)) != "" {
		return errors.New("password encryption key is not valid PKCS#8 PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("parse password encryption key: %w", err)
	}
	privateKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return errors.New("password encryption key is not RSA")
	}
	if privateKey.N == nil || privateKey.N.BitLen() < passwordEncryptionKeyBits {
		return fmt.Errorf("password encryption key must be at least %d bits", passwordEncryptionKeyBits)
	}
	if err := privateKey.Validate(); err != nil {
		return fmt.Errorf("validate password encryption key: %w", err)
	}
	privateKey.Precompute()

	publicKeyDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return fmt.Errorf("marshal password encryption public key: %w", err)
	}
	publicKeyPEM := string(pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: publicKeyDER,
	}))
	keyDigest := sha256.Sum256(publicKeyDER)
	keyID := hex.EncodeToString(keyDigest[:16])

	passwordEncryptionState.Lock()
	defer passwordEncryptionState.Unlock()
	passwordEncryptionState.privateKey = privateKey
	passwordEncryptionState.publicKey = publicKeyPEM
	passwordEncryptionState.keyID = keyID
	return nil
}

// PasswordEncryptionPublicKey returns the active key identifier and SPKI PEM
// public key exposed to browser clients.
func PasswordEncryptionPublicKey() (keyID string, publicKeyPEM string) {
	passwordEncryptionState.RLock()
	defer passwordEncryptionState.RUnlock()
	return passwordEncryptionState.keyID, passwordEncryptionState.publicKey
}

// DecryptPassword accepts legacy RSA-OAEP/SHA-256 ciphertext and v2 envelopes.
// V2 wraps a fresh AES-256 key with RSA-OAEP and encrypts the password with GCM,
// allowing long Unicode passwords to work with existing 2048-bit server keys.
// Both formats share one public error for all malformed inputs.
func DecryptPassword(ciphertextBase64 string, keyID string) (string, error) {
	passwordEncryptionState.RLock()
	privateKey := passwordEncryptionState.privateKey
	activeKeyID := passwordEncryptionState.keyID
	passwordEncryptionState.RUnlock()
	if privateKey == nil || keyID == "" || keyID != activeKeyID {
		return "", ErrPasswordEncryptionInvalid
	}
	if strings.HasPrefix(ciphertextBase64, "v2.") {
		if len(ciphertextBase64) > 4096 {
			return "", ErrPasswordEncryptionInvalid
		}
		parts := strings.Split(ciphertextBase64, ".")
		if len(parts) != 4 {
			return "", ErrPasswordEncryptionInvalid
		}
		wrappedKey, err := base64.StdEncoding.Strict().DecodeString(parts[1])
		if err != nil || len(wrappedKey) != privateKey.Size() {
			return "", ErrPasswordEncryptionInvalid
		}
		nonce, err := base64.StdEncoding.Strict().DecodeString(parts[2])
		if err != nil || len(nonce) != 12 {
			return "", ErrPasswordEncryptionInvalid
		}
		ciphertext, err := base64.StdEncoding.Strict().DecodeString(parts[3])
		if err != nil || len(ciphertext) <= 16 || len(ciphertext) > MaxAccountPasswordLength*utf8.UTFMax+16 {
			return "", ErrPasswordEncryptionInvalid
		}
		key, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, privateKey, wrappedKey, []byte("password-v2"))
		if err != nil || len(key) != 32 {
			return "", ErrPasswordEncryptionInvalid
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return "", ErrPasswordEncryptionInvalid
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return "", ErrPasswordEncryptionInvalid
		}
		plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte("password-v2:"+keyID))
		if err != nil {
			return "", ErrPasswordEncryptionInvalid
		}
		return string(plaintext), nil
	}
	ciphertext, err := base64.StdEncoding.DecodeString(ciphertextBase64)
	if err != nil || len(ciphertext) != privateKey.Size() {
		return "", ErrPasswordEncryptionInvalid
	}
	plaintext, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, privateKey, ciphertext, nil)
	if err != nil || len(plaintext) == 0 {
		return "", ErrPasswordEncryptionInvalid
	}
	return string(plaintext), nil
}
