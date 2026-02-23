package kiro

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
<<<<<<< HEAD

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/misc"
=======
>>>>>>> archive/pr-234-head-20260223
)

// KiroTokenStorage holds the persistent token data for Kiro authentication.
type KiroTokenStorage struct {
	// Type is the provider type for management UI recognition (must be "kiro")
	Type string `json:"type"`
	// AccessToken is the OAuth2 access token for API access
	AccessToken string `json:"access_token"`
	// RefreshToken is used to obtain new access tokens
	RefreshToken string `json:"refresh_token"`
	// ProfileArn is the AWS CodeWhisperer profile ARN
	ProfileArn string `json:"profile_arn"`
	// ExpiresAt is the timestamp when the token expires
	ExpiresAt string `json:"expires_at"`
	// AuthMethod indicates the authentication method used
	AuthMethod string `json:"auth_method"`
	// Provider indicates the OAuth provider
	Provider string `json:"provider"`
	// LastRefresh is the timestamp of the last token refresh
	LastRefresh string `json:"last_refresh"`
	// ClientID is the OAuth client ID (required for token refresh)
	ClientID string `json:"client_id,omitempty"`
	// ClientSecret is the OAuth client secret (required for token refresh)
	ClientSecret string `json:"client_secret,omitempty"`
	// Region is the AWS region
	Region string `json:"region,omitempty"`
	// StartURL is the AWS Identity Center start URL (for IDC auth)
	StartURL string `json:"start_url,omitempty"`
	// Email is the user's email address
	Email string `json:"email,omitempty"`
}

// SaveTokenToFile persists the token storage to the specified file path.
func (s *KiroTokenStorage) SaveTokenToFile(authFilePath string) error {
	cleanPath, err := cleanTokenPath(authFilePath, "kiro token")
	if err != nil {
		return err
	}
	dir := filepath.Dir(cleanPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal token storage: %w", err)
	}

	if err := os.WriteFile(cleanPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write token file: %w", err)
	}

	return nil
}

func cleanTokenPath(path, scope string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("%s: auth file path is empty", scope)
	}
<<<<<<< HEAD
	normalizedInput := filepath.FromSlash(trimmed)
	safe, err := misc.ResolveSafeFilePath(normalizedInput)
	if err != nil {
		return "", fmt.Errorf("%s: auth file path is invalid", scope)
	}

	baseDir, absPath, err := normalizePathWithinBase(safe)
	if err != nil {
		return "", fmt.Errorf("%s: auth file path is invalid", scope)
	}
	if err := denySymlinkPath(baseDir, absPath); err != nil {
		return "", fmt.Errorf("%s: auth file path is invalid", scope)
	}
	return absPath, nil
}

func normalizePathWithinBase(path string) (string, string, error) {
	cleanPath := filepath.Clean(path)
	if cleanPath == "." || cleanPath == ".." {
		return "", "", fmt.Errorf("path is invalid")
	}

	var (
		baseDir string
		absPath string
		err     error
	)

	if filepath.IsAbs(cleanPath) {
		absPath = filepath.Clean(cleanPath)
		baseDir = filepath.Clean(filepath.Dir(absPath))
	} else {
		baseDir, err = os.Getwd()
		if err != nil {
			return "", "", fmt.Errorf("resolve working directory: %w", err)
		}
		baseDir, err = filepath.Abs(baseDir)
		if err != nil {
			return "", "", fmt.Errorf("resolve base directory: %w", err)
		}
		absPath = filepath.Clean(filepath.Join(baseDir, cleanPath))
	}

	if !pathWithinBase(baseDir, absPath) {
		return "", "", fmt.Errorf("path escapes base directory")
	}
	return filepath.Clean(baseDir), filepath.Clean(absPath), nil
}

func pathWithinBase(baseDir, path string) bool {
	rel, err := filepath.Rel(baseDir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func denySymlinkPath(baseDir, targetPath string) error {
	if !pathWithinBase(baseDir, targetPath) {
		return fmt.Errorf("path escapes base directory")
	}
	rel, err := filepath.Rel(baseDir, targetPath)
	if err != nil {
		return fmt.Errorf("resolve relative path: %w", err)
	}
	if rel == "." {
		return nil
	}
	current := filepath.Clean(baseDir)
	for _, component := range strings.Split(rel, string(os.PathSeparator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, errStat := os.Lstat(current)
		if errStat != nil {
			if os.IsNotExist(errStat) {
				return nil
			}
			return fmt.Errorf("stat path: %w", errStat)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink is not allowed in auth file path")
		}
	}
	return nil
}

func cleanAuthPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve auth file path: %w", err)
=======
	clean := filepath.Clean(filepath.FromSlash(trimmed))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%s: auth file path is invalid", scope)
	}
	abs, err := filepath.Abs(clean)
	if err != nil {
		return "", fmt.Errorf("%s: resolve auth file path: %w", scope, err)
>>>>>>> archive/pr-234-head-20260223
	}
	return filepath.Clean(abs), nil
}

// LoadFromFile loads token storage from the specified file path.
func LoadFromFile(authFilePath string) (*KiroTokenStorage, error) {
<<<<<<< HEAD
	cleanPath, err := cleanTokenPath(authFilePath, "kiro token")
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(cleanPath)
=======
	data, err := os.ReadFile(authFilePath)
>>>>>>> archive/pr-234-head-20260223
	if err != nil {
		return nil, fmt.Errorf("failed to read token file: %w", err)
	}

	var storage KiroTokenStorage
	if err := json.Unmarshal(data, &storage); err != nil {
		return nil, fmt.Errorf("failed to parse token file: %w", err)
	}

	return &storage, nil
}

// ToTokenData converts storage to KiroTokenData for API use.
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
