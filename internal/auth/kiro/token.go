package kiro

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// KiroTokenStorage implements the TokenStorage interface for Kiro credentials.
type KiroTokenStorage struct {
	*KiroCredentials
}

// NewKiroTokenStorage creates a new instance of KiroTokenStorage.
func NewKiroTokenStorage(creds *KiroCredentials) *KiroTokenStorage {
	return &KiroTokenStorage{
		KiroCredentials: creds,
	}
}

// SaveTokenToFile persists the Kiro credentials to the specified file path.
func (s *KiroTokenStorage) SaveTokenToFile(authFilePath string) error {
	if authFilePath == "" {
		return fmt.Errorf("auth file path cannot be empty")
	}

	dir := filepath.Dir(authFilePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create auth directory: %w", err)
	}

	data, err := json.MarshalIndent(s.KiroCredentials, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal token data: %w", err)
	}

	if err := os.WriteFile(authFilePath, data, 0600); err != nil {
		return fmt.Errorf("failed to write token file: %w", err)
	}

	return nil
}
