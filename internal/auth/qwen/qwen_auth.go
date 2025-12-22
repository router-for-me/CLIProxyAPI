package qwen

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/oauthflow"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/oauthhttp"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// QwenOAuthDeviceCodeEndpoint is the URL for initiating the OAuth 2.0 device authorization flow.
	QwenOAuthDeviceCodeEndpoint = "https://chat.qwen.ai/api/v1/oauth2/device/code"
	// QwenOAuthTokenEndpoint is the URL for exchanging device codes or refresh tokens for access tokens.
	QwenOAuthTokenEndpoint = "https://chat.qwen.ai/api/v1/oauth2/token"
	// QwenOAuthClientID is the client identifier for the Qwen OAuth 2.0 application.
	QwenOAuthClientID = "f0304373b74a44d2b584a3fb70ca9e56"
	// QwenOAuthScope defines the permissions requested by the application.
	QwenOAuthScope = "openid profile email model.completion"
	// QwenOAuthGrantType specifies the grant type for the device code flow.
	QwenOAuthGrantType = "urn:ietf:params:oauth:grant-type:device_code"
)

// QwenTokenData represents the OAuth credentials, including access and refresh tokens.
type QwenTokenData struct {
	AccessToken string `json:"access_token"`
	// RefreshToken is used to obtain a new access token when the current one expires.
	RefreshToken string `json:"refresh_token,omitempty"`
	// TokenType indicates the type of token, typically "Bearer".
	TokenType string `json:"token_type"`
	// ResourceURL specifies the base URL of the resource server.
	ResourceURL string `json:"resource_url,omitempty"`
	// Expire indicates the expiration date and time of the access token.
	Expire string `json:"expiry_date,omitempty"`
}

// DeviceFlow represents the response from the device authorization endpoint.
type DeviceFlow struct {
	// DeviceCode is the code that the client uses to poll for an access token.
	DeviceCode string `json:"device_code"`
	// UserCode is the code that the user enters at the verification URI.
	UserCode string `json:"user_code"`
	// VerificationURI is the URL where the user can enter the user code to authorize the device.
	VerificationURI string `json:"verification_uri"`
	// VerificationURIComplete is a URI that includes the user_code, which can be used to automatically
	// fill in the code on the verification page.
	VerificationURIComplete string `json:"verification_uri_complete"`
	// ExpiresIn is the time in seconds until the device_code and user_code expire.
	ExpiresIn int `json:"expires_in"`
	// Interval is the minimum time in seconds that the client should wait between polling requests.
	Interval int `json:"interval"`
	// CodeVerifier is the cryptographically random string used in the PKCE flow.
	CodeVerifier string `json:"code_verifier"`
}

// QwenTokenResponse represents the successful token response from the token endpoint.
type QwenTokenResponse struct {
	// AccessToken is the token used to access protected resources.
	AccessToken string `json:"access_token"`
	// RefreshToken is used to obtain a new access token.
	RefreshToken string `json:"refresh_token,omitempty"`
	// TokenType indicates the type of token, typically "Bearer".
	TokenType string `json:"token_type"`
	// ResourceURL specifies the base URL of the resource server.
	ResourceURL string `json:"resource_url,omitempty"`
	// ExpiresIn is the time in seconds until the access token expires.
	ExpiresIn int `json:"expires_in"`
}

// QwenAuth manages authentication and token handling for the Qwen API.
type QwenAuth struct {
	httpClient *http.Client
}

// NewQwenAuth creates a new QwenAuth instance with a proxy-configured HTTP client.
func NewQwenAuth(cfg *config.Config) *QwenAuth {
	return &QwenAuth{
		httpClient: util.SetOAuthProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second}),
	}
}

// generateCodeVerifier generates a cryptographically random string for the PKCE code verifier.
func (qa *QwenAuth) generateCodeVerifier() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

