package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// extractKiroIdentifier extracts a meaningful identifier for file naming.
// Returns account name if provided, otherwise profile ARN ID, then client ID.
// All extracted values are sanitized to prevent path injection attacks.
func extractKiroIdentifier(accountName, profileArn, clientID string) string {
	// Priority 1: Use account name if provided
	if accountName != "" {
		return kiroauth.SanitizeEmailForFilename(accountName)
	}

	// Priority 2: Use profile ARN ID part (sanitized to prevent path injection)
	if profileArn != "" {
		parts := strings.Split(profileArn, "/")
		if len(parts) >= 2 {
			// Sanitize the ARN component to prevent path traversal
			return kiroauth.SanitizeEmailForFilename(parts[len(parts)-1])
		}
	}

	// Priority 3: Use client ID (for IDC auth without email/profileArn)
	if clientID != "" {
		return kiroauth.SanitizeEmailForFilename(clientID)
	}

	// Fallback: timestamp
	return fmt.Sprintf("%d", time.Now().UnixNano()%100000)
}

// KiroAuthenticator implements OAuth authentication for Kiro with Google login.
type KiroAuthenticator struct{}

// NewKiroAuthenticator constructs a Kiro authenticator.
func NewKiroAuthenticator() *KiroAuthenticator {
	return &KiroAuthenticator{}
}

// Provider returns the provider key for the authenticator.
func (a *KiroAuthenticator) Provider() string {
	return "kiro"
}

// RefreshLead indicates how soon before expiry a refresh should be attempted.
// Set to 20 minutes for proactive refresh before token expiry.
func (a *KiroAuthenticator) RefreshLead() *time.Duration {
	d := 20 * time.Minute
	return &d
}

// createAuthRecord creates an auth record from token data.
func (a *KiroAuthenticator) createAuthRecord(tokenData *kiroauth.KiroTokenData, source string) (*coreauth.Auth, error) {
	// Parse expires_at
	expiresAt, err := time.Parse(time.RFC3339, tokenData.ExpiresAt)
	if err != nil {
		expiresAt = time.Now().Add(1 * time.Hour)
	}

	// Determine label and identifier based on auth method
	var label, idPart string
	if tokenData.AuthMethod == "idc" {
		label = "kiro-idc"
		// For IDC auth, always use clientID as identifier
		if tokenData.ClientID != "" {
			idPart = kiroauth.SanitizeEmailForFilename(tokenData.ClientID)
		} else {
			idPart = fmt.Sprintf("%d", time.Now().UnixNano()%100000)
		}
	} else {
		label = fmt.Sprintf("kiro-%s", source)
		idPart = extractKiroIdentifier(tokenData.Email, tokenData.ProfileArn, tokenData.ClientID)
	}

	now := time.Now()
	fileName := fmt.Sprintf("%s-%s.json", label, idPart)

	metadata := map[string]any{
		"type":          "kiro",
		"access_token":  tokenData.AccessToken,
		"refresh_token": tokenData.RefreshToken,
		"profile_arn":   tokenData.ProfileArn,
		"expires_at":    tokenData.ExpiresAt,
		"auth_method":   tokenData.AuthMethod,
		"provider":      tokenData.Provider,
		"client_id":     tokenData.ClientID,
		"client_secret": tokenData.ClientSecret,
		"email":         tokenData.Email,
	}

	// Add IDC-specific fields if present
	if tokenData.StartURL != "" {
		metadata["start_url"] = tokenData.StartURL
	}
	if tokenData.Region != "" {
		metadata["region"] = tokenData.Region
	}

	attributes := map[string]string{
		"profile_arn": tokenData.ProfileArn,
		"source":      source,
		"email":       tokenData.Email,
	}

	// Add IDC-specific attributes if present
	if tokenData.AuthMethod == "idc" {
		attributes["source"] = "aws-idc"
		if tokenData.StartURL != "" {
			attributes["start_url"] = tokenData.StartURL
		}
		if tokenData.Region != "" {
			attributes["region"] = tokenData.Region
		}
	}

	record := &coreauth.Auth{
		ID:        fileName,
		Provider:  "kiro",
		FileName:  fileName,
		Label:     label,
		Status:    coreauth.StatusActive,
		CreatedAt: now,
		UpdatedAt: now,
		Metadata:  metadata,
		Attributes: attributes,
		// NextRefreshAfter: 20 minutes before expiry
		NextRefreshAfter: expiresAt.Add(-20 * time.Minute),
	}

	if tokenData.Email != "" {
		fmt.Printf("\n✓ Kiro authentication completed successfully! (Account: %s)\n", tokenData.Email)
	} else {
		fmt.Println("\n✓ Kiro authentication completed successfully!")
	}

	return record, nil
}

