/**
 * @file Cline authentication service implementation
 * @description Implements token refresh logic for Cline API authentication. Unlike traditional
 * OAuth flows, Cline uses a simple refresh token mechanism where a long-lived refresh token
 * is exchanged for short-lived JWT access tokens (~10 minutes). The refresh token is obtained
 * from the Cline VSCode extension via the "Cline: Export Auth Token" command.
 * 
 * Authentication flow:
 * 1. User exports refresh token from Cline VSCode extension
 * 2. Refresh token is stored in config or auth file
 * 3. System exchanges refresh token for JWT access token via API
 * 4. JWT token is used with "workos:" prefix for API requests
 * 5. Token is automatically refreshed when expired
 */

package cline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// ClineAPIBaseURL is the base URL for Cline API endpoints
	ClineAPIBaseURL = "https://api.cline.bot"
	// ClineTokenRefreshEndpoint is the endpoint for refreshing access tokens
	ClineTokenRefreshEndpoint = "/api/v1/auth/refresh"
)

// ClineAuth handles Cline authentication and token refresh operations.
// It provides methods for exchanging refresh tokens for access tokens
// and managing token lifecycle.
type ClineAuth struct {
	httpClient *http.Client
	apiBaseURL string
}

// NewClineAuth creates a new Cline authentication service.
// It initializes the HTTP client with proxy settings from the configuration.
//
// Parameters:
//   - cfg: The application configuration containing proxy settings
//
// Returns:
//   - *ClineAuth: A new Cline authentication service instance
func NewClineAuth(cfg *config.Config) *ClineAuth {
	return &ClineAuth{
		httpClient: util.SetProxy(&cfg.SDKConfig, &http.Client{}),
		apiBaseURL: ClineAPIBaseURL,
	}
}

// RefreshTokens exchanges a refresh token for a new access token.
// This method calls the Cline API to obtain a fresh JWT access token
// using the provided refresh token.
//
// Parameters:
//   - ctx: The context for the request
//   - refreshToken: The refresh token to use for getting new access token
//
// Returns:
//   - *ClineTokenData: The new token data with updated access token
//   - error: An error if token refresh fails
func (c *ClineAuth) RefreshTokens(ctx context.Context, refreshToken string) (*ClineTokenData, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is required")
	}

	reqBody := map[string]interface{}{
		"refreshToken": refreshToken,
		"grantType":    "refresh_token",
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	endpoint := c.apiBaseURL + ClineTokenRefreshEndpoint
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(string(jsonBody)))
	if err != nil {
		return nil, fmt.Errorf("failed to create refresh request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token refresh request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token refresh failed with status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp ClineTokenRefreshResponse
	if err = json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	if !tokenResp.Success {
		return nil, fmt.Errorf("token refresh failed: API returned success=false")
	}

	// Parse expiration time
	expiresAt, err := time.Parse(time.RFC3339, tokenResp.Data.ExpiresAt)
	if err != nil {
		// Fallback: assume 10 minutes from now if parsing fails
		expiresAt = time.Now().Add(10 * time.Minute)
		log.Warnf("Failed to parse Cline token expiration time, using default: %v", err)
	}

	// Use the new refresh token if provided, otherwise keep the old one
	newRefreshToken := refreshToken
	if tokenResp.Data.RefreshToken != "" {
		newRefreshToken = tokenResp.Data.RefreshToken
	}

	return &ClineTokenData{
		AccessToken:  tokenResp.Data.AccessToken,
		RefreshToken: newRefreshToken,
		Email:        tokenResp.Data.UserInfo.Email,
		Expire:       expiresAt.Format(time.RFC3339),
	}, nil
}

// RefreshTokensWithRetry refreshes tokens with automatic retry logic.
// This method implements exponential backoff retry logic for token refresh operations,
// providing resilience against temporary network or service issues.
//
// Parameters:
//   - ctx: The context for the request
//   - refreshToken: The refresh token to use
//   - maxRetries: The maximum number of retry attempts
//
// Returns:
//   - *ClineTokenData: The refreshed token data
//   - error: An error if all retry attempts fail
func (c *ClineAuth) RefreshTokensWithRetry(ctx context.Context, refreshToken string, maxRetries int) (*ClineTokenData, error) {
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Wait before retry with exponential backoff
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}

		tokenData, err := c.RefreshTokens(ctx, refreshToken)
		if err == nil {
			return tokenData, nil
		}

		lastErr = err
		log.Warnf("Cline token refresh attempt %d failed: %v", attempt+1, err)
	}

	return nil, fmt.Errorf("token refresh failed after %d attempts: %w", maxRetries, lastErr)
}

// CreateTokenStorage creates a new ClineTokenStorage from token data.
// This method converts the token data into a storage structure
// suitable for persistence and later use.
//
// Parameters:
//   - tokenData: The token data to store
//
// Returns:
//   - *ClineTokenStorage: A new token storage instance
func (c *ClineAuth) CreateTokenStorage(tokenData *ClineTokenData) *ClineTokenStorage {
	return &ClineTokenStorage{
		AccessToken:  tokenData.AccessToken,
		RefreshToken: tokenData.RefreshToken,
		LastRefresh:  time.Now().Format(time.RFC3339),
		Email:        tokenData.Email,
		Expire:       tokenData.Expire,
	}
}

// UpdateTokenStorage updates an existing token storage with new token data.
// This method refreshes the token storage with newly obtained access and refresh tokens,
// updating timestamps and expiration information.
//
// Parameters:
//   - storage: The existing token storage to update
//   - tokenData: The new token data to apply
func (c *ClineAuth) UpdateTokenStorage(storage *ClineTokenStorage, tokenData *ClineTokenData) {
	storage.AccessToken = tokenData.AccessToken
	// Only update refresh token if a new one is provided, otherwise keep the existing one
	if tokenData.RefreshToken != "" {
		storage.RefreshToken = tokenData.RefreshToken
	}
	storage.LastRefresh = time.Now().Format(time.RFC3339)
	if tokenData.Email != "" {
		storage.Email = tokenData.Email
	}
	storage.Expire = tokenData.Expire
}

// ShouldRefreshToken checks if the Cline access token needs to be refreshed.
// JWT tokens from Cline API typically expire after ~10 minutes, so we refresh
// them 5 minutes before expiration to ensure continuous availability.
//
// Parameters:
//   - expireTime: The expiration timestamp in RFC3339 format
//
// Returns:
//   - bool: true if refresh is needed, false otherwise
//   - time.Duration: time until expiration
//   - error: An error if the expiration time cannot be parsed
func ShouldRefreshToken(expireTime string) (bool, time.Duration, error) {
	if strings.TrimSpace(expireTime) == "" {
		return true, 0, fmt.Errorf("cline: expire time is empty")
	}

	expire, err := time.Parse(time.RFC3339, expireTime)
	if err != nil {
		return true, 0, fmt.Errorf("cline: parse expire time failed: %w", err)
	}

	now := time.Now()
	timeUntilExpiry := expire.Sub(now)

	// Refresh 5 minutes before expiration (JWT tokens live ~10 minutes)
	const refreshLead = 5 * time.Minute
	needsRefresh := timeUntilExpiry <= refreshLead

	return needsRefresh, timeUntilExpiry, nil
}
