package auth

import "strings"

// BaseURLFromMetadata returns the per-auth upstream base URL stored in auth metadata.
// Both base_url and base-url are accepted to match config naming conventions.
func BaseURLFromMetadata(metadata map[string]any) string {
	for _, key := range []string{"base_url", "base-url"} {
		if raw, ok := metadata[key].(string); ok {
			if trimmed := strings.TrimSpace(raw); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// ApplyBaseURLFromMetadata copies the per-auth upstream base URL from metadata
// into auth attributes so executors can resolve it consistently across stores.
func ApplyBaseURLFromMetadata(auth *Auth) {
	if auth == nil {
		return
	}
	if !shouldApplyBaseURLFromMetadata(auth) {
		return
	}
	baseURL := BaseURLFromMetadata(auth.Metadata)
	if baseURL == "" {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes["base_url"] = baseURL
}

func shouldApplyBaseURLFromMetadata(auth *Auth) bool {
	if auth == nil || auth.AuthKind() != AuthKindOAuth {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "claude", "codex":
		return true
	default:
		return false
	}
}