// Login performs OAuth login for Kiro with AWS (Builder ID or IDC).
// This shows a method selection prompt and handles both flows.
func (a *KiroAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("kiro auth: configuration is required")
	}

	// Use the unified method selection flow (Builder ID or IDC)
	ssoClient := kiroauth.NewSSOOIDCClient(cfg)
	tokenData, err := ssoClient.LoginWithMethodSelection(ctx)
	if err != nil {
		return nil, fmt.Errorf("login failed: %w", err)
	}

	return a.createAuthRecord(tokenData, "aws")
}

// LoginWithAuthCode performs OAuth login for Kiro with AWS Builder ID using authorization code flow.
// This provides a better UX than device code flow as it uses automatic browser callback.
func (a *KiroAuthenticator) LoginWithAuthCode(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("kiro auth: configuration is required")
	}

	oauth := kiroauth.NewKiroOAuth(cfg)

	// Use AWS Builder ID authorization code flow
	tokenData, err := oauth.LoginWithBuilderIDAuthCode(ctx)
	if err != nil {
		return nil, fmt.Errorf("login failed: %w", err)
	}

	// Parse expires_at
	expiresAt, err := time.Parse(time.RFC3339, tokenData.ExpiresAt)
	if err != nil {
		expiresAt = time.Now().Add(1 * time.Hour)
	}

	// Extract identifier for file naming
	idPart := extractKiroIdentifier(tokenData.Email, tokenData.ProfileArn, tokenData.ClientID)

	now := time.Now()
	fileName := fmt.Sprintf("kiro-aws-%s.json", idPart)

	record := &coreauth.Auth{
		ID:        fileName,
		Provider:  "kiro",
		FileName:  fileName,
		Label:     "kiro-aws",
		Status:    coreauth.StatusActive,
		CreatedAt: now,
		UpdatedAt: now,
		Metadata: map[string]any{
			"type":          "kiro",
			"access_token":  tokenData.AccessToken,
			"refresh_token": tokenData.RefreshToken,
			"profile_arn":   tokenData.ProfileArn,
			"expires_at":    tokenData.ExpiresAt,
			"auth_method":   tokenData.AuthMethod,
			"provider":      tokenData.Provider,
			"client_id":     tokenData.ClientID,
			"client_secret": tokenData.ClientSecret,
			"email":         tokenData.Email,
		},
		Attributes: map[string]string{
			"profile_arn": tokenData.ProfileArn,
			"source":      "aws-builder-id-authcode",
			"email":       tokenData.Email,
		},
		// NextRefreshAfter: 20 minutes before expiry
		NextRefreshAfter: expiresAt.Add(-20 * time.Minute),
	}

	if tokenData.Email != "" {
		fmt.Printf("\n✓ Kiro authentication completed successfully! (Account: %s)\n", tokenData.Email)
	} else {
		fmt.Println("\n✓ Kiro authentication completed successfully!")
	}

	return record, nil
}

// LoginWithGoogle performs OAuth login for Kiro with Google.
// NOTE: Google login is not available for third-party applications due to AWS Cognito restrictions.
// Please use AWS Builder ID or import your token from Kiro IDE.
func (a *KiroAuthenticator) LoginWithGoogle(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	return nil, fmt.Errorf("Google login is not available for third-party applications due to AWS Cognito restrictions.\n\nAlternatives:\n  1. Use AWS Builder ID: cliproxy kiro --builder-id\n  2. Import token from Kiro IDE: cliproxy kiro --import\n\nTo get a token from Kiro IDE:\n  1. Open Kiro IDE and login with Google\n  2. Find: ~/.kiro/kiro-auth-token.json\n  3. Run: cliproxy kiro --import")
}

