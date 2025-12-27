package securefile

import (
	"os"
	"strings"
	"sync/atomic"
)

// AuthEncryptionSettings controls encryption-at-rest for auth JSON files.
type AuthEncryptionSettings struct {
	Enabled                bool
	Secret                 string
	AllowPlaintextFallback bool
}

var authEncryptionSettings atomic.Value // stores AuthEncryptionSettings

func init() {
	authEncryptionSettings.Store(AuthEncryptionSettings{})
}

// ConfigureAuthEncryption updates global auth encryption behavior.
func ConfigureAuthEncryption(settings AuthEncryptionSettings) {
	settings.Secret = strings.TrimSpace(settings.Secret)
	authEncryptionSettings.Store(settings)
}

// CurrentAuthEncryption returns the active auth encryption settings.
func CurrentAuthEncryption() AuthEncryptionSettings {
	if v := authEncryptionSettings.Load(); v != nil {
		if s, ok := v.(AuthEncryptionSettings); ok {
			return s
		}
	}
	return AuthEncryptionSettings{}
}

// ResolveAuthEncryptionSecret resolves a secret from config/env.
// Explicit secret wins; otherwise checks env CLIPROXY_AUTH_ENCRYPTION_KEY then CLI_PROXY_API_AUTH_ENCRYPTION_KEY.
func ResolveAuthEncryptionSecret(explicit string) string {
	if trimmed := strings.TrimSpace(explicit); trimmed != "" {
		return trimmed
	}
	for _, key := range []string{"CLIPROXY_AUTH_ENCRYPTION_KEY", "CLI_PROXY_API_AUTH_ENCRYPTION_KEY"} {
		if v, ok := os.LookupEnv(key); ok {
			if trimmed := strings.TrimSpace(v); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}
