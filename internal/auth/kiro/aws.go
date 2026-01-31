// Package kiro provides authentication functionality for AWS CodeWhisperer (Kiro) API.
// It includes interfaces and implementations for token storage and authentication methods.
package kiro

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PKCECodes holds PKCE verification codes for OAuth2 PKCE flow
type PKCECodes struct {
	// CodeVerifier is the cryptographically random string used to correlate
	// the authorization request to the token request
	CodeVerifier string `json:"code_verifier"`
	// CodeChallenge is the SHA256 hash of the code verifier, base64url-encoded
	CodeChallenge string `json:"code_challenge"`
}

// KiroTokenData holds OAuth token information from AWS CodeWhisperer (Kiro)
type KiroTokenData struct {
	// AccessToken is the OAuth2 access token for API access
	AccessToken string `json:"accessToken"`
	// RefreshToken is used to obtain new access tokens
	RefreshToken string `json:"refreshToken"`
	// ProfileArn is the AWS CodeWhisperer profile ARN
	ProfileArn string `json:"profileArn"`
	// ExpiresAt is the timestamp when the token expires
	ExpiresAt string `json:"expiresAt"`
	// AuthMethod indicates the authentication method used (e.g., "builder-id", "social", "idc")
	AuthMethod string `json:"authMethod"`
	// Provider indicates the OAuth provider (e.g., "AWS", "Google", "Enterprise")
	Provider string `json:"provider"`
	// ClientID is the OIDC client ID (needed for token refresh)
	ClientID string `json:"clientId,omitempty"`
	// ClientSecret is the OIDC client secret (needed for token refresh)
	ClientSecret string `json:"clientSecret,omitempty"`
	// ClientIDHash is the hash of client ID used to locate device registration file
	// (Enterprise Kiro IDE stores clientId/clientSecret in ~/.aws/sso/cache/{clientIdHash}.json)
	ClientIDHash string `json:"clientIdHash,omitempty"`
	// Email is the user's email address (used for file naming)
	Email string `json:"email,omitempty"`
	// StartURL is the IDC/Identity Center start URL (only for IDC auth method)
	StartURL string `json:"startUrl,omitempty"`
	// Region is the AWS region for IDC authentication (only for IDC auth method)
	Region string `json:"region,omitempty"`
}

// KiroAuthBundle aggregates authentication data after OAuth flow completion
type KiroAuthBundle struct {
	// TokenData contains the OAuth tokens from the authentication flow
	TokenData KiroTokenData `json:"token_data"`
	// LastRefresh is the timestamp of the last token refresh
	LastRefresh string `json:"last_refresh"`
}

// KiroUsageInfo represents usage information from CodeWhisperer API
type KiroUsageInfo struct {
	// SubscriptionTitle is the subscription plan name (e.g., "KIRO FREE")
	SubscriptionTitle string `json:"subscription_title"`
	// CurrentUsage is the current credit usage
	CurrentUsage float64 `json:"current_usage"`
	// UsageLimit is the maximum credit limit
	UsageLimit float64 `json:"usage_limit"`
	// NextReset is the timestamp of the next usage reset
	NextReset string `json:"next_reset"`
}

// KiroModel represents a model available through the CodeWhisperer API
type KiroModel struct {
	// ModelID is the unique identifier for the model
	ModelID string `json:"modelId"`
	// ModelName is the human-readable name
	ModelName string `json:"modelName"`
	// Description is the model description
	Description string `json:"description"`
	// RateMultiplier is the credit multiplier for this model
	RateMultiplier float64 `json:"rateMultiplier"`
	// RateUnit is the unit for rate calculation (e.g., "credit")
	RateUnit string `json:"rateUnit"`
	// MaxInputTokens is the maximum input token limit
	MaxInputTokens int `json:"maxInputTokens,omitempty"`
}

// KiroIDETokenFile is the default path to Kiro IDE's token file
const KiroIDETokenFile = ".aws/sso/cache/kiro-auth-token.json"

// Default retry configuration for file reading
const (
	defaultTokenReadMaxAttempts = 10               // Maximum retry attempts
	defaultTokenReadBaseDelay   = 50 * time.Millisecond // Base delay between retries
)

