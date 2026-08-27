package auth

// CanonicalCredentialMetadataKey returns the canonical snake_case name for
// credential metadata keys that previously also accepted config-style aliases.
func CanonicalCredentialMetadataKey(key string) string {
	switch key {
	case "api-key":
		return "api_key"
	case "base-url":
		return "base_url"
	case "disable-cooling":
		return "disable_cooling"
	case "excluded-models":
		return "excluded_models"
	case "fingerprint-profile":
		return "fingerprint_profile"
	case "model-aliases":
		return "model_aliases"
	case "proxy-url":
		return "proxy_url"
	case "request-retry":
		return "request_retry"
	case "request-scoped-errors":
		return "request_scoped_errors"
	case "tool-prefix-disabled":
		return "tool_prefix_disabled"
	default:
		return key
	}
}

// NormalizeCredentialMetadata rewrites recognized legacy keys to their
// canonical snake_case names. An explicitly present canonical value wins.
func NormalizeCredentialMetadata(metadata map[string]any) {
	for key, value := range metadata {
		canonical := CanonicalCredentialMetadataKey(key)
		if canonical == key {
			continue
		}
		if _, exists := metadata[canonical]; !exists {
			metadata[canonical] = value
		}
		delete(metadata, key)
	}
}