// generateCodeChallenge creates a SHA-256 hash of the code verifier, used as the PKCE code challenge.
func (qa *QwenAuth) generateCodeChallenge(codeVerifier string) string {
	hash := sha256.Sum256([]byte(codeVerifier))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

// generatePKCEPair creates a new code verifier and its corresponding code challenge for PKCE.
func (qa *QwenAuth) generatePKCEPair() (string, string, error) {
	codeVerifier, err := qa.generateCodeVerifier()
	if err != nil {
		return "", "", err
	}
	codeChallenge := qa.generateCodeChallenge(codeVerifier)
	return codeVerifier, codeChallenge, nil
}

// RefreshTokens exchanges a refresh token for a new access token.
func (qa *QwenAuth) RefreshTokens(ctx context.Context, refreshToken string) (*QwenTokenData, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", refreshToken)
	data.Set("client_id", QwenOAuthClientID)

	encoded := data.Encode()
	status, _, body, err := oauthhttp.Do(
		ctx,
		qa.httpClient,
		func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, QwenOAuthTokenEndpoint, strings.NewReader(encoded))
			if err != nil {
				return nil, fmt.Errorf("failed to create token request: %w", err)
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Accept", "application/json")
			return req, nil
		},
		oauthhttp.DefaultRetryConfig(),
	)
	if err != nil && status == 0 {
		return nil, fmt.Errorf("token refresh request failed: %w", err)
	}

	if status != http.StatusOK {
		var errorData map[string]interface{}
		if err = json.Unmarshal(body, &errorData); err == nil {
			return nil, fmt.Errorf("token refresh failed: %v - %v", errorData["error"], errorData["error_description"])
		}
		msg := strings.TrimSpace(string(body))
		if err != nil {
			return nil, fmt.Errorf("token refresh failed: %s: %w", msg, err)
		}
		return nil, fmt.Errorf("token refresh failed: %s", msg)
	}
	if err != nil {
		return nil, fmt.Errorf("token refresh request failed: %w", err)
	}

	var tokenData QwenTokenResponse
	if err = json.Unmarshal(body, &tokenData); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	return &QwenTokenData{
		AccessToken:  tokenData.AccessToken,
		TokenType:    tokenData.TokenType,
		RefreshToken: tokenData.RefreshToken,
		ResourceURL:  tokenData.ResourceURL,
		Expire:       time.Now().Add(time.Duration(tokenData.ExpiresIn) * time.Second).Format(time.RFC3339),
	}, nil
}

// InitiateDeviceFlow starts the OAuth 2.0 device authorization flow and returns the device flow details.
func (qa *QwenAuth) InitiateDeviceFlow(ctx context.Context) (*DeviceFlow, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Generate PKCE code verifier and challenge
	codeVerifier, codeChallenge, err := qa.generatePKCEPair()
	if err != nil {
		return nil, fmt.Errorf("failed to generate PKCE pair: %w", err)
	}

	data := url.Values{}
	data.Set("client_id", QwenOAuthClientID)
	data.Set("scope", QwenOAuthScope)
	data.Set("code_challenge", codeChallenge)
	data.Set("code_challenge_method", "S256")

	encoded := data.Encode()
	status, _, body, err := oauthhttp.Do(
		ctx,
		qa.httpClient,
		func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, QwenOAuthDeviceCodeEndpoint, strings.NewReader(encoded))
			if err != nil {
				return nil, fmt.Errorf("failed to create token request: %w", err)
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Accept", "application/json")
			return req, nil
		},
		oauthhttp.DefaultRetryConfig(),
	)
	if err != nil && status == 0 {
		return nil, fmt.Errorf("device authorization request failed: %w", err)
	}

	if status != http.StatusOK {
		msg := strings.TrimSpace(string(body))
		if err != nil {
			return nil, fmt.Errorf("device authorization failed: %d. Response: %s: %w", status, msg, err)
		}
		return nil, fmt.Errorf("device authorization failed: %d. Response: %s", status, msg)
	}
	if err != nil {
		return nil, fmt.Errorf("device authorization request failed: %w", err)
	}

	var result DeviceFlow
	if err = json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse device flow response: %w", err)
	}

	// Check if the response indicates success
	if result.DeviceCode == "" {
		return nil, fmt.Errorf("device authorization failed: device_code not found in response")
	}

	// Add the code_verifier to the result so it can be used later for polling
	result.CodeVerifier = codeVerifier

	return &result, nil
}

