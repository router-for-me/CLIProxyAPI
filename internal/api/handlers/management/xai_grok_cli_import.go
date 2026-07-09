package management

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
)

type xaiGrokCLIUploadCandidate struct {
	scope     string
	key       string
	email     string
	subject   string
	expiresAt string
	expires   time.Time
	hasExpiry bool
}

type xaiGrokCLIUploadedAuth struct {
	Type                 string `json:"type"`
	AuthKind             string `json:"auth_kind"`
	Email                string `json:"email,omitempty"`
	AccessToken          string `json:"access_token"`
	BaseURL              string `json:"base_url"`
	Priority             int    `json:"priority,omitempty"`
	GrokCLIVersion       string `json:"grok_cli_version,omitempty"`
	GrokClientIdentifier string `json:"grok_client_identifier,omitempty"`
	GrokAuthScope        string `json:"grok_auth_scope,omitempty"`
	GrokExpiresAt        string `json:"grok_expires_at,omitempty"`
}

func (h *Handler) normalizeUploadedAuthFile(name string, data []byte) (string, []byte, error) {
	convertedName, convertedData, converted, err := convertXAIGrokCLIAuthUpload(data)
	if err != nil || !converted {
		return name, data, err
	}
	if strings.TrimSpace(convertedName) == "" {
		return name, convertedData, nil
	}
	return convertedName, convertedData, nil
}

func convertXAIGrokCLIAuthUpload(data []byte) (string, []byte, bool, error) {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return "", nil, false, nil
	}
	if _, hasType := root["type"]; hasType {
		return "", nil, false, nil
	}

	candidate, ok := selectXAIGrokCLIUploadCandidate(root)
	if !ok {
		return "", nil, false, nil
	}

	email := candidate.email
	if email == "" {
		email = candidate.subject
	}
	if email == "" {
		email = "grok-cli-session"
	}

	auth := xaiGrokCLIUploadedAuth{
		Type:           "xai",
		AuthKind:       "grok_cli_session",
		Email:          email,
		AccessToken:    candidate.key,
		BaseURL:        xaiauth.GrokCLIProxyBaseURL,
		Priority:       100,
		GrokCLIVersion: localGrokCLIVersion(),
		GrokAuthScope:  candidate.scope,
		GrokExpiresAt:  candidate.expiresAt,
	}
	if clientID := localGrokClientIdentifier(); clientID != "" {
		auth.GrokClientIdentifier = clientID
	}

	encoded, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return "", nil, false, err
	}
	encoded = append(encoded, '\n')
	return xaiauth.CredentialFileName(email, candidate.subject), encoded, true, nil
}

func selectXAIGrokCLIUploadCandidate(root map[string]any) (xaiGrokCLIUploadCandidate, bool) {
	if candidate, ok := parseXAIGrokCLIUploadCandidate("", root); ok {
		return candidate, true
	}

	var best xaiGrokCLIUploadCandidate
	found := false
	for scope, raw := range root {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		candidate, ok := parseXAIGrokCLIUploadCandidate(scope, entry)
		if !ok {
			continue
		}
		if !found || candidateBetterThan(candidate, best) {
			best = candidate
			found = true
		}
	}
	return best, found
}

func parseXAIGrokCLIUploadCandidate(scope string, entry map[string]any) (xaiGrokCLIUploadCandidate, bool) {
	key := strings.TrimSpace(jsonString(entry, "key"))
	if key == "" {
		return xaiGrokCLIUploadCandidate{}, false
	}
	if !looksLikeXAIGrokCLIAuthEntry(scope, entry) {
		return xaiGrokCLIUploadCandidate{}, false
	}

	expiresAt := strings.TrimSpace(jsonString(entry, "expires_at"))
	expires, hasExpiry := parseGrokCLIExpiry(expiresAt)
	subject := firstNonEmptyGrokString(
		jsonString(entry, "principal_id"),
		jsonString(entry, "user_id"),
		jsonString(entry, "team_id"),
	)

	return xaiGrokCLIUploadCandidate{
		scope:     strings.TrimSpace(scope),
		key:       key,
		email:     strings.TrimSpace(jsonString(entry, "email")),
		subject:   subject,
		expiresAt: expiresAt,
		expires:   expires,
		hasExpiry: hasExpiry,
	}, true
}

func looksLikeXAIGrokCLIAuthEntry(scope string, entry map[string]any) bool {
	if strings.HasPrefix(strings.TrimSpace(scope), xaiauth.Issuer+"::") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(jsonString(entry, "oidc_issuer")), xaiauth.Issuer) {
		return true
	}
	if strings.TrimSpace(jsonString(entry, "oidc_client_id")) == xaiauth.ClientID {
		return true
	}
	return strings.Contains(strings.ToLower(scope), "auth.x.ai")
}

func candidateBetterThan(candidate, current xaiGrokCLIUploadCandidate) bool {
	if candidate.hasExpiry && current.hasExpiry {
		return candidate.expires.After(current.expires)
	}
	if candidate.hasExpiry != current.hasExpiry {
		return candidate.hasExpiry
	}
	return candidate.email != "" && current.email == ""
}

func parseGrokCLIExpiry(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	if ts, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return ts, true
	}
	if ts, err := time.Parse(time.RFC3339, value); err == nil {
		return ts, true
	}
	return time.Time{}, false
}

func jsonString(values map[string]any, key string) string {
	raw, ok := values[key]
	if !ok {
		return ""
	}
	value, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func firstNonEmptyGrokString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func localGrokCLIVersion() string {
	data, err := os.ReadFile(localGrokFilePath("version.json"))
	if err != nil {
		return xaiauth.DefaultGrokCLIVersion
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return xaiauth.DefaultGrokCLIVersion
	}
	if version := firstNonEmptyGrokString(jsonString(payload, "version"), jsonString(payload, "stable_version")); version != "" {
		return version
	}
	return xaiauth.DefaultGrokCLIVersion
}

func localGrokClientIdentifier() string {
	data, err := os.ReadFile(localGrokFilePath("agent_id"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func localGrokFilePath(name string) string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".grok", name)
}
