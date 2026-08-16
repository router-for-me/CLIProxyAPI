package cursor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
)

// TokenStorage persists Cursor OAuth credentials and the latest model snapshot.
type TokenStorage struct {
	AccessToken  string         `json:"access_token"`
	RefreshToken string         `json:"refresh_token"`
	Expired      string         `json:"expired,omitempty"`
	LastRefresh  string         `json:"last_refresh,omitempty"`
	AuthKind     string         `json:"auth_kind"`
	Type         string         `json:"type"`
	Models       []ModelDetails `json:"models"`
	Metadata     map[string]any `json:"-"`
}

func (s *TokenStorage) SetMetadata(metadata map[string]any) { s.Metadata = metadata }

func (s *TokenStorage) SaveTokenToFile(path string) error {
	if s == nil {
		return fmt.Errorf("cursor token storage: storage is nil")
	}
	misc.LogSavingCredentials(path)
	s.Type = Provider
	s.AuthKind = OAuthKind
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("cursor token storage: create directory: %w", err)
	}
	data, errMerge := misc.MergeMetadata(s, s.Metadata)
	if errMerge != nil {
		return fmt.Errorf("cursor token storage: merge metadata: %w", errMerge)
	}
	raw, errMarshal := json.MarshalIndent(data, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("cursor token storage: marshal credentials: %w", errMarshal)
	}
	if errWrite := os.WriteFile(path, append(raw, '\n'), 0o600); errWrite != nil {
		return fmt.Errorf("cursor token storage: write credentials: %w", errWrite)
	}
	return nil
}
