package config

import (
	"errors"
	"sort"
	"strings"
)

// ValidateAPIKeyModelAccess rejects api-key-model-access entries whose keys
// collide after trimming. Client API keys are trimmed when they are configured
// and when a request is authenticated, so two entries that only differ in
// surrounding whitespace select the same policy; keeping both would make the
// applied policy depend on map iteration order. Keys are secrets, so the
// diagnostic never repeats them.
func ValidateAPIKeyModelAccess(access map[string]APIKeyModelAccess) error {
	if len(access) < 2 {
		return nil
	}
	keys := make([]string, 0, len(access))
	for key := range access {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	normalized := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		if _, exists := normalized[trimmedKey]; exists {
			return errors.New("api-key-model-access: two entries normalize to the same API key after trimming; remove the duplicate or whitespace-padded entry")
		}
		normalized[trimmedKey] = struct{}{}
	}
	return nil
}