// LoginWithGitHub performs OAuth login for Kiro with GitHub.
// NOTE: GitHub login is not available for third-party applications due to AWS Cognito restrictions.
// Please use AWS Builder ID or import your token from Kiro IDE.
func (a *KiroAuthenticator) LoginWithGitHub(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	return nil, fmt.Errorf("GitHub login is not available for third-party applications due to AWS Cognito restrictions.\n\nAlternatives:\n  1. Use AWS Builder ID: cliproxy kiro --builder-id\n  2. Import token from Kiro IDE: cliproxy kiro --import\n\nTo get a token from Kiro IDE:\n  1. Open Kiro IDE and login with GitHub\n  2. Find: ~/.kiro/kiro-auth-token.json\n  3. Run: cliproxy kiro --import")
}

// ImportFromKiroIDE imports token from Kiro IDE's token file.
func (a *KiroAuthenticator) ImportFromKiroIDE(ctx context.Context, cfg *config.Config) (*coreauth.Auth, error) {
	tokenData, err := kiroauth.LoadKiroIDEToken()
	if err != nil {
		return nil, fmt.Errorf("failed to load Kiro IDE token: %w", err)
	}

	// Parse expires_at
	expiresAt, err := time.Parse(time.RFC3339, tokenData.ExpiresAt)
	if err != nil {
		expiresAt = time.Now().Add(1 * time.Hour)
	}

	// Extract email from JWT if not already set (for imported tokens)
	if tokenData.Email == "" {
		tokenData.Email = kiroauth.ExtractEmailFromJWT(tokenData.AccessToken)
	}

	// Extract identifier for file naming
	idPart := extractKiroIdentifier(tokenData.Email, tokenData.ProfileArn, tokenData.ClientID)
	// Sanitize provider to prevent path traversal (defense-in-depth)
	provider := kiroauth.SanitizeEmailForFilename(strings.ToLower(strings.TrimSpace(tokenData.Provider)))
	if provider == "" {
		provider = "imported" // Fallback for legacy tokens without provider
	}

	now := time.Now()
	fileName := fmt.Sprintf("kiro-%s-%s.json", provider, idPart)

	record := &coreauth.Auth{
		ID:        fileName,
		Provider:  "kiro",
		FileName:  fileName,
		Label:     fmt.Sprintf("kiro-%s", provider),
		Status:    coreauth.StatusActive,
		CreatedAt: now,
		UpdatedAt: now,
		Metadata: map[string]any{
			"type":           "kiro",
			"access_token":   tokenData.AccessToken,
			"refresh_token":  tokenData.RefreshToken,
			"profile_arn":    tokenData.ProfileArn,
			"expires_at":     tokenData.ExpiresAt,
			"auth_method":    tokenData.AuthMethod,
			"provider":       tokenData.Provider,
			"client_id":      tokenData.ClientID,
			"client_secret":  tokenData.ClientSecret,
			"client_id_hash": tokenData.ClientIDHash,
			"email":          tokenData.Email,
			"region":         tokenData.Region,
			"start_url":      tokenData.StartURL,
		},
		Attributes: map[string]string{
			"profile_arn": tokenData.ProfileArn,
			"source":      "kiro-ide-import",
			"email":       tokenData.Email,
			"region":      tokenData.Region,
		},
		// NextRefreshAfter: 20 minutes before expiry
		NextRefreshAfter: expiresAt.Add(-20 * time.Minute),
	}

	// Display the email if extracted
	if tokenData.Email != "" {
		fmt.Printf("\n✓ Imported Kiro token from IDE (Provider: %s, Account: %s)\n", tokenData.Provider, tokenData.Email)
	} else {
		fmt.Printf("\n✓ Imported Kiro token from IDE (Provider: %s)\n", tokenData.Provider)
	}

	return record, nil
}

