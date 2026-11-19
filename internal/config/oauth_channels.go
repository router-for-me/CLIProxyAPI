package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// ValidateOAuthChannelKeys rejects ambiguous channel spellings before sanitization.
// Field and key order stay stable so ambiguous documents report the same error.
func (cfg *Config) ValidateOAuthChannelKeys() error {
	if err := validateOAuthChannelMap("oauth-model-alias", cfg.OAuthModelAlias); err != nil {
		return err
	}
	if err := validateOAuthChannelMap("oauth-excluded-models", cfg.OAuthExcludedModels); err != nil {
		return err
	}
	if err := validateOAuthChannelMap("oauth-settings", cfg.OAuthSettings); err != nil {
		return err
	}
	return validateOAuthChannelMap("oauth-request-scoped-errors", cfg.OAuthRequestScopedErrors)
}

func validateOAuthChannelMap[T any](field string, entries map[string][]T) error {
	seen := make(map[string]string, len(entries))
	for _, raw := range slices.Sorted(maps.Keys(entries)) {
		channel := strings.ToLower(strings.TrimSpace(raw))
		if channel == "" {
			continue // Empty channels are discarded by the existing sanitizers.
		}
		if previous, exists := seen[channel]; exists {
			return fmt.Errorf("%s: channel keys %q and %q normalize to %q", field, previous, raw, channel)
		}
		seen[channel] = raw
	}
	return nil
}
