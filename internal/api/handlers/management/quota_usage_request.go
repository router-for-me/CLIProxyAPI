package management

import (
	"net/http"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// quotaUsageRequestMatches binds recovery evidence to the selected OAuth
// credential and the provider's canonical, read-only usage endpoint.
func quotaUsageRequestMatches(req *http.Request, auth *coreauth.Auth) bool {
	if req == nil || auth == nil || req.URL == nil || req.Method != http.MethodGet || req.Body != nil ||
		req.URL.Scheme != "https" || req.URL.User != nil || req.URL.RawQuery != "" || req.URL.ForceQuery || req.URL.Fragment != "" || req.URL.RawPath != "" {
		return false
	}
	token, _ := auth.Metadata["access_token"].(string)
	token = strings.TrimSpace(token)
	if token == "" || len(req.Header.Values("Authorization")) != 1 || req.Header.Get("Authorization") != "Bearer "+token || req.Header.Get("Cookie") != "" || req.Header.Get("X-Api-Key") != "" {
		return false
	}
	if req.Host != "" && req.Host != req.URL.Host {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "claude":
		return req.URL.Host == "api.anthropic.com" && req.URL.Path == "/api/oauth/usage"
	case "codex":
		if req.URL.Host != "chatgpt.com" || req.URL.Path != "/backend-api/wham/usage" {
			return false
		}
		accountID, _ := auth.Metadata["account_id"].(string)
		accountHeader := req.Header.Values("Chatgpt-Account-Id")
		// An explicit account selects a subscription independently of the bearer.
		// If identity is unknown, only an omitted selector can be trusted.
		if strings.TrimSpace(accountID) == "" {
			return len(accountHeader) == 0
		}
		return len(accountHeader) == 1 && accountHeader[0] == accountID
	default:
		return false
	}
}
