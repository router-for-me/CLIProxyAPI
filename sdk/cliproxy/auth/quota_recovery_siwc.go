package auth

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
)

// A canonical usage probe inspects the outer token and account. Inference can
// use the nested SIWC partition or configured identity headers instead. The
// auth-wide cooldown does not retain which identity produced the failure.
func quotaRecoverySIWCAccountMatches(auth *Auth) bool {
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if provider != "codex" && provider != "claude" {
		return true
	}
	account, _ := auth.Metadata["account_id"].(string)
	account = strings.TrimSpace(account)
	token, _ := auth.Metadata["access_token"].(string)
	token = strings.TrimSpace(token)
	if provider == "codex" && IsCodexUseSIWC(auth) {
		if account == "" {
			return false
		}
		claims, err := codex.ParseJWTToken(authSIWCTokenField(auth, "id_token"))
		if err != nil || strings.TrimSpace(claims.GetAccountID()) != account {
			return false
		}
		token = authSIWCTokenField(auth, "access_token")
		if token == "" {
			token = authSIWCTokenField(auth, "access")
		}
	}
	// Configured headers are applied after credential selection. Unknown or
	// different identity overrides cannot be established by the outer probe.
	for key, raw := range auth.Attributes {
		if !strings.HasPrefix(key, "header:") {
			continue
		}
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(strings.TrimPrefix(key, "header:"))) {
		case "authorization":
			if token == "" || value != "Bearer "+token {
				return false
			}
		case "chatgpt-account-id":
			if provider == "codex" && value != account {
				return false
			}
		case "x-api-key":
			if provider == "claude" {
				return false
			}
		}
	}
	return true
}
