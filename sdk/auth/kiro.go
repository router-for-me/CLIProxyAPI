package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// KiroAuthenticator implements the Authenticator interface for Kiro (Amazon Q).
type KiroAuthenticator struct{}

// NewKiroAuthenticator creates a new instance of KiroAuthenticator.
func NewKiroAuthenticator() *KiroAuthenticator {
	return &KiroAuthenticator{}
}

// Provider returns the provider identifier.
func (a *KiroAuthenticator) Provider() string {
	return constant.Kiro
}

// Login initiates the login process for Kiro with smart auto-discovery.
func (a *KiroAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if opts.Prompt == nil {
		return nil, fmt.Errorf("interactive prompt is required for Kiro login")
	}

	// 1. Gather existing refresh tokens to identify duplicates
	existingTokens := make(map[string]bool)

	// First, scan auth directory directly for existing Kiro auth files
	if cfg != nil && cfg.AuthDir != "" {
		existingTokens = scanExistingKiroTokens(cfg.AuthDir)
	}

	// Also check via store if available
	if store := GetTokenStore(); store != nil {
		existingAuths, _ := store.List(ctx)
		for _, existing := range existingAuths {
			if existing.Provider == constant.Kiro && !existing.Disabled {
				if kStore, ok := existing.Storage.(*kiro.KiroTokenStorage); ok && kStore.RefreshToken != "" {
					existingTokens[kStore.RefreshToken] = true
				}
				if metaRT, ok := existing.Metadata["refresh_token"].(string); ok && metaRT != "" {
					existingTokens[metaRT] = true
				}
				if metaRT, ok := existing.Metadata["refreshToken"].(string); ok && metaRT != "" {
					existingTokens[metaRT] = true
				}
			}
		}
	}

	// 2. Discover potential new tokens
	allCandidates, err := discoverKiroTokens()
	if err != nil {
		fmt.Printf("Warning: failed to discover existing tokens: %v\n", err)
	}

	var newCandidates []tokenCandidate
	for _, c := range allCandidates {
		if !existingTokens[c.RefreshToken] {
			newCandidates = append(newCandidates, c)
		}
	}

	var refreshToken string

	// 3. Determine flow based on findings
	if len(newCandidates) == 1 {
		// Case A: Exactly one new token found -> Auto-import
		target := newCandidates[0]
		fmt.Printf("Found new AWS SSO token in %s (Modified: %s)\n", target.Path, target.ModTime.Format(time.RFC822))
		fmt.Println("Automatically importing...")
		refreshToken = target.RefreshToken

	} else if len(newCandidates) > 1 {
		// Case B: Multiple new tokens -> Selection menu
		var menu strings.Builder
		menu.WriteString("\nMultiple new Kiro accounts found. Select one:\n")

		for i, c := range newCandidates {
			menu.WriteString(fmt.Sprintf("%d. Import from %s (Modified: %s)\n", i+1, c.Path, c.ModTime.Format(time.RFC822)))
		}

		manualIdx := len(newCandidates) + 1
		menu.WriteString(fmt.Sprintf("%d. Enter Refresh Token manually\n", manualIdx))
		menu.WriteString("\nEnter choice: ")

		choiceStr, err := opts.Prompt(menu.String())
		if err != nil {
			return nil, err
		}

		var choice int
		if _, err := fmt.Sscanf(strings.TrimSpace(choiceStr), "%d", &choice); err != nil {
			return nil, fmt.Errorf("invalid selection")
		}

		if choice > 0 && choice <= len(newCandidates) {
			refreshToken = newCandidates[choice-1].RefreshToken
		} else if choice == manualIdx {
			refreshToken, err = promptForManualToken(opts)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, fmt.Errorf("invalid selection")
		}

	} else {
		// Case C: No new tokens found -> Manual Entry
		if len(allCandidates) > 0 {
			fmt.Println("All discovered tokens are already registered.")
		} else {
			fmt.Println("No AWS SSO tokens discovered in default location.")
		}

		var menu strings.Builder
		menu.WriteString("\nSelect Kiro Login Option:\n")
		menu.WriteString("1. Enter Refresh Token manually\n")
		menu.WriteString("2. Enter path to token file manually\n")
		menu.WriteString("\nEnter choice: ")

		choiceStr, err := opts.Prompt(menu.String())
		if err != nil {
			return nil, err
		}

		switch choice := strings.TrimSpace(choiceStr); choice {
		case "1":
			refreshToken, err = promptForManualToken(opts)
			if err != nil {
				return nil, err
			}
		case "2":
			path, err := opts.Prompt("Enter full path to token file: ")
			if err != nil {
				return nil, err
			}
			path = strings.Trim(strings.TrimSpace(path), "\"'")
			rt, err := extractRefreshTokenFromFile(path)
			if err != nil {
				return nil, fmt.Errorf("failed to read token from %s: %w", path, err)
			}
			if rt == "" {
				return nil, fmt.Errorf("no refresh token found in file %s", path)
			}
			refreshToken = rt
		default:
			return nil, fmt.Errorf("invalid selection")
		}
	}

	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is empty")
	}

	// Final Check: Ensure the chosen/entered token isn't a duplicate (catch manual entry duplicates)
	if existingTokens[refreshToken] {
		return nil, fmt.Errorf("this account is already registered")
	}

	// 4. Import credentials directly (skip refresh verification for AWS Builder ID tokens)
	// AWS Builder ID tokens from SSO cache are already valid - just import them
	fmt.Println("Importing credentials...")

	// Try to get access token from the same source file if available
	var accessToken string
	var expiresAt time.Time

	// Find the candidate that matches our refresh token to get full credentials
	for _, c := range allCandidates {
		if c.RefreshToken == refreshToken {
			// Read full file to get access token
			if fullCreds, err := readFullKiroCredentials(c.Path); err == nil {
				accessToken = fullCreds.AccessToken
				expiresAt = fullCreds.ExpiresAt
			}
			break
		}
	}

	// Build credentials with all available fields
	creds := &kiro.KiroCredentials{
		Type:         "kiro", // Provider type for watcher detection
		RefreshToken: refreshToken,
		AccessToken:  accessToken,
		Region:       kiro.DefaultRegion,
		ExpiresAt:    expiresAt,
		AuthMethod:   "social", // Default to social for Kiro IDE tokens
	}

	// Try to get full credentials including authMethod from source file
	for _, c := range allCandidates {
		if c.RefreshToken == refreshToken {
			if fullCreds, err := readFullKiroCredentials(c.Path); err == nil {
				if fullCreds.AuthMethod != "" {
					creds.AuthMethod = fullCreds.AuthMethod
				}
				if fullCreds.ProfileArn != "" {
					creds.ProfileArn = fullCreds.ProfileArn
				}
				if fullCreds.Provider != "" {
					creds.Provider = fullCreds.Provider
				}
			}
			break
		}
	}

	// Refresh token to get fresh access token
	fmt.Println("Refreshing credentials with Kiro...")
	refreshedCreds, err := kiro.RefreshTokens(creds)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh token: %w", err)
	}
	fmt.Println("Token refreshed successfully!")

	authID := fmt.Sprintf("%s-%d", constant.Kiro, time.Now().Unix())
	authFileName := fmt.Sprintf("%s.json", authID)
	storage := kiro.NewKiroTokenStorage(refreshedCreds)

	metaBytes, _ := json.Marshal(refreshedCreds)
	var metadata map[string]any
	_ = json.Unmarshal(metaBytes, &metadata)

	return &coreauth.Auth{
		ID:        authID,
		Provider:  constant.Kiro,
		FileName:  authFileName,
		Storage:   storage,
		Status:    coreauth.StatusActive,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Metadata:  metadata,
	}, nil
}

