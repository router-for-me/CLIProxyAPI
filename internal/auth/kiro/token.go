package kiro

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type KiroTokenStorage struct {
	Type         string `json:"type"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ProfileArn   string `json:"profile_arn"`
	ExpiresAt    string `json:"expires_at"`
	AuthMethod   string `json:"auth_method"`
	Provider     string `json:"provider"`
	LastRefresh  string `json:"last_refresh"`
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
	Region       string `json:"region,omitempty"`
	StartURL     string `json:"start_url,omitempty"`
	Email        string `json:"email,omitempty"`
}

func (s *KiroTokenStorage) SaveTokenToFile(authFilePath string) error {
	dir := filepath.Dir(authFilePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal token storage: %w", err)
	}

	if err := os.WriteFile(authFilePath, data, 0600); err != nil {
		return fmt.Errorf("failed to write token file: %w", err)
	}

	return nil
}

func LoadFromFile(authFilePath string) (*KiroTokenStorage, error) {
	data, err := os.ReadFile(authFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read token file: %w", err)
	}

	var storage KiroTokenStorage
	if err := json.Unmarshal(data, &storage); err != nil {
		return nil, fmt.Errorf("failed to parse token file: %w", err)
	}

	return &storage, nil
}

func (s *KiroTokenStorage) ToTokenData() *KiroTokenData {
	return &KiroTokenData{
		AccessToken:  s.AccessToken,
		RefreshToken: s.RefreshToken,
		ProfileArn:   s.ProfileArn,
		ExpiresAt:    s.ExpiresAt,
		AuthMethod:   s.AuthMethod,
		Provider:     s.Provider,
		ClientID:     s.ClientID,
		ClientSecret: s.ClientSecret,
		Region:       s.Region,
		StartURL:     s.StartURL,
		Email:        s.Email,
	}
}