// isTransientFileError checks if the error is a transient file access error
// that may be resolved by retrying (e.g., file locked by another process on Windows).
func isTransientFileError(err error) bool {
	if err == nil {
		return false
	}

	// Check for OS-level file access errors (Windows sharing violation, etc.)
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		// Windows sharing violation (ERROR_SHARING_VIOLATION = 32)
		// Windows lock violation (ERROR_LOCK_VIOLATION = 33)
		errStr := pathErr.Err.Error()
		if strings.Contains(errStr, "being used by another process") ||
			strings.Contains(errStr, "sharing violation") ||
			strings.Contains(errStr, "lock violation") {
			return true
		}
	}

	// Check error message for common transient patterns
	errMsg := strings.ToLower(err.Error())
	transientPatterns := []string{
		"being used by another process",
		"sharing violation",
		"lock violation",
		"access is denied",
		"unexpected end of json",
		"unexpected eof",
	}
	for _, pattern := range transientPatterns {
		if strings.Contains(errMsg, pattern) {
			return true
		}
	}

	return false
}

// LoadKiroIDETokenWithRetry loads token data from Kiro IDE's token file with retry logic.
// This handles transient file access errors (e.g., file locked by Kiro IDE during write).
// maxAttempts: maximum number of retry attempts (default 10 if <= 0)
// baseDelay: base delay between retries with exponential backoff (default 50ms if <= 0)
func LoadKiroIDETokenWithRetry(maxAttempts int, baseDelay time.Duration) (*KiroTokenData, error) {
	if maxAttempts <= 0 {
		maxAttempts = defaultTokenReadMaxAttempts
	}
	if baseDelay <= 0 {
		baseDelay = defaultTokenReadBaseDelay
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		token, err := LoadKiroIDEToken()
		if err == nil {
			return token, nil
		}
		lastErr = err

		// Only retry for transient errors
		if !isTransientFileError(err) {
			return nil, err
		}

		// Exponential backoff: delay * 2^attempt, capped at 500ms
		delay := baseDelay * time.Duration(1<<uint(attempt))
		if delay > 500*time.Millisecond {
			delay = 500 * time.Millisecond
		}
		time.Sleep(delay)
	}

	return nil, fmt.Errorf("failed to read token file after %d attempts: %w", maxAttempts, lastErr)
}

// LoadKiroIDEToken loads token data from Kiro IDE's token file.
// For Enterprise Kiro IDE (IDC auth), it also loads clientId and clientSecret
// from the device registration file referenced by clientIdHash.
func LoadKiroIDEToken() (*KiroTokenData, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	tokenPath := filepath.Join(homeDir, KiroIDETokenFile)
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read Kiro IDE token file (%s): %w", tokenPath, err)
	}

	var token KiroTokenData
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, fmt.Errorf("failed to parse Kiro IDE token: %w", err)
	}

	if token.AccessToken == "" {
		return nil, fmt.Errorf("access token is empty in Kiro IDE token file")
	}

	// Normalize AuthMethod to lowercase (Kiro IDE uses "IdC" but we expect "idc")
	token.AuthMethod = strings.ToLower(token.AuthMethod)

	// For Enterprise Kiro IDE (IDC auth), load clientId and clientSecret from device registration
	// The device registration file is located at ~/.aws/sso/cache/{clientIdHash}.json
	if token.ClientIDHash != "" && token.ClientID == "" {
		if err := loadDeviceRegistration(homeDir, token.ClientIDHash, &token); err != nil {
			// Log warning but don't fail - token might still work for some operations
			fmt.Printf("warning: failed to load device registration for clientIdHash %s: %v\n", token.ClientIDHash, err)
		}
	}

	return &token, nil
}

// loadDeviceRegistration loads clientId and clientSecret from the device registration file.
// Enterprise Kiro IDE stores these in ~/.aws/sso/cache/{clientIdHash}.json
func loadDeviceRegistration(homeDir, clientIDHash string, token *KiroTokenData) error {
	if clientIDHash == "" {
		return fmt.Errorf("clientIdHash is empty")
	}

	// Sanitize clientIdHash to prevent path traversal
	if strings.Contains(clientIDHash, "/") || strings.Contains(clientIDHash, "\\") || strings.Contains(clientIDHash, "..") {
		return fmt.Errorf("invalid clientIdHash: contains path separator")
	}

	deviceRegPath := filepath.Join(homeDir, ".aws", "sso", "cache", clientIDHash+".json")
	data, err := os.ReadFile(deviceRegPath)
	if err != nil {
		return fmt.Errorf("failed to read device registration file (%s): %w", deviceRegPath, err)
	}

	// Device registration file structure
	var deviceReg struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
		ExpiresAt    string `json:"expiresAt"`
	}

	if err := json.Unmarshal(data, &deviceReg); err != nil {
		return fmt.Errorf("failed to parse device registration: %w", err)
	}

	if deviceReg.ClientID == "" || deviceReg.ClientSecret == "" {
		return fmt.Errorf("device registration missing clientId or clientSecret")
	}

	token.ClientID = deviceReg.ClientID
	token.ClientSecret = deviceReg.ClientSecret

	return nil
}