func promptForManualToken(opts *LoginOptions) (string, error) {
	rt, err := opts.Prompt("Enter Kiro (AWS Builder ID) Refresh Token: ")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(rt), nil
}

// RefreshLead returns the duration before expiration to trigger a refresh.
func (a *KiroAuthenticator) RefreshLead() *time.Duration {
	d := 5 * time.Minute
	return &d
}

type tokenCandidate struct {
	Path         string
	ModTime      time.Time
	RefreshToken string
}

func discoverKiroTokens() ([]tokenCandidate, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	cacheDir := filepath.Join(home, ".aws", "sso", "cache")
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var candidates []tokenCandidate

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		fullPath := filepath.Join(cacheDir, entry.Name())
		rt, err := extractRefreshTokenFromFile(fullPath)
		if err != nil || rt == "" {
			continue
		}

		info, _ := entry.Info()
		candidates = append(candidates, tokenCandidate{
			Path:         fullPath,
			ModTime:      info.ModTime(),
			RefreshToken: rt,
		})
	}

	// Sort by modification time (newest first)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].ModTime.After(candidates[j].ModTime)
	})

	return candidates, nil
}

// extractRefreshTokenFromFile attempts to read a JSON file and find a refresh token.
func extractRefreshTokenFromFile(path string) (string, error) {
	// Clean path separators
	path = filepath.Clean(path)

	// Expand ~ if present
	if strings.HasPrefix(path, "~") {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, path[1:])
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	// Try to parse as generic JSON
	var data map[string]interface{}
	if err := json.Unmarshal(content, &data); err != nil {
		return "", err
	}

	if rt, ok := data["refreshToken"].(string); ok && rt != "" {
		return rt, nil
	}
	if rt, ok := data["refresh_token"].(string); ok && rt != "" {
		return rt, nil
	}

	return "", nil // Not an error, just no token found
}

