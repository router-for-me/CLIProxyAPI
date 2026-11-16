package auth

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
)

// SIWC inference uses its nested OAuth partition without the outer account
// selector. A canonical usage probe inspects the outer token and account. Only
// the account claim stored with the SIWC OAuth result can join those identities;
// the auth-wide cooldown does not retain which partition produced the failure.
func quotaRecoverySIWCAccountMatches(auth *Auth) bool {
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") || !IsCodexUseSIWC(auth) {
		return true
	}
	account, _ := auth.Metadata["account_id"].(string)
	account = strings.TrimSpace(account)
	if account == "" {
		return false
	}
	claims, err := codex.ParseJWTToken(authSIWCTokenField(auth, "id_token"))
	if err != nil || strings.TrimSpace(claims.GetAccountID()) != account {
		return false
	}
	token := authSIWCTokenField(auth, "access_token")
	if token == "" {
		token = authSIWCTokenField(auth, "access")
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
			if value != account {
				return false
			}
		}
	}
	return true
}
