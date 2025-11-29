package copilot

import (
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// ApplyTokenRefresh updates auth metadata and storage with new Copilot token data.
// This is the single source of truth for post-refresh mutations.
func ApplyTokenRefresh(auth *coreauth.Auth, tokenResp *CopilotTokenResponse, now time.Time) {
	if auth == nil || tokenResp == nil {
		return
	}

	expiryStr := time.Unix(tokenResp.ExpiresAt, 0).Format(time.RFC3339)
	lastRefreshStr := now.Format(time.RFC3339)

	// Update metadata (runtime cache)
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["copilot_token"] = tokenResp.Token
	auth.Metadata["copilot_token_expiry"] = expiryStr
	auth.Metadata["type"] = "copilot"

	// Update storage (persistence layer)
	if storage, ok := auth.Storage.(*CopilotTokenStorage); ok && storage != nil {
		storage.CopilotToken = tokenResp.Token
		storage.CopilotTokenExpiry = expiryStr
		storage.RefreshIn = tokenResp.RefreshIn
		storage.LastRefresh = lastRefreshStr
	}

	auth.LastRefreshedAt = now
}

// ResolveAccountType extracts the account type using canonical precedence:
// 1. Attributes (Runtime) 2. Storage (Fallback) 3. Default
func ResolveAccountType(auth *coreauth.Auth) AccountType {
	if auth == nil {
		return AccountTypeIndividual
	}

	if auth.Attributes != nil {
		if at, ok := auth.Attributes["account_type"]; ok && at != "" {
			parsed, _ := ParseAccountType(at)
			return parsed
		}
	}
	if storage, ok := auth.Storage.(*CopilotTokenStorage); ok && storage != nil && storage.AccountType != "" {
		parsed, _ := ParseAccountType(storage.AccountType)
		return parsed
	}
	return AccountTypeIndividual
}

// ResolveGitHubToken extracts the GitHub OAuth token (Metadata > Storage).
func ResolveGitHubToken(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["github_token"].(string); ok && v != "" {
			return v
		}
	}
	if storage, ok := auth.Storage.(*CopilotTokenStorage); ok && storage != nil {
		return storage.GitHubToken
	}
	return ""
}

// ResolveCopilotToken extracts the cached Copilot token and expiry from metadata.
func ResolveCopilotToken(auth *coreauth.Auth) (token string, expiry time.Time, ok bool) {
	if auth == nil || auth.Metadata == nil {
		return "", time.Time{}, false
	}

	token, _ = auth.Metadata["copilot_token"].(string)
	expiryStr, _ := auth.Metadata["copilot_token_expiry"].(string)

	if token == "" || expiryStr == "" {
		return "", time.Time{}, false
	}

	expiry, err := time.Parse(time.RFC3339, expiryStr)
	if err != nil {
		return "", time.Time{}, false
	}
	return token, expiry, true
}

// EnsureMetadataHydrated hydrates metadata from storage if needed.
func EnsureMetadataHydrated(auth *coreauth.Auth) {
	if auth == nil {
		return
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}

	if _, ok := auth.Metadata["github_token"].(string); !ok {
		if storage, ok := auth.Storage.(*CopilotTokenStorage); ok && storage != nil && storage.GitHubToken != "" {
			auth.Metadata["github_token"] = storage.GitHubToken
		}
	}
}