// readFullKiroCredentials reads full credentials from a Kiro token file.
func readFullKiroCredentials(path string) (*kiro.KiroCredentials, error) {
	path = filepath.Clean(path)

	if strings.HasPrefix(path, "~") {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, path[1:])
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var data map[string]interface{}
	if err := json.Unmarshal(content, &data); err != nil {
		return nil, err
	}

	creds := &kiro.KiroCredentials{
		Region: kiro.DefaultRegion,
	}

	// Support both camelCase (Kiro format) and snake_case
	if at, ok := data["accessToken"].(string); ok {
		creds.AccessToken = at
	} else if at, ok := data["access_token"].(string); ok {
		creds.AccessToken = at
	}

	if rt, ok := data["refreshToken"].(string); ok {
		creds.RefreshToken = rt
	} else if rt, ok := data["refresh_token"].(string); ok {
		creds.RefreshToken = rt
	}

	if exp, ok := data["expiresAt"].(string); ok {
		if t, err := time.Parse(time.RFC3339, exp); err == nil {
			creds.ExpiresAt = t
		}
	} else if exp, ok := data["expires_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339, exp); err == nil {
			creds.ExpiresAt = t
		}
	}

	if am, ok := data["authMethod"].(string); ok {
		creds.AuthMethod = am
	} else if am, ok := data["auth_method"].(string); ok {
		creds.AuthMethod = am
	}

	if pa, ok := data["profileArn"].(string); ok {
		creds.ProfileArn = pa
	} else if pa, ok := data["profile_arn"].(string); ok {
		creds.ProfileArn = pa
	}

	if p, ok := data["provider"].(string); ok {
		creds.Provider = p
	}

	return creds, nil
}

// scanExistingKiroTokens scans the auth directory for existing Kiro auth files
// and returns a map of refresh tokens that are already registered.
func scanExistingKiroTokens(authDir string) map[string]bool {
	tokens := make(map[string]bool)
	if authDir == "" {
		return tokens
	}

	entries, err := os.ReadDir(authDir)
	if err != nil {
		return tokens
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Only check kiro auth files
		if !strings.HasPrefix(strings.ToLower(name), "kiro") || !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}

		fullPath := filepath.Join(authDir, name)
		content, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}

		var data map[string]interface{}
		if err := json.Unmarshal(content, &data); err != nil {
			continue
		}

		// Check type field to confirm it's a kiro auth file
		if t, ok := data["type"].(string); !ok || strings.ToLower(t) != "kiro" {
			continue
		}

		// Extract refresh token
		if rt, ok := data["refresh_token"].(string); ok && rt != "" {
			tokens[rt] = true
		}
		if rt, ok := data["refreshToken"].(string); ok && rt != "" {
			tokens[rt] = true
		}
	}

	return tokens
}