// PollForToken polls the token endpoint with the device code to obtain an access token.
func (qa *QwenAuth) PollForToken(ctx context.Context, deviceFlow *DeviceFlow) (*QwenTokenData, error) {
	if deviceFlow == nil {
		return nil, fmt.Errorf("device flow is nil")
	}
	deviceCode := strings.TrimSpace(deviceFlow.DeviceCode)
	if deviceCode == "" {
		return nil, fmt.Errorf("device code is empty")
	}
	codeVerifier := strings.TrimSpace(deviceFlow.CodeVerifier)
	if codeVerifier == "" {
		return nil, fmt.Errorf("code verifier is empty")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	device := &oauthflow.DeviceCodeResult{
		DeviceCode:   deviceCode,
		ExpiresIn:    deviceFlow.ExpiresIn,
		Interval:     deviceFlow.Interval,
		CodeVerifier: codeVerifier,
	}

	token, err := oauthflow.PollDeviceToken(ctx, device, func(pollCtx context.Context) (*oauthflow.TokenResult, error) {
		data := url.Values{}
		data.Set("grant_type", QwenOAuthGrantType)
		data.Set("client_id", QwenOAuthClientID)
		data.Set("device_code", deviceCode)
		data.Set("code_verifier", codeVerifier)

		encoded := data.Encode()
		status, _, body, err := oauthhttp.Do(
			pollCtx,
			qa.httpClient,
			func() (*http.Request, error) {
				req, err := http.NewRequestWithContext(pollCtx, http.MethodPost, QwenOAuthTokenEndpoint, strings.NewReader(encoded))
				if err != nil {
					return nil, err
				}
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.Header.Set("Accept", "application/json")
				return req, nil
			},
			oauthhttp.DefaultRetryConfig(),
		)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", oauthflow.ErrTransient, err)
		}

		if status == http.StatusOK {
			var response QwenTokenResponse
			if err = json.Unmarshal(body, &response); err != nil {
				return nil, fmt.Errorf("failed to parse token response: %w", err)
			}
			meta := map[string]any{}
			if strings.TrimSpace(response.ResourceURL) != "" {
				meta["resource_url"] = response.ResourceURL
			}
			tokenType := strings.TrimSpace(response.TokenType)
			if tokenType == "" {
				tokenType = "Bearer"
			}
			return &oauthflow.TokenResult{
				AccessToken:  response.AccessToken,
				RefreshToken: response.RefreshToken,
				TokenType:    tokenType,
				ExpiresAt:    time.Now().Add(time.Duration(response.ExpiresIn) * time.Second).Format(time.RFC3339),
				Metadata:     meta,
			}, nil
		}

		// Parse the response as JSON to check for OAuth RFC 8628 standard errors.
		var errorData struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		if err = json.Unmarshal(body, &errorData); err == nil {
			if status == http.StatusBadRequest {
				switch strings.TrimSpace(errorData.Error) {
				case "authorization_pending":
					return nil, oauthflow.ErrAuthorizationPending
				case "slow_down":
					return nil, oauthflow.ErrSlowDown
				case "expired_token":
					return nil, oauthflow.ErrDeviceCodeExpired
				case "access_denied":
					return nil, oauthflow.ErrAccessDenied
				}
			}
			if strings.TrimSpace(errorData.Error) != "" {
				return nil, fmt.Errorf("device token poll failed: %s - %s", errorData.Error, errorData.ErrorDescription)
			}
		}

		trimmed := strings.TrimSpace(string(body))
		if status == http.StatusTooManyRequests || status >= http.StatusInternalServerError {
			return nil, fmt.Errorf("%w: status %d: %s", oauthflow.ErrTransient, status, trimmed)
		}
		return nil, fmt.Errorf("device token poll failed: status %d: %s", status, trimmed)
	})
	if err != nil {
		return nil, err
	}
	if token == nil {
		return nil, fmt.Errorf("token result is nil")
	}

	tokenData := &QwenTokenData{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		TokenType:    token.TokenType,
		Expire:       token.ExpiresAt,
	}
	if token.Metadata != nil {
		if raw, ok := token.Metadata["resource_url"]; ok {
			if val, okStr := raw.(string); okStr {
				tokenData.ResourceURL = strings.TrimSpace(val)
			}
		}
	}

	return tokenData, nil
}

// RefreshTokensWithRetry attempts to refresh tokens with a specified number of retries upon failure.
func (o *QwenAuth) RefreshTokensWithRetry(ctx context.Context, refreshToken string, maxRetries int) (*QwenTokenData, error) {
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

// CreateTokenStorage creates a QwenTokenStorage object from a QwenTokenData object.
func (o *QwenAuth) CreateTokenStorage(tokenData *QwenTokenData) *QwenTokenStorage {
	storage := &QwenTokenStorage{
		AccessToken:  tokenData.AccessToken,
		RefreshToken: tokenData.RefreshToken,
		LastRefresh:  time.Now().Format(time.RFC3339),
		ResourceURL:  tokenData.ResourceURL,
		Expire:       tokenData.Expire,
	}

	return storage
}

// UpdateTokenStorage updates an existing token storage with new token data
func (o *QwenAuth) UpdateTokenStorage(storage *QwenTokenStorage, tokenData *QwenTokenData) {
	storage.AccessToken = tokenData.AccessToken
	storage.RefreshToken = tokenData.RefreshToken
	storage.LastRefresh = time.Now().Format(time.RFC3339)
	storage.ResourceURL = tokenData.ResourceURL
	storage.Expire = tokenData.Expire
}
