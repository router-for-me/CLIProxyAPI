package access

import (
	"net/http"
	"strings"
)

// CredentialCandidate is one API-key credential extracted from a request.
type CredentialCandidate struct {
	Value  string
	Source string
}

// CredentialCandidatesFromRequest extracts API-key credentials in provider order.
func CredentialCandidatesFromRequest(r *http.Request) ([]CredentialCandidate, bool) {
	if r == nil {
		return nil, false
	}

	authHeader := r.Header.Get("Authorization")
	authHeaderGoogle := r.Header.Get("X-Goog-Api-Key")
	authHeaderAnthropic := r.Header.Get("X-Api-Key")
	queryKey := ""
	queryAuthToken := ""
	if r.URL != nil {
		queryKey = r.URL.Query().Get("key")
		queryAuthToken = r.URL.Query().Get("auth_token")
	}
	if authHeader == "" && authHeaderGoogle == "" && authHeaderAnthropic == "" && queryKey == "" && queryAuthToken == "" {
		return nil, false
	}

	candidates := []CredentialCandidate{
		{Value: extractBearerToken(authHeader), Source: "authorization"},
		{Value: strings.TrimSpace(authHeaderGoogle), Source: "x-goog-api-key"},
		{Value: strings.TrimSpace(authHeaderAnthropic), Source: "x-api-key"},
		{Value: strings.TrimSpace(queryKey), Source: "query-key"},
		{Value: strings.TrimSpace(queryAuthToken), Source: "query-auth-token"},
	}
	return candidates, true
}

func extractBearerToken(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 {
		return header
	}
	if strings.ToLower(parts[0]) != "bearer" {
		return header
	}
	return strings.TrimSpace(parts[1])
}
