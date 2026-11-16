package config

import (
	"fmt"
	"sort"
	"strings"
)

// ValidateAPIKeyModelAccess rejects api-key-model-access entries whose keys
// collide after trimming. Client API keys are trimmed when they are configured
// and when a request is authenticated, so two entries that only differ in
// surrounding whitespace select the same policy; keeping both would make the
// applied policy depend on map iteration order.
func ValidateAPIKeyModelAccess(access map[string]APIKeyModelAccess) error {
	if len(access) < 2 {
		return nil
	}
	keys := make([]string, 0, len(access))
	for key := range access {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	normalized := make(map[string]string, len(keys))
	for _, key := range keys {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		if previous, exists := normalized[trimmedKey]; exists {
			return fmt.Errorf("api-key-model-access: entries %q and %q both normalize to API key %q", previous, key, trimmedKey)
		}
		normalized[trimmedKey] = key
	}
	return nil
}
