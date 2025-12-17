// Package copilot provides authentication and token management for GitHub Copilot API.
// It handles the OAuth2 device flow for secure authentication with the Copilot API.
package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// copilotAPITokenURL is the endpoint for getting Copilot API tokens from GitHub token.
	copilotAPITokenURL = "https://api.github.com/copilot_internal/v2/token"
	// copilotAPIEndpoint is the base URL for making API requests.
	copilotAPIEndpoint = "https://api.githubcopilot.com"

	// Common HTTP header values for Copilot API requests.
	copilotUserAgent       = "GithubCopilot/1.0"
	copilotEditorVersion   = "vscode/1.100.0"
	copilotPluginVersion   = "copilot/1.300.0"
	copilotIntegrationID   = "vscode-chat"
	copilotOpenAIIntent    = "conversation-panel"
)

// CopilotAPIToken represents the Copilot API token response.
type CopilotAPIToken struct {
	// Token is the JWT token for authenticating with the Copilot API.
	Token string `json:"token"`
	// ExpiresAt is the Unix timestamp when the token expires.
	ExpiresAt int64 `json:"expires_at"`
	// Endpoints contains the available API endpoints.
	Endpoints struct {
		API           string `json:"api"`
		Proxy         string `json:"proxy"`
		OriginTracker string `json:"origin-tracker"`
		Telemetry     string `json:"telemetry"`
	} `json:"endpoints,omitempty"`
	// ErrorDetails contains error information if the request failed.
	ErrorDetails *struct {
		URL              string `json:"url"`
		Message          string `json:"message"`
		DocumentationURL string `json:"documentation_url"`
	} `json:"error_details,omitempty"`
}

// CopilotAuth handles GitHub Copilot authentication flow.
// It provides methods for device flow authentication and token management.
type CopilotAuth struct {
	httpClient   *http.Client
	deviceClient *DeviceFlowClient
	cfg          *config.Config
}

// NewCopilotAuth creates a new CopilotAuth service instance.
// It initializes an HTTP client with proxy settings from the provided configuration.
func NewCopilotAuth(cfg *config.Config) *CopilotAuth {
	return &CopilotAuth{
		httpClient:   util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second}),
		deviceClient: NewDeviceFlowClient(cfg),
		cfg:          cfg,
	}
}

// StartDeviceFlow initiates the device flow authentication.
// Returns the device code response containing the user code and verification URI.
func (c *CopilotAuth) StartDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {
	return c.deviceClient.RequestDeviceCode(ctx)
}

// WaitForAuthorization polls for user authorization and returns the auth bundle.
func (c *CopilotAuth) WaitForAuthorization(ctx context.Context, deviceCode *DeviceCodeResponse) (*CopilotAuthBundle, error) {
	tokenData, err := c.deviceClient.PollForToken(ctx, deviceCode)
	if err != nil {
		return nil, err
	}

	// Fetch the GitHub username
	username, err := c.deviceClient.FetchUserInfo(ctx, tokenData.AccessToken)
	if err != nil {
		log.Warnf("copilot: failed to fetch user info: %v", err)
		username = "unknown"
	}

	return &CopilotAuthBundle{
		TokenData: tokenData,
		Username:  username,
	}, nil
}

// GetCopilotAPIToken exchanges a GitHub access token for a Copilot API token.
// This token is used to make authenticated requests to the Copilot API.
func (c *CopilotAuth) GetCopilotAPIToken(ctx context.Context, githubAccessToken string) (*CopilotAPIToken, error) {
	if githubAccessToken == "" {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, fmt.Errorf("github access token is empty"))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, copilotAPITokenURL, nil)
	if err != nil {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, err)
	}

	req.Header.Set("Authorization", "token "+githubAccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", copilotUserAgent)
	req.Header.Set("Editor-Version", copilotEditorVersion)
	req.Header.Set("Editor-Plugin-Version", copilotPluginVersion)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("copilot api token: close body error: %v", errClose)
		}
	}()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, err)
	}

	if !isHTTPSuccess(resp.StatusCode) {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed,
			fmt.Errorf("status %d: %s", resp.StatusCode, string(bodyBytes)))
	}

	var apiToken CopilotAPIToken
	if err = json.Unmarshal(bodyBytes, &apiToken); err != nil {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, err)
	}

	if apiToken.Token == "" {
		return nil, NewAuthenticationError(ErrTokenExchangeFailed, fmt.Errorf("empty copilot api token"))
	}

	return &apiToken, nil
}

// ValidateToken checks if a GitHub access token is valid by attempting to fetch user info.
func (c *CopilotAuth) ValidateToken(ctx context.Context, accessToken string) (bool, string, error) {
	if accessToken == "" {
		return false, "", nil
	}

	username, err := c.deviceClient.FetchUserInfo(ctx, accessToken)
	if err != nil {
		return false, "", err
	}

	return true, username, nil
}

// CreateTokenStorage creates a new CopilotTokenStorage from auth bundle.
func (c *CopilotAuth) CreateTokenStorage(bundle *CopilotAuthBundle) *CopilotTokenStorage {
	return &CopilotTokenStorage{
		AccessToken: bundle.TokenData.AccessToken,
		TokenType:   bundle.TokenData.TokenType,
		Scope:       bundle.TokenData.Scope,
		Username:    bundle.Username,
		Type:        "github-copilot",
	}
}

// LoadAndValidateToken loads a token from storage and validates it.
// Returns the storage if valid, or an error if the token is invalid or expired.
func (c *CopilotAuth) LoadAndValidateToken(ctx context.Context, storage *CopilotTokenStorage) (bool, error) {
	if storage == nil || storage.AccessToken == "" {
		return false, fmt.Errorf("no token available")
	}

	// Check if we can still use the GitHub token to get a Copilot API token
	apiToken, err := c.GetCopilotAPIToken(ctx, storage.AccessToken)
	if err != nil {
		return false, err
	}

	// Check if the API token is expired
	if apiToken.ExpiresAt > 0 && time.Now().Unix() >= apiToken.ExpiresAt {
		return false, fmt.Errorf("copilot api token expired")
	}

	return true, nil
}

// GetAPIEndpoint returns the Copilot API endpoint URL.
func (c *CopilotAuth) GetAPIEndpoint() string {
	return copilotAPIEndpoint
}

// MakeAuthenticatedRequest creates an authenticated HTTP request to the Copilot API.
func (c *CopilotAuth) MakeAuthenticatedRequest(ctx context.Context, method, url string, body io.Reader, apiToken *CopilotAPIToken) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+apiToken.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", copilotUserAgent)
	req.Header.Set("Editor-Version", copilotEditorVersion)
	req.Header.Set("Editor-Plugin-Version", copilotPluginVersion)
	req.Header.Set("Openai-Intent", copilotOpenAIIntent)
	req.Header.Set("Copilot-Integration-Id", copilotIntegrationID)

	return req, nil
}

// buildChatCompletionURL builds the URL for chat completions API.
func buildChatCompletionURL() string {
	return copilotAPIEndpoint + "/chat/completions"
}

// isHTTPSuccess checks if the status code indicates success (2xx).
func isHTTPSuccess(statusCode int) bool {
	return statusCode >= 200 && statusCode < 300
}