// LoadKiroTokenFromPath loads token data from a custom path.
// This supports multiple accounts by allowing different token files.
// For Enterprise Kiro IDE (IDC auth), it also loads clientId and clientSecret
// from the device registration file referenced by clientIdHash.
func LoadKiroTokenFromPath(tokenPath string) (*KiroTokenData, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	// Expand ~ to home directory
	if len(tokenPath) > 0 && tokenPath[0] == '~' {
		tokenPath = filepath.Join(homeDir, tokenPath[1:])
	}

	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read token file (%s): %w", tokenPath, err)
	}

	var token KiroTokenData
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, fmt.Errorf("failed to parse token file: %w", err)
	}

	if token.AccessToken == "" {
		return nil, fmt.Errorf("access token is empty in token file")
	}

	// Normalize AuthMethod to lowercase (Kiro IDE uses "IdC" but we expect "idc")
	token.AuthMethod = strings.ToLower(token.AuthMethod)

	// For Enterprise Kiro IDE (IDC auth), load clientId and clientSecret from device registration
	if token.ClientIDHash != "" && token.ClientID == "" {
		if err := loadDeviceRegistration(homeDir, token.ClientIDHash, &token); err != nil {
			// Log warning but don't fail - token might still work for some operations
			fmt.Printf("warning: failed to load device registration for clientIdHash %s: %v\n", token.ClientIDHash, err)
		}
	}

	return &token, nil
}

// ListKiroTokenFiles lists all Kiro token files in the cache directory.
// This supports multiple accounts by finding all token files.
func ListKiroTokenFiles() ([]string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	cacheDir := filepath.Join(homeDir, ".aws", "sso", "cache")
	
	// Check if directory exists
	if _, err := os.Stat(cacheDir); os.IsNotExist(err) {
		return nil, nil // No token files
	}

	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read cache directory: %w", err)
	}

	var tokenFiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Look for kiro token files only (avoid matching unrelated AWS SSO cache files)
		if strings.HasSuffix(name, ".json") && strings.HasPrefix(name, "kiro") {
			tokenFiles = append(tokenFiles, filepath.Join(cacheDir, name))
		}
	}

	return tokenFiles, nil
}

// LoadAllKiroTokens loads all Kiro tokens from the cache directory.
// This supports multiple accounts.
func LoadAllKiroTokens() ([]*KiroTokenData, error) {
	files, err := ListKiroTokenFiles()
	if err != nil {
		return nil, err
	}

	var tokens []*KiroTokenData
	for _, file := range files {
		token, err := LoadKiroTokenFromPath(file)
		if err != nil {
			// Skip invalid token files
			continue
		}
		tokens = append(tokens, token)
	}

	return tokens, nil
}

// JWTClaims represents the claims we care about from a JWT token.
// JWT tokens from Kiro/AWS contain user information in the payload.
type JWTClaims struct {
	Email         string `json:"email,omitempty"`
	Sub           string `json:"sub,omitempty"`
	PreferredUser string `json:"preferred_username,omitempty"`
	Name          string `json:"name,omitempty"`
	Iss           string `json:"iss,omitempty"`
}

// ExtractEmailFromJWT extracts the user's email from a JWT access token.
// JWT tokens typically have format: header.payload.signature
// The payload is base64url-encoded JSON containing user claims.
func ExtractEmailFromJWT(accessToken string) string {
	if accessToken == "" {
		return ""
	}

	// JWT format: header.payload.signature
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return ""
	}

	// Decode the payload (second part)
	payload := parts[1]

	// Add padding if needed (base64url requires padding)
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}

	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		// Try RawURLEncoding (no padding)
		decoded, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return ""
		}
	}

	var claims JWTClaims
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return ""
	}

	// Return email if available
	if claims.Email != "" {
		return claims.Email
	}

	// Fallback to preferred_username (some providers use this)
	if claims.PreferredUser != "" && strings.Contains(claims.PreferredUser, "@") {
		return claims.PreferredUser
	}

	// Fallback to sub if it looks like an email
	if claims.Sub != "" && strings.Contains(claims.Sub, "@") {
		return claims.Sub
	}

	return ""
}

