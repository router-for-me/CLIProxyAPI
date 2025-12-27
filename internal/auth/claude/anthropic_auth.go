// Package claude provides OAuth2 authentication functionality for Anthropic's Claude API.
// This package implements the complete OAuth2 flow with PKCE (Proof Key for Code Exchange)
// for secure authentication with Claude API, including token exchange, refresh, and storage.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/oauthhttp"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	anthropicAuthURL   = "https://claude.ai/oauth/authorize"
	anthropicTokenURL  = "https://console.anthropic.com/v1/oauth/token"
	anthropicClientID  = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	defaultRedirectURI = "http://localhost:54545/callback"
)

// tokenResponse represents the response structure from Anthropic's OAuth token endpoint.
// It contains access token, refresh token, and associated user/organization information.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Organization struct {
		UUID string `json:"uuid"`
		Name string `json:"name"`
	} `json:"organization"`
	Account struct {
		UUID         string `json:"uuid"`
		EmailAddress string `json:"email_address"`
	} `json:"account"`
}

// ClaudeAuth handles Anthropic OAuth2 authentication flow.
// It provides methods for generating authorization URLs, exchanging codes for tokens,
// and refreshing expired tokens using PKCE for enhanced security.
type ClaudeAuth struct {
	httpClient *http.Client
}

// NewClaudeAuth creates a new Anthropic authentication service.
// It initializes the HTTP client with proxy settings from the configuration.
//
// Parameters:
//   - cfg: The application configuration containing proxy settings
//
// Returns:
//   - *ClaudeAuth: A new Claude authentication service instance
func NewClaudeAuth(cfg *config.Config) *ClaudeAuth {
	client := &http.Client{Timeout: 30 * time.Second}
	return &ClaudeAuth{
		httpClient: util.SetOAuthProxy(&cfg.SDKConfig, client),
	}
}

// GenerateAuthURL creates the OAuth authorization URL with PKCE.
// This method generates a secure authorization URL including PKCE challenge codes
// for the OAuth2 flow with Anthropic's API.
//
// Parameters:
//   - state: A random state parameter for CSRF protection
//   - pkceCodes: The PKCE codes for secure code exchange
//
// Returns:
//   - string: The complete authorization URL
//   - string: The state parameter for verification
//   - error: An error if PKCE codes are missing or URL generation fails
func (o *ClaudeAuth) GenerateAuthURL(state string, pkceCodes *PKCECodes) (string, string, error) {
	return o.GenerateAuthURLWithRedirectURI(state, pkceCodes, defaultRedirectURI)
}

