package auth

import "strings"

const (
	// CredentialPolicyCodexAlphaSearchV1 selects credentials supported by Codex Alpha Search.
	CredentialPolicyCodexAlphaSearchV1 = "codex_alpha_search_v1"
	// CredentialPolicyCodexBackendV1 selects ChatGPT OAuth credentials for the
	// account-scoped chatgpt.com backend endpoints (usage, reset credits, tasks).
	CredentialPolicyCodexBackendV1 = "codex_backend_v1"
)

func normalizeCredentialPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case CredentialPolicyCodexAlphaSearchV1:
		return CredentialPolicyCodexAlphaSearchV1
	case CredentialPolicyCodexBackendV1:
		return CredentialPolicyCodexBackendV1
	default:
		return ""
	}
}

func credentialPolicyAllows(policy string, auth *Auth) bool {
	if auth == nil {
		return false
	}
	switch policy {
	case CredentialPolicyCodexAlphaSearchV1:
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			return false
		}
		switch auth.AuthKind() {
		case AuthKindOAuth:
			return true
		case AuthKindAPIKey:
			return strings.EqualFold(authAttribute(auth, AttributeCodexAlphaSearch), "true")
		default:
			return false
		}
	case CredentialPolicyCodexBackendV1:
		return strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") && auth.AuthKind() == AuthKindOAuth
	default:
		return false
	}
}
