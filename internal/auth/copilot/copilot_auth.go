// Package copilot provides OAuth2 Device Flow authentication functionality for GitHub Copilot API.
// This package implements the complete OAuth2 device flow with GitHub, followed by exchanging
// the GitHub token for a Copilot API token that expires approximately every 25 minutes.
package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

// CopilotAuth handles GitHub Copilot OAuth2 device flow authentication.
// It provides methods for the complete authentication flow: device code request,
// polling for GitHub token, and exchanging for Copilot API token.
type CopilotAuth struct {
	httpClient *http.Client
}

// NewCopilotAuth creates a new GitHub Copilot authentication service.
// It initializes the HTTP client with proxy settings from the configuration.
//
// Parameters:
//   - cfg: The application configuration containing proxy settings
//
// Returns:
//   - *CopilotAuth: A new Copilot authentication service instance
func NewCopilotAuth(cfg *config.Config) *CopilotAuth {
	return &CopilotAuth{
		httpClient: util.SetProxy(&cfg.SDKConfig, &http.Client{}),
	}
}

// InitiateDeviceFlow starts the OAuth 2.0 device authorization flow with GitHub.
// It requests a device code that the user will use to authorize the application.
//
// Parameters:
//   - ctx: The context for the request
//
// Returns:
//   - *DeviceCodeResponse: The device code and verification details
//   - error: An error if the request fails
func (ca *CopilotAuth) InitiateDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {
	data := url.Values{}
	data.Set("client_id", GitHubClientID)
	data.Set("scope", GitHubScope)

	req, err := http.NewRequestWithContext(ctx, "POST", GitHubDeviceCodeURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create device code request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := ca.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("device code request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device authorization failed: %d %s. Response: %s", resp.StatusCode, resp.Status, string(body))
	}

	var result DeviceCodeResponse
	if err = json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse device code response: %w", err)
	}

	if result.DeviceCode == "" {
		return nil, fmt.Errorf("device authorization failed: device_code not found in response")
	}

	return &result, nil
}

// PollForGitHubToken polls the GitHub token endpoint to obtain an access token.
// It follows the OAuth 2.0 device flow polling pattern with proper error handling.
//
// Parameters:
//   - deviceCode: The device code from InitiateDeviceFlow
//   - interval: The minimum polling interval in seconds
//
// Returns:
//   - string: The GitHub access token (ghu_xxx)
//   - error: An error if polling fails or times out
func (ca *CopilotAuth) PollForGitHubToken(deviceCode string, interval int) (string, error) {
	pollInterval := time.Duration(interval) * time.Second
	maxAttempts := 180 // 15 minutes max (180 * 5 seconds)

	for attempt := 0; attempt < maxAttempts; attempt++ {
		data := url.Values{}
		data.Set("client_id", GitHubClientID)
		data.Set("device_code", deviceCode)
		data.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

		resp, err := http.PostForm(GitHubAccessTokenURL, data)
		if err != nil {
			fmt.Printf("Polling attempt %d/%d failed: %v\n", attempt+1, maxAttempts, err)
			time.Sleep(pollInterval)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			fmt.Printf("Polling attempt %d/%d failed: %v\n", attempt+1, maxAttempts, err)
			time.Sleep(pollInterval)
			continue
		}

		var tokenResp GitHubTokenResponse
		if err = json.Unmarshal(body, &tokenResp); err != nil {
			fmt.Printf("Polling attempt %d/%d failed to parse response: %v\n", attempt+1, maxAttempts, err)
			time.Sleep(pollInterval)
			continue
		}

		// Check for errors
		if tokenResp.Error != "" {
			switch tokenResp.Error {
			case "authorization_pending":
				// User has not yet approved. Continue polling.
				fmt.Printf("Polling attempt %d/%d...\n\n", attempt+1, maxAttempts)
				time.Sleep(pollInterval)
				continue
			case "slow_down":
				// Client is polling too frequently.
				pollInterval = time.Duration(float64(pollInterval) * 1.5)
				if pollInterval > 10*time.Second {
					pollInterval = 10 * time.Second
				}
				fmt.Printf("Server requested to slow down, increasing poll interval to %v\n\n", pollInterval)
				time.Sleep(pollInterval)
				continue
			case "expired_token":
				return "", fmt.Errorf("device code expired. Please restart the authentication process")
			case "access_denied":
				return "", fmt.Errorf("authorization denied by user. Please restart the authentication process")
			default:
				return "", fmt.Errorf("authentication failed: %s - %s", tokenResp.Error, tokenResp.ErrorDescription)
			}
		}

		// Success - we have the GitHub token
		if tokenResp.AccessToken != "" {
			return tokenResp.AccessToken, nil
		}

		// If no token and no error, continue polling
		time.Sleep(pollInterval)
	}

	return "", fmt.Errorf("authentication timeout. Please restart the authentication process")
}