// SanitizeEmailForFilename sanitizes an email address for use in a filename.
// Replaces special characters with underscores and prevents path traversal attacks.
// Also handles URL-encoded characters to prevent encoded path traversal attempts.
func SanitizeEmailForFilename(email string) string {
	if email == "" {
		return ""
	}

	result := email

	// First, handle URL-encoded path traversal attempts (%2F, %2E, %5C, etc.)
	// This prevents encoded characters from bypassing the sanitization.
	// Note: We replace % last to catch any remaining encodings including double-encoding (%252F)
	result = strings.ReplaceAll(result, "%2F", "_") // /
	result = strings.ReplaceAll(result, "%2f", "_")
	result = strings.ReplaceAll(result, "%5C", "_") // \
	result = strings.ReplaceAll(result, "%5c", "_")
	result = strings.ReplaceAll(result, "%2E", "_") // .
	result = strings.ReplaceAll(result, "%2e", "_")
	result = strings.ReplaceAll(result, "%00", "_") // null byte
	result = strings.ReplaceAll(result, "%", "_")   // Catch remaining % to prevent double-encoding attacks

	// Replace characters that are problematic in filenames
	// Keep @ and . in middle but replace other special characters
	for _, char := range []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|", " ", "\x00"} {
		result = strings.ReplaceAll(result, char, "_")
	}

	// Prevent path traversal: replace leading dots in each path component
	// This handles cases like "../../../etc/passwd" → "_.._.._.._etc_passwd"
	parts := strings.Split(result, "_")
	for i, part := range parts {
		for strings.HasPrefix(part, ".") {
			part = "_" + part[1:]
		}
		parts[i] = part
	}
	result = strings.Join(parts, "_")

	return result
}

// ExtractIDCIdentifier extracts a unique identifier from IDC startUrl.
// Examples:
//   - "https://d-1234567890.awsapps.com/start" -> "d-1234567890"
//   - "https://my-company.awsapps.com/start" -> "my-company"
//   - "https://acme-corp.awsapps.com/start" -> "acme-corp"
func ExtractIDCIdentifier(startURL string) string {
	if startURL == "" {
		return ""
	}

	// Remove protocol prefix
	url := strings.TrimPrefix(startURL, "https://")
	url = strings.TrimPrefix(url, "http://")

	// Extract subdomain (first part before the first dot)
	// Format: {identifier}.awsapps.com/start
	parts := strings.Split(url, ".")
	if len(parts) > 0 && parts[0] != "" {
		identifier := parts[0]
		// Sanitize for filename safety
		identifier = strings.ReplaceAll(identifier, "/", "_")
		identifier = strings.ReplaceAll(identifier, "\\", "_")
		identifier = strings.ReplaceAll(identifier, ":", "_")
		return identifier
	}

	return ""
}

// GenerateTokenFileName generates a unique filename for token storage.
// Priority: email > startUrl identifier (for IDC) > authMethod only
// Format: kiro-{authMethod}-{identifier}.json
func GenerateTokenFileName(tokenData *KiroTokenData) string {
	authMethod := tokenData.AuthMethod
	if authMethod == "" {
		authMethod = "unknown"
	}

	// Priority 1: Use email if available
	if tokenData.Email != "" {
		// Sanitize email for filename (replace @ and . with -)
		sanitizedEmail := tokenData.Email
		sanitizedEmail = strings.ReplaceAll(sanitizedEmail, "@", "-")
		sanitizedEmail = strings.ReplaceAll(sanitizedEmail, ".", "-")
		return fmt.Sprintf("kiro-%s-%s.json", authMethod, sanitizedEmail)
	}

	// Priority 2: For IDC, use startUrl identifier
	if authMethod == "idc" && tokenData.StartURL != "" {
		identifier := ExtractIDCIdentifier(tokenData.StartURL)
		if identifier != "" {
			return fmt.Sprintf("kiro-%s-%s.json", authMethod, identifier)
		}
	}

	// Priority 3: Fallback to authMethod only
	return fmt.Sprintf("kiro-%s.json", authMethod)
}