// Refresh refreshes an expired Kiro token using AWS SSO OIDC.
func (a *KiroAuthenticator) Refresh(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if auth == nil || auth.Metadata == nil {
		return nil, fmt.Errorf("invalid auth record")
	}

	refreshToken, ok := auth.Metadata["refresh_token"].(string)
	if !ok || refreshToken == "" {
		return nil, fmt.Errorf("refresh token not found")
	}

	clientID, _ := auth.Metadata["client_id"].(string)
	clientSecret, _ := auth.Metadata["client_secret"].(string)
	clientIDHash, _ := auth.Metadata["client_id_hash"].(string)
	authMethod, _ := auth.Metadata["auth_method"].(string)
	startURL, _ := auth.Metadata["start_url"].(string)
	region, _ := auth.Metadata["region"].(string)

	// For Enterprise Kiro IDE (IDC auth), try to load clientId/clientSecret from device registration
	// if they are missing from metadata. This handles the case where token was imported without
	// clientId/clientSecret but has clientIdHash.
	if (clientID == "" || clientSecret == "") && clientIDHash != "" {
		if loadedClientID, loadedClientSecret, err := loadDeviceRegistrationCredentials(clientIDHash); err == nil {
			clientID = loadedClientID
			clientSecret = loadedClientSecret
		}
	}

	var tokenData *kiroauth.KiroTokenData
	var err error

	ssoClient := kiroauth.NewSSOOIDCClient(cfg)

	// Use SSO OIDC refresh for AWS Builder ID or IDC, otherwise use Kiro's OAuth refresh endpoint
	switch {
	case clientID != "" && clientSecret != "" && authMethod == "idc" && region != "":
		// IDC refresh with region-specific endpoint
		tokenData, err = ssoClient.RefreshTokenWithRegion(ctx, clientID, clientSecret, refreshToken, region, startURL)
	case clientID != "" && clientSecret != "" && (authMethod == "builder-id" || authMethod == "idc"):
		// Builder ID or IDC refresh with default endpoint (us-east-1)
		tokenData, err = ssoClient.RefreshToken(ctx, clientID, clientSecret, refreshToken)
	default:
		// Fallback to Kiro's refresh endpoint (for social auth: Google/GitHub)
		oauth := kiroauth.NewKiroOAuth(cfg)
		tokenData, err = oauth.RefreshToken(ctx, refreshToken)
	}

	if err != nil {
		return nil, fmt.Errorf("token refresh failed: %w", err)
	}

	// Parse expires_at
	expiresAt, err := time.Parse(time.RFC3339, tokenData.ExpiresAt)
	if err != nil {
		expiresAt = time.Now().Add(1 * time.Hour)
	}

	// Clone auth to avoid mutating the input parameter
	updated := auth.Clone()
	now := time.Now()
	updated.UpdatedAt = now
	updated.LastRefreshedAt = now
	updated.Metadata["access_token"] = tokenData.AccessToken
	updated.Metadata["refresh_token"] = tokenData.RefreshToken
	updated.Metadata["expires_at"] = tokenData.ExpiresAt
	updated.Metadata["last_refresh"] = now.Format(time.RFC3339) // For double-check optimization
	// Store clientId/clientSecret if they were loaded from device registration
	if clientID != "" && updated.Metadata["client_id"] == nil {
		updated.Metadata["client_id"] = clientID
	}
	if clientSecret != "" && updated.Metadata["client_secret"] == nil {
		updated.Metadata["client_secret"] = clientSecret
	}
	// NextRefreshAfter: 20 minutes before expiry
	updated.NextRefreshAfter = expiresAt.Add(-20 * time.Minute)

	return updated, nil
}

// loadDeviceRegistrationCredentials loads clientId and clientSecret from device registration file.
// This is used when refreshing tokens that were imported without clientId/clientSecret.
func loadDeviceRegistrationCredentials(clientIDHash string) (clientID, clientSecret string, err error) {
	if clientIDHash == "" {
		return "", "", fmt.Errorf("clientIdHash is empty")
	}

	// Sanitize clientIdHash to prevent path traversal
	if strings.Contains(clientIDHash, "/") || strings.Contains(clientIDHash, "\\") || strings.Contains(clientIDHash, "..") {
		return "", "", fmt.Errorf("invalid clientIdHash: contains path separator")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("failed to get home directory: %w", err)
	}

	deviceRegPath := filepath.Join(homeDir, ".aws", "sso", "cache", clientIDHash+".json")
	data, err := os.ReadFile(deviceRegPath)
	if err != nil {
		return "", "", fmt.Errorf("failed to read device registration file: %w", err)
	}

	var deviceReg struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	}

	if err := json.Unmarshal(data, &deviceReg); err != nil {
		return "", "", fmt.Errorf("failed to parse device registration: %w", err)
	}

	if deviceReg.ClientID == "" || deviceReg.ClientSecret == "" {
		return "", "", fmt.Errorf("device registration missing clientId or clientSecret")
	}

	return deviceReg.ClientID, deviceReg.ClientSecret, nil
}