// ExchangeGitHubTokenForCopilot exchanges a GitHub access token for a Copilot API token.
// The Copilot token expires approximately every 25 minutes and must be refreshed.
//
// Parameters:
//   - ctx: The context for the request
//   - githubToken: The GitHub access token (ghu_xxx)
//
// Returns:
//   - *CopilotTokenResponse: The Copilot token and metadata
//   - error: An error if the exchange fails
func (ca *CopilotAuth) ExchangeGitHubTokenForCopilot(ctx context.Context, githubToken string) (*CopilotTokenResponse, error) {
	if githubToken == "" {
		return nil, fmt.Errorf("GitHub token is required")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", GitHubCopilotTokenURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create Copilot token request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", githubToken))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "GitHubCopilotChat/1.0")

	resp, err := ca.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Copilot token exchange request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read Copilot token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Copilot token exchange failed with status %d: %s", resp.StatusCode, string(body))
	}

	var copilotResp CopilotTokenResponse
	if err = json.Unmarshal(body, &copilotResp); err != nil {
		return nil, fmt.Errorf("failed to parse Copilot token response: %w", err)
	}

	return &copilotResp, nil
}

// RefreshCopilotToken refreshes the Copilot token using the stored GitHub token.
// This should be called when the Copilot token is close to expiring (every ~25 minutes).
//
// Parameters:
//   - ctx: The context for the request
//   - githubToken: The GitHub access token
//
// Returns:
//   - *CopilotTokenData: The refreshed Copilot token data
//   - error: An error if the refresh fails
func (ca *CopilotAuth) RefreshCopilotToken(ctx context.Context, githubToken string) (*CopilotTokenData, error) {
	copilotResp, err := ca.ExchangeGitHubTokenForCopilot(ctx, githubToken)
	if err != nil {
		return nil, err
	}

	return &CopilotTokenData{
		GitHubToken:    githubToken,
		CopilotToken:   copilotResp.Token,
		CopilotAPIBase: copilotResp.Endpoints.API,
		CopilotExpire:  time.Unix(copilotResp.ExpiresAt, 0).Format(time.RFC3339),
		SKU:            copilotResp.SKU,
	}, nil
}

// RefreshCopilotTokenWithRetry attempts to refresh the Copilot token with retry logic.
//
// Parameters:
//   - ctx: The context for the request
//   - githubToken: The GitHub access token
//   - maxRetries: The maximum number of retry attempts
//
// Returns:
//   - *CopilotTokenData: The refreshed Copilot token data
//   - error: An error if all retry attempts fail
func (ca *CopilotAuth) RefreshCopilotTokenWithRetry(ctx context.Context, githubToken string, maxRetries int) (*CopilotTokenData, error) {
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Wait before retry
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}

		tokenData, err := ca.RefreshCopilotToken(ctx, githubToken)
		if err == nil {
			return tokenData, nil
		}

		lastErr = err
		log.Warnf("Copilot token refresh attempt %d failed: %v", attempt+1, err)
	}

	return nil, fmt.Errorf("Copilot token refresh failed after %d attempts: %w", maxRetries, lastErr)
}

// CreateTokenStorage creates a new CopilotTokenStorage from token data.
//
// Parameters:
//   - tokenData: The Copilot token data
//
// Returns:
//   - *CopilotTokenStorage: A new token storage instance
func (ca *CopilotAuth) CreateTokenStorage(tokenData *CopilotTokenData) *CopilotTokenStorage {
	storage := &CopilotTokenStorage{
		GitHubToken:    tokenData.GitHubToken,
		CopilotToken:   tokenData.CopilotToken,
		CopilotAPIBase: tokenData.CopilotAPIBase,
		CopilotExpire:  tokenData.CopilotExpire,
		LastRefresh:    time.Now().Format(time.RFC3339),
		Email:          tokenData.Email,
		SKU:            tokenData.SKU,
	}

	return storage
}

// UpdateTokenStorage updates an existing token storage with new Copilot token data.
//
// Parameters:
//   - storage: The existing token storage to update
//   - tokenData: The new Copilot token data
func (ca *CopilotAuth) UpdateTokenStorage(storage *CopilotTokenStorage, tokenData *CopilotTokenData) {
	storage.CopilotToken = tokenData.CopilotToken
	storage.CopilotAPIBase = tokenData.CopilotAPIBase
	storage.CopilotExpire = tokenData.CopilotExpire
	storage.LastRefresh = time.Now().Format(time.RFC3339)
	storage.SKU = tokenData.SKU
	// Keep the GitHub token unchanged as it's long-lived
}
