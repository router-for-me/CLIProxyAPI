package helps

import (
	"strings"

	kimiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kimi"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// ResolveKimiBaseURL resolves the upstream API base URL for Kimi API requests based on auth.
func ResolveKimiBaseURL(auth *cliproxyauth.Auth) string {
	if auth != nil {
		if auth.Attributes != nil {
			if raw := strings.TrimRight(strings.TrimSpace(auth.Attributes["base_url"]), "/"); raw != "" {
				return raw
			}
		}
		if auth.Metadata != nil {
			if raw, ok := auth.Metadata["base_url"].(string); ok && strings.TrimSpace(raw) != "" {
				return strings.TrimRight(strings.TrimSpace(raw), "/")
			}
		}
		if kimiauth.IsKimiAIAuth(auth) {
			return kimiauth.KimiAIAPIBaseURL
		}
	}
	return kimiauth.KimiAPIBaseURL
}

// ResolveKimiResponsesURL resolves the upstream URL for Kimi Responses API requests.
func ResolveKimiResponsesURL(auth *cliproxyauth.Auth) string {
	baseURL := ResolveKimiBaseURL(auth)
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL + "/responses"
	}
	return baseURL + "/v1/responses"
}

// ResolveKimiChatURL resolves the upstream URL for Kimi Chat Completions requests.
func ResolveKimiChatURL(auth *cliproxyauth.Auth) string {
	baseURL := ResolveKimiBaseURL(auth)
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL + "/chat/completions"
	}
	return baseURL + "/v1/chat/completions"
}

// ResolveKimiClaudeBaseURL resolves the base URL for Kimi Claude Messages delegation.
func ResolveKimiClaudeBaseURL(auth *cliproxyauth.Auth) string {
	baseURL := ResolveKimiBaseURL(auth)
	return strings.TrimSuffix(baseURL, "/v1")
}
