package handlers

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/websearch"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// borrowedCredential lists a provider's login sessions, keyed by the
// provider name stored in the auth record.
type borrowedCredential struct {
	auths []*coreauth.Auth
}

// searchCredentialProviders maps a websearch provider to the auth-store
// provider whose OAuth session it can borrow. Borrowing is a last resort:
// an explicitly configured search key always wins, and these only fill in
// for deployments that already logged in to the model provider and would
// otherwise get no grounded search at all.
var searchCredentialProviders = map[string]string{
	websearch.ProviderAnthropic: "claude",
	websearch.ProviderGemini:    "gemini",
	websearch.ProviderCodex:     "codex",
	websearch.ProviderXAI:       "xai",
	websearch.ProviderZAI:       "zai",
	websearch.ProviderKimi:      "kimi",
}

// borrowedSearchCredentials returns the usable login sessions for the given
// search providers. A session marked disabled or unavailable is skipped: it
// is a credential the operator has already taken out of rotation, and using
// it for search would burn quota the model traffic no longer needs.
func borrowedSearchCredentials(manager *coreauth.Manager, providers []string) borrowedCredential {
	out := borrowedCredential{}
	if manager == nil || len(providers) == 0 {
		return out
	}
	auths := manager.List()
	if len(auths) == 0 {
		return out
	}
	for _, searchProvider := range providers {
		authProvider, ok := searchCredentialProviders[strings.TrimSpace(searchProvider)]
		if !ok {
			continue
		}
		for _, auth := range auths {
			if auth == nil || auth.Disabled || auth.Unavailable {
				continue
			}
			if strings.TrimSpace(auth.Provider) != authProvider {
				continue
			}
			out.auths = append(out.auths, auth)
			// One session per provider is enough: search requests are
			// independent of each other and cycling the rest would only
			// spread rate-limit rejections.
			break
		}
	}
	return out
}

// applyBorrowedSearchCredentials fills in provider credentials that the
// search configuration left empty, using sessions already present in the
// auth store. It mutates cfg in place and reports whether anything changed.
func applyBorrowedSearchCredentials(cfg *websearch.Config, manager *coreauth.Manager, providers []string) bool {
	borrowed := borrowedSearchCredentials(manager, providers)
	if len(borrowed.auths) == 0 {
		return false
	}
	changed := false
	for _, auth := range borrowed.auths {
		access := authSearchAccessToken(auth)
		if access == "" {
			continue
		}
		switch strings.TrimSpace(auth.Provider) {
		case "claude":
			if strings.TrimSpace(cfg.AnthropicAPIKey) == "" {
				cfg.AnthropicAPIKey = access
				changed = true
			}
		case "gemini":
			if strings.TrimSpace(cfg.GeminiAPIKey) == "" {
				cfg.GeminiAPIKey = access
				changed = true
			}
		case "codex":
			if strings.TrimSpace(cfg.CodexAPIKey) == "" {
				cfg.CodexAPIKey = access
				changed = true
			}
		case "xai":
			if strings.TrimSpace(cfg.XAIAPIKey) == "" {
				cfg.XAIAPIKey = access
				changed = true
			}
		case "zai":
			if strings.TrimSpace(cfg.ZAIAPIKey) == "" {
				cfg.ZAIAPIKey = access
				changed = true
			}
		case "kimi":
			if strings.TrimSpace(cfg.KimiAPIKey) == "" {
				cfg.KimiAPIKey = access
				changed = true
			}
		}
	}
	return changed
}

// authSearchAccessToken reads the bearer credential out of a login session.
//
// The auth file is the authority here, not the in-memory Metadata: Claude
// and Codex register their token straight into TokenStorage and leave
// Metadata holding only identity fields, so a session created by a login in
// this process would otherwise look credential-less. Reading the backing
// file covers both cases, and the metadata is only a fallback for records
// with no readable file (a test double, or a store that has not flushed).
func authSearchAccessToken(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if token := credentialFileAccessToken(auth.FileName); token != "" {
		return token
	}
	for _, key := range []string{"access_token", "accessToken", "api_key", "apiKey", "token"} {
		if value, okLookup := auth.Metadata[key]; okLookup {
			if text, okString := value.(string); okString {
				if trimmed := strings.TrimSpace(text); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

// credentialFileAccessToken reads the access token out of a stored auth
// file. A missing or malformed file yields an empty token: borrowing is a
// best-effort fill, and a credential we cannot read is one we must not use.
func credentialFileAccessToken(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		return ""
	}
	var document map[string]any
	if errUnmarshal := json.Unmarshal(data, &document); errUnmarshal != nil {
		return ""
	}
	for _, key := range []string{"access_token", "accessToken", "api_key", "apiKey", "token"} {
		if text, okString := document[key].(string); okString {
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}
