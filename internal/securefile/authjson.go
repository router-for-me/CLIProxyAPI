package securefile

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	authEnvelopeVersion = 1
	authEnvelopeAlgAES  = "aes-256-gcm"
)

type authEnvelope struct {
	V     int    `json:"v"`
	Alg   string `json:"alg"`
	Nonce string `json:"nonce"`
	Ct    string `json:"ct"`
}

// deriveKey returns a 32-byte key derived from the provided secret. If the secret is base64
// for exactly 32 bytes, it is used directly; otherwise SHA-256(secret) is used.
func deriveKey(secret string) ([]byte, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil, fmt.Errorf("securefile: encryption secret is empty")
	}
	if decoded, err := base64.StdEncoding.DecodeString(secret); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	sum := sha256.Sum256([]byte(secret))
	return sum[:], nil
}

func encryptBytes(plaintext []byte, secret string) ([]byte, error) {
	key, err := deriveKey(secret)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, plaintext, nil)
	env := authEnvelope{
		V:     authEnvelopeVersion,
		Alg:   authEnvelopeAlgAES,
		Nonce: base64.StdEncoding.EncodeToString(nonce),
		Ct:    base64.StdEncoding.EncodeToString(ct),
	}
	return json.Marshal(env)
}

func decryptBytes(envelopeBytes []byte, secret string) ([]byte, error) {
	var env authEnvelope
	if err := json.Unmarshal(envelopeBytes, &env); err != nil {
		return nil, fmt.Errorf("securefile: invalid encrypted envelope: %w", err)
	}
	if env.V != authEnvelopeVersion || strings.TrimSpace(env.Alg) != authEnvelopeAlgAES {
		return nil, fmt.Errorf("securefile: unsupported envelope (v=%d alg=%s)", env.V, env.Alg)
	}
	key, err := deriveKey(secret)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce, err := base64.StdEncoding.DecodeString(env.Nonce)
	if err != nil {
		return nil, fmt.Errorf("securefile: invalid nonce encoding: %w", err)
	}
	ct, err := base64.StdEncoding.DecodeString(env.Ct)
	if err != nil {
		return nil, fmt.Errorf("securefile: invalid ciphertext encoding: %w", err)
	}
	plaintext, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("securefile: decrypt failed: %w", err)
	}
	return plaintext, nil
}

func looksEncryptedEnvelope(raw []byte) bool {
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	_, hasV := probe["v"]
	_, hasAlg := probe["alg"]
	_, hasCt := probe["ct"]
	return hasV && hasAlg && hasCt
}

// DecodeAuthJSON returns decrypted JSON bytes if raw is an encrypted envelope; otherwise returns raw.
// It returns (plaintext, wasEncrypted, error).
func DecodeAuthJSON(raw []byte, settings AuthEncryptionSettings) ([]byte, bool, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return raw, false, nil
	}
	if !looksEncryptedEnvelope(raw) {
		return raw, false, nil
	}
	secret := ResolveAuthEncryptionSecret(settings.Secret)
	if strings.TrimSpace(secret) == "" {
		return nil, true, fmt.Errorf("securefile: auth file is encrypted but no encryption key is configured")
	}
	plaintext, err := decryptBytes(raw, secret)
	if err != nil {
		return nil, true, err
	}
	return plaintext, true, nil
}

func writeAuthJSONFileUnlocked(path string, jsonBytes []byte, settings AuthEncryptionSettings) error {
	payload := jsonBytes
	if settings.Enabled {
		secret := ResolveAuthEncryptionSecret(settings.Secret)
		if strings.TrimSpace(secret) == "" {
			return fmt.Errorf("securefile: auth encryption enabled but no encryption key configured")
		}
		enc, err := encryptBytes(jsonBytes, secret)
		if err != nil {
			return err
		}
		payload = enc
	}
	if err := EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	return AtomicWriteFile(path, payload, 0o600)
}

// ReadAuthJSONFile reads path, locking path+".lock", and returns decrypted JSON when needed.
func ReadAuthJSONFile(path string) ([]byte, bool, error) {
	settings := CurrentAuthEncryption()
	lockPath := path + ".lock"
	var (
		out       []byte
		encrypted bool
		readErr   error
	)
	err := WithLock(lockPath, 10*time.Second, func() error {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		plaintext, wasEncrypted, err := DecodeAuthJSON(raw, settings)
		if err != nil {
			return err
		}
		out = plaintext
		encrypted = wasEncrypted
		// Best-effort migration: if encryption is enabled and we read plaintext, re-save encrypted.
		if settings.Enabled && !wasEncrypted && settings.AllowPlaintextFallback {
			if err := writeAuthJSONFileUnlocked(path, plaintext, settings); err != nil {
				// ignore; caller still gets plaintext content
			}
		}
		return nil
	})
	if err != nil {
		readErr = err
	}
	return out, encrypted, readErr
}

// WriteAuthJSONFile writes jsonBytes to path with 0600 perms, using lock + atomic write.
// If auth encryption is enabled, it stores an encrypted envelope.
func WriteAuthJSONFile(path string, jsonBytes []byte) error {
	if path == "" {
		return fmt.Errorf("securefile: path is empty")
	}
	settings := CurrentAuthEncryption()
	lockPath := path + ".lock"
	return WithLock(lockPath, 10*time.Second, func() error {
		return writeAuthJSONFileUnlocked(path, jsonBytes, settings)
	})
}
