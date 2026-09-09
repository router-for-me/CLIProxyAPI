package config

import (
	"fmt"
	"strings"
)

// ValidateAPIKeyPrefixes rejects malformed restrictions instead of silently granting access.
func (cfg *Config) ValidateAPIKeyPrefixes() error {
	for key, prefixes := range cfg.APIKeyPrefixes {
		if key == "" || strings.TrimSpace(key) != key {
			return fmt.Errorf("api-key-prefixes contains an empty or untrimmed API key")
		}
		for _, prefix := range prefixes {
			if prefix == "" || normalizeModelPrefix(prefix) != prefix {
				return fmt.Errorf("api-key-prefixes requires nonempty normalized prefixes without slashes")
			}
		}
	}
	return nil
}
