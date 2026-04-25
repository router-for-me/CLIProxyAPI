// Package gemini provides authentication and token management functionality
// for Google's Gemini AI services. It handles OAuth2 token storage, serialization,
// and retrieval for maintaining authenticated sessions with the Gemini API.
package gemini

import (
	"fmt"
	"strings"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/auth/base"
)

// GeminiTokenStorage stores OAuth2 token information for Google Gemini API authentication.
// It maintains compatibility with the existing auth system while adding Gemini-specific fields
// for managing access tokens, refresh tokens, and user account information.
//
// Note: Gemini wraps its raw OAuth2 token inside the Token field (type any) rather than
// storing access/refresh tokens as top-level strings, so BaseTokenStorage.AccessToken and
// BaseTokenStorage.RefreshToken remain empty for this provider.
type GeminiTokenStorage struct {
	base.BaseTokenStorage

	// Token holds the raw OAuth2 token data, including access and refresh tokens.
	Token any `json:"token"`

	// ProjectID is the Google Cloud Project ID associated with this token.
	ProjectID string `json:"project_id"`

	// Auto indicates if the project ID was automatically selected.
	Auto bool `json:"auto"`

	// Checked indicates if the associated Cloud AI API has been verified as enabled.
	Checked bool `json:"checked"`
}

// SaveTokenToFile serializes the Gemini token storage to a JSON file.
// This method creates the necessary directory structure and writes the token
// data in JSON format to the specified file path for persistent storage.
//
// Parameters:
//   - authFilePath: The full path where the token file should be saved
//
// Returns:
//   - error: An error if the operation fails, nil otherwise
func (ts *GeminiTokenStorage) SaveTokenToFile(authFilePath string) error {
	ts.Type = "gemini"
	if err := ts.Save(authFilePath, ts); err != nil {
		return fmt.Errorf("gemini token: %w", err)
	}
	return nil
}

// CredentialFileName returns the filename used to persist Gemini CLI credentials.
// When projectID represents multiple projects (comma-separated or literal ALL),
// the suffix is normalized to "all" and a "gemini-" prefix is enforced to keep
// web and CLI generated files consistent.
func CredentialFileName(email, projectID string, includeProviderPrefix bool) string {
	email = strings.TrimSpace(email)
	project := strings.TrimSpace(projectID)
	if strings.EqualFold(project, "all") || strings.Contains(project, ",") {
		return fmt.Sprintf("gemini-%s-all.json", email)
	}
	prefix := ""
	if includeProviderPrefix {
		prefix = "gemini-"
	}
	return fmt.Sprintf("%s%s-%s.json", prefix, email, project)
}
