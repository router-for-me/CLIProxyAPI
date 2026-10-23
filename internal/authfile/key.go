package authfile

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/zalando/go-keyring"
)

const (
	// EnvKey holds the base64 or hex encryption key.
	EnvKey = "CLIPROXY_AUTH_ENCRYPTION_KEY"
	// EnvKeyFile names a file that holds the key, such as a Docker secret.
	EnvKeyFile = "CLIPROXY_AUTH_ENCRYPTION_KEY_FILE"

	keyringService = "CLIProxyAPI"
	keyringUser    = "auth-encryption-key"
)

// keyringBackend is the OS keyring. Tests replace it.
var keyringBackend interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
} = osKeyring{}

type osKeyring struct{}

func (osKeyring) Get(service, user string) (string, error) { return keyring.Get(service, user) }

func (osKeyring) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}

// LoadKey returns the encryption key and where it came from. It checks
// EnvKey, then keyFile (or EnvKeyFile), then the OS keyring (Windows
// Credential Manager, macOS Keychain, or Linux Secret Service). When create is
// true and no key exists anywhere, it generates one and stores it in the OS
// keyring.
func LoadKey(keyFile string, create bool) ([]byte, string, error) {
	if raw := strings.TrimSpace(os.Getenv(EnvKey)); raw != "" {
		key, errParse := ParseKey(raw)
		if errParse != nil {
			return nil, "", fmt.Errorf("%s: %w", EnvKey, errParse)
		}
		return key, "env " + EnvKey, nil
	}
	if strings.TrimSpace(keyFile) == "" {
		keyFile = strings.TrimSpace(os.Getenv(EnvKeyFile))
	}
	if keyFile != "" {
		data, errRead := os.ReadFile(keyFile)
		if errRead != nil {
			return nil, "", fmt.Errorf("read key file: %w", errRead)
		}
		key, errParse := ParseKey(string(data))
		if errParse != nil {
			return nil, "", fmt.Errorf("key file %s: %w", keyFile, errParse)
		}
		return key, "file " + keyFile, nil
	}

	stored, errGet := keyringBackend.Get(keyringService, keyringUser)
	if errGet == nil {
		key, errParse := ParseKey(stored)
		if errParse != nil {
			return nil, "", fmt.Errorf("OS keyring entry %s/%s: %w", keyringService, keyringUser, errParse)
		}
		return key, "OS keyring", nil
	}
	if !errors.Is(errGet, keyring.ErrNotFound) {
		return nil, "", fmt.Errorf("OS keyring unavailable (%v); set %s or %s instead", errGet, EnvKey, EnvKeyFile)
	}
	if !create {
		return nil, "", fmt.Errorf("no key found; set %s, %s, or store one in the OS keyring", EnvKey, EnvKeyFile)
	}
	key, errGenerate := GenerateKey()
	if errGenerate != nil {
		return nil, "", errGenerate
	}
	if errSet := keyringBackend.Set(keyringService, keyringUser, base64.StdEncoding.EncodeToString(key)); errSet != nil {
		return nil, "", fmt.Errorf("store new key in OS keyring (%v); set %s or %s instead", errSet, EnvKey, EnvKeyFile)
	}
	return key, "OS keyring (new key)", nil
}

// GenerateKey returns a new random key.
func GenerateKey() ([]byte, error) {
	key := make([]byte, KeySize)
	if _, errRand := rand.Read(key); errRand != nil {
		return nil, fmt.Errorf("authfile: generate key: %w", errRand)
	}
	return key, nil
}

// ParseKey decodes a base64 (standard or URL, padded or not) or hex key.
func ParseKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if key, errDecode := encoding.DecodeString(raw); errDecode == nil && len(key) == KeySize {
			return key, nil
		}
	}
	if key, errDecode := hex.DecodeString(raw); errDecode == nil && len(key) == KeySize {
		return key, nil
	}
	return nil, fmt.Errorf("key must be %d bytes encoded as base64 or hex", KeySize)
}
