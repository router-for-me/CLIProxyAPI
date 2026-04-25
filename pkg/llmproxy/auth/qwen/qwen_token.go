// Package qwen provides authentication and token management functionality
// for Alibaba's Qwen AI services. It handles OAuth2 token storage, serialization,
// and retrieval for maintaining authenticated sessions with the Qwen API.
package qwen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	auth "github.com/KooshaPari/phenotype-go-auth"
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/misc"
)

// QwenTokenStorage extends BaseTokenStorage with Qwen-specific fields for managing
// access tokens, refresh tokens, and user account information.
// It embeds auth.BaseTokenStorage to inherit shared token management functionality.
type QwenTokenStorage struct {
	*auth.BaseTokenStorage

	// ResourceURL is the base URL for API requests.
	ResourceURL string `json:"resource_url"`
}

// NewQwenTokenStorage creates a new QwenTokenStorage instance with the given file path.
// Parameters:
//   - filePath: The full path where the token file should be saved/loaded
//
// Returns:
//   - *QwenTokenStorage: A new QwenTokenStorage instance
func NewQwenTokenStorage(filePath string) *QwenTokenStorage {
	return &QwenTokenStorage{
		BaseTokenStorage: auth.NewBaseTokenStorage(filePath),
	}
}

// SaveTokenToFile serializes the Qwen token storage to a JSON file.
// This method creates the necessary directory structure and writes the token
// data in JSON format to the specified file path for persistent storage.
//
// Parameters:
//   - authFilePath: The full path where the token file should be saved
//
// Returns:
//   - error: An error if the operation fails, nil otherwise
func (ts *QwenTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	if ts.BaseTokenStorage == nil {
		return fmt.Errorf("qwen token: base token storage is nil")
	}

	if _, err := cleanTokenFilePath(authFilePath, "qwen token"); err != nil {
		return err
	}

	ts.BaseTokenStorage.Type = "qwen"
	return ts.BaseTokenStorage.Save()
}

// cleanTokenFilePath validates and normalises a credentials file path.
func cleanTokenFilePath(path, scope string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("%s: auth file path is empty", scope)
	}
	clean := filepath.Clean(filepath.FromSlash(trimmed))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%s: auth file path is invalid", scope)
	}
	abs, err := filepath.Abs(clean)
	if err != nil {
		return "", fmt.Errorf("%s: resolve auth file path: %w", scope, err)
	}
	return filepath.Clean(abs), nil
}