func (o *ClaudeAuth) GenerateAuthURLWithRedirectURI(state string, pkceCodes *PKCECodes, redirectURI string) (string, string, error) {
	if pkceCodes == nil {
		return "", "", fmt.Errorf("PKCE codes are required")
	}
	redirectURI = strings.TrimSpace(redirectURI)
	if redirectURI == "" {
		redirectURI = defaultRedirectURI
	}

	params := url.Values{
		"code":                  {"true"},
		"client_id":             {anthropicClientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {"org:create_api_key user:profile user:inference"},
		"code_challenge":        {pkceCodes.CodeChallenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}

	authURL := fmt.Sprintf("%s?%s", anthropicAuthURL, params.Encode())
	return authURL, state, nil
}

// parseCodeAndState extracts the authorization code and state from the callback response.
// It handles the parsing of the code parameter which may contain additional fragments.
//
// Parameters:
//   - code: The raw code parameter from the OAuth callback
//
// Returns:
//   - parsedCode: The extracted authorization code
//   - parsedState: The extracted state parameter if present
func (c *ClaudeAuth) parseCodeAndState(code string) (parsedCode, parsedState string) {
	splits := strings.Split(code, "#")
	parsedCode = splits[0]
	if len(splits) > 1 {
		parsedState = splits[1]
	}
	return
}

// ExchangeCodeForTokens exchanges authorization code for access tokens.
// This method implements the OAuth2 token exchange flow using PKCE for security.
// It sends the authorization code along with PKCE verifier to get access and refresh tokens.
//
// Parameters:
//   - ctx: The context for the request
//   - code: The authorization code received from OAuth callback
//   - state: The state parameter for verification
//   - pkceCodes: The PKCE codes for secure verification
//
// Returns:
//   - *ClaudeAuthBundle: The complete authentication bundle with tokens
//   - error: An error if token exchange fails
func (o *ClaudeAuth) ExchangeCodeForTokens(ctx context.Context, code, state string, pkceCodes *PKCECodes) (*ClaudeAuthBundle, error) {
	return o.ExchangeCodeForTokensWithRedirectURI(ctx, code, state, pkceCodes, defaultRedirectURI)
}

func (o *ClaudeAuth) ExchangeCodeForTokensWithRedirectURI(ctx context.Context, code, state string, pkceCodes *PKCECodes, redirectURI string) (*ClaudeAuthBundle, error) {
	if pkceCodes == nil {
		return nil, fmt.Errorf("PKCE codes are required for token exchange")
	}
	newCode, newState := o.parseCodeAndState(code)
	redirectURI = strings.TrimSpace(redirectURI)
	if redirectURI == "" {
		redirectURI = defaultRedirectURI
	}

	// Prepare token exchange request
	reqBody := map[string]interface{}{
		"code":          newCode,
		"state":         state,
		"grant_type":    "authorization_code",
		"client_id":     anthropicClientID,
		"redirect_uri":  redirectURI,
		"code_verifier": pkceCodes.CodeVerifier,
	}

	// Include state if present
	if newState != "" {
		reqBody["state"] = newState
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	status, _, body, err := oauthhttp.Do(
		ctx,
		o.httpClient,
		func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicTokenURL, bytes.NewReader(jsonBody))
			if err != nil {
				return nil, fmt.Errorf("failed to create token request: %w", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json")
			return req, nil
		},
		oauthhttp.DefaultRetryConfig(),
	)
	if err != nil && status == 0 {
		return nil, fmt.Errorf("token exchange request failed: %w", err)
	}
	if status != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if err != nil {
			return nil, fmt.Errorf("token exchange failed with status %d: %s: %w", status, msg, err)
		}
		return nil, fmt.Errorf("token exchange failed with status %d: %s", status, msg)
	}
	if err != nil {
		return nil, fmt.Errorf("token exchange request failed: %w", err)
	}

	var tokenResp tokenResponse
	if err = json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	// Create token data
	tokenData := ClaudeTokenData{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		Email:        tokenResp.Account.EmailAddress,
		Expire:       time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339),
	}

	// Create auth bundle
	bundle := &ClaudeAuthBundle{
		TokenData:   tokenData,
		LastRefresh: time.Now().Format(time.RFC3339),
	}

	return bundle, nil
}

// RefreshTokens refreshes the access token using the refresh token.
// This method exchanges a valid refresh token for a new access token,
// extending the user's authenticated session.
//
// Parameters:
//   - ctx: The context for the request
//   - refreshToken: The refresh token to use for getting new access token
//
// Returns:
//   - *ClaudeTokenData: The new token data with updated access token
//   - error: An error if token refresh fails
func (o *ClaudeAuth) RefreshTokens(ctx context.Context, refreshToken string) (*ClaudeTokenData, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is required")
	}

	reqBody := map[string]interface{}{
		"client_id":     anthropicClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	status, _, body, err := oauthhttp.Do(
		ctx,
		o.httpClient,
		func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicTokenURL, bytes.NewReader(jsonBody))
			if err != nil {
				return nil, fmt.Errorf("failed to create refresh request: %w", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json")
			return req, nil
		},
		oauthhttp.DefaultRetryConfig(),
	)
	if err != nil && status == 0 {
		return nil, fmt.Errorf("token refresh request failed: %w", err)
	}
	if status != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if err != nil {
			return nil, fmt.Errorf("token refresh failed with status %d: %s: %w", status, msg, err)
		}
		return nil, fmt.Errorf("token refresh failed with status %d: %s", status, msg)
	}
	if err != nil {
		return nil, fmt.Errorf("token refresh request failed: %w", err)
	}

	var tokenResp tokenResponse
	if err = json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	// Create token data
	return &ClaudeTokenData{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		Email:        tokenResp.Account.EmailAddress,
		Expire:       time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339),
	}, nil
}

// CreateTokenStorage creates a new ClaudeTokenStorage from auth bundle and user info.
// This method converts the authentication bundle into a token storage structure
// suitable for persistence and later use.
//
// Parameters:
//   - bundle: The authentication bundle containing token data
//
// Returns:
//   - *ClaudeTokenStorage: A new token storage instance
func (o *ClaudeAuth) CreateTokenStorage(bundle *ClaudeAuthBundle) *ClaudeTokenStorage {
	storage := &ClaudeTokenStorage{
		AccessToken:  bundle.TokenData.AccessToken,
		RefreshToken: bundle.TokenData.RefreshToken,
		LastRefresh:  bundle.LastRefresh,
		Email:        bundle.TokenData.Email,
		Expire:       bundle.TokenData.Expire,
	}

	return storage
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
//   - *ClaudeTokenData: The refreshed token data
//   - error: An error if all retry attempts fail
func (o *ClaudeAuth) RefreshTokensWithRetry(ctx context.Context, refreshToken string, maxRetries int) (*ClaudeTokenData, error) {
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

		tokenData, err := o.RefreshTokens(ctx, refreshToken)
		if err == nil {
			return tokenData, nil
		}

		lastErr = err
		log.Warnf("Token refresh attempt %d failed: %v", attempt+1, err)
	}

	return nil, fmt.Errorf("token refresh failed after %d attempts: %w", maxRetries, lastErr)
}

// UpdateTokenStorage updates an existing token storage with new token data.
// This method refreshes the token storage with newly obtained access and refresh tokens,
// updating timestamps and expiration information.
//
// Parameters:
//   - storage: The existing token storage to update
//   - tokenData: The new token data to apply
func (o *ClaudeAuth) UpdateTokenStorage(storage *ClaudeTokenStorage, tokenData *ClaudeTokenData) {
	storage.AccessToken = tokenData.AccessToken
	storage.RefreshToken = tokenData.RefreshToken
	storage.LastRefresh = time.Now().Format(time.RFC3339)
	storage.Email = tokenData.Email
	storage.Expire = tokenData.Expire
}
