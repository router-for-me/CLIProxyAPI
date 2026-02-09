package iflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
)

// IFlowTokenStorage persists iFlow OAuth credentials alongside the derived API key.
type IFlowTokenStorage struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	LastRefresh  string `json:"last_refresh"`
	Expire       string `json:"expired"`
	APIKey       string `json:"api_key"`
	Email        string `json:"email"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	Cookie       string `json:"cookie"`
	Type         string `json:"type"`

	// Metadata holds arbitrary key-value pairs injected via hooks.
	// It is not exported to JSON directly to allow flattening during serialization.
	Metadata map[string]any `json:"-"`
}

// SetMetadata allows external callers to inject metadata into the storage before saving.
func (ts *IFlowTokenStorage) SetMetadata(meta map[string]any) {
	ts.Metadata = meta
}

// SaveTokenToFile serialises the token storage to disk.
func (ts *IFlowTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "iflow"
	if err := os.MkdirAll(filepath.Dir(authFilePath), 0o700); err != nil {
		return fmt.Errorf("iflow token: create directory failed: %w", err)
	}

	f, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("iflow token: create file failed: %w", err)
	}
	defer func() { _ = f.Close() }()

	// Convert struct to map for merging
	data := make(map[string]any)
	temp, errJson := json.Marshal(ts)
	if errJson != nil {
		return fmt.Errorf("failed to marshal struct: %w", errJson)
	}
	if errUnmarshal := json.Unmarshal(temp, &data); errUnmarshal != nil {
		return fmt.Errorf("failed to unmarshal struct map: %w", errUnmarshal)
	}

	// Merge extra metadata
	if ts.Metadata != nil {
		for k, v := range ts.Metadata {
			data[k] = v
		}
	}

	if err = json.NewEncoder(f).Encode(data); err != nil {
		return fmt.Errorf("iflow token: encode token failed: %w", err)
	}
	return nil
}
