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
	partition, _ := auth.Metadata["siwc"].(map[string]any)
	idToken, _ := partition["id_token"].(string)
	claims, err := codex.ParseJWTToken(strings.TrimSpace(idToken))
	return err == nil && strings.TrimSpace(claims.GetAccountID()) == account
}
