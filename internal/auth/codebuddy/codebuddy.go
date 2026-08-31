// Package codebuddy provides authentication and token management for the
// Tencent CodeBuddy (WorkBuddy) copilot service. The upstream exposes an
// OpenAI-compatible chat completions endpoint and a state-based plugin auth
// flow (state generation, browser authorization, state-to-token exchange,
// refresh token rotation).
package codebuddy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// DefaultBaseURL is the default upstream base URL of the CodeBuddy copilot API.
	DefaultBaseURL = "https://copilot.tencent.com"
	// DefaultDomain is the X-Domain header value used by login-state endpoints.
	DefaultDomain = "www.codebuddy.cn"
	// PlatformDomain is the X-Domain header value used by plugin and inference endpoints.
	PlatformDomain = "copilot.tencent.com"

	// PluginAuthStatePath is the endpoint path for generating an auth state.
	PluginAuthStatePath = "/v2/plugin/auth/state"
	// PluginAuthTokenPath is the endpoint path for exchanging a state for tokens.
	PluginAuthTokenPath = "/v2/plugin/auth/token"
	// PluginAuthTokenRefreshPath is the endpoint path for refreshing tokens.
	PluginAuthTokenRefreshPath = "/v2/plugin/auth/token/refresh"
	// LoginAccountPath is the endpoint path for account information.
	LoginAccountPath = "/v2/plugin/login/account"
	// ConfigPath is the endpoint path for the user config (available models).
	ConfigPath = "/v3/config"

	// ChatCompletionsPath is the OpenAI-compatible chat completions endpoint path.
	ChatCompletionsPath = "/v2/chat/completions"

	// AuthSourcePlugin is the X-Auth-Refresh-Source header value required by the refresh endpoint.
	AuthSourcePlugin = "plugin"
	// PlatformWorkBuddy is the platform query parameter used when generating an auth state.
	PlatformWorkBuddy = "workbuddy"

	// ClientUserAgent is the User-Agent expected by the CodeBuddy upstream.
	ClientUserAgent = "WorkBuddy/5.2.5 WorkBuddy/5.2.5 CLI/2.106.4"

	// defaultAccessTokenTTL is used when the upstream omits expiresIn.
	defaultAccessTokenTTL = 24 * time.Hour
	// pollInterval is the delay between token exchange attempts while waiting for authorization.
	pollInterval = 3 * time.Second
	// maxPollDuration bounds the total wait for user authorization.
	maxPollDuration = 15 * time.Minute
)

// TokenResponse represents the token payload returned by the token and refresh
// endpoints. CodeBuddy returns camelCase JSON fields.
type TokenResponse struct {
	AccessToken      string `json:"accessToken,omitempty"`
	ExpiresIn        int64  `json:"expiresIn,omitempty"`
	RefreshExpiresIn int64  `json:"refreshExpiresIn,omitempty"`
	RefreshToken     string `json:"refreshToken,omitempty"`
	TokenType        string `json:"tokenType,omitempty"`
	SessionState     string `json:"sessionState,omitempty"`
	// Domain is the user domain identifier returned by the token endpoint.
	Domain string `json:"domain,omitempty"`
	// Scope is the authorization scope returned by the token endpoint.
	Scope string `json:"scope,omitempty"`
}

// StateResult is the data payload of the auth state response.
type StateResult struct {
	State   string `json:"state"`
	AuthURL string `json:"authUrl"`
	Domain  string `json:"domain,omitempty"`
}

// AccountInfo is the account information payload (only fields the gateway needs).
type AccountInfo struct {
	UID         string `json:"uid"`
	Nickname    string `json:"nickname"`
	Type        string `json:"type"`
	UIN         string `json:"uin"`
	PhoneNumber string `json:"phoneNumber"`
}

// envelope is the unified {code, msg, data} response wrapper.
type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// BuildAuthStateURL returns the auth state generation URL.
func BuildAuthStateURL(baseURL string) string {
	return effectiveBaseURL(baseURL) + PluginAuthStatePath
}

// BuildAuthTokenURL returns the state-to-token exchange URL.
func BuildAuthTokenURL(baseURL, state string) string {
	return effectiveBaseURL(baseURL) + PluginAuthTokenPath + "?state=" + state
}

// BuildAuthTokenRefreshURL returns the token refresh URL.
func BuildAuthTokenRefreshURL(baseURL string) string {
	return effectiveBaseURL(baseURL) + PluginAuthTokenRefreshPath
}

// BuildLoginAccountURL returns the account information URL.
func BuildLoginAccountURL(baseURL, state string) string {
	return effectiveBaseURL(baseURL) + LoginAccountPath + "?state=" + state
}

// BuildConfigURL returns the user config URL.
func BuildConfigURL(baseURL string) string {
	return effectiveBaseURL(baseURL) + ConfigPath
}

// BuildChatCompletionsURL returns the chat completions URL.
func BuildChatCompletionsURL(baseURL string) string {
	return effectiveBaseURL(baseURL) + ChatCompletionsPath
}

// effectiveBaseURL normalizes a base URL override and falls back to the default.
func effectiveBaseURL(override string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(override), "/")
	if trimmed == "" {
		return DefaultBaseURL
	}
	return trimmed
}

// Client talks to the CodeBuddy auth endpoints.
type Client struct {
	httpClient *http.Client
	baseURL    string
}

// NewClient creates a CodeBuddy auth client. proxyURL, when non-empty, takes
// precedence over cfg.ProxyURL.
func NewClient(cfg *config.Config, proxyURL string) *Client {
	client := &http.Client{}
	effectiveProxyURL := strings.TrimSpace(proxyURL)
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effectiveProxyURL
	util.SetProxy(&sdkCfg, client)
	return &Client{
		httpClient: client,
		baseURL:    DefaultBaseURL,
	}
}

// decodeEnvelope reads an HTTP response and unwraps the {code, msg, data} envelope.
func (c *Client) decodeEnvelope(resp *http.Response, action string) (json.RawMessage, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("codebuddy %s: HTTP %d: %s", action, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("codebuddy %s: read response: %w", action, err)
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("codebuddy %s: invalid json: %w", action, err)
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("codebuddy %s: code=%d msg=%s", action, env.Code, env.Msg)
	}
	return env.Data, nil
}

// FetchState generates a fresh auth state and authorization URL.
func (c *Client) FetchState(ctx context.Context) (*StateResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, BuildAuthStateURL(c.baseURL)+"?platform="+PlatformWorkBuddy, strings.NewReader("{}"))
	if err != nil {
		return nil, fmt.Errorf("codebuddy: build state request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Domain", PlatformDomain)
	req.Header.Set("X-No-Authorization", "true")
	req.Header.Set("X-No-User-Id", "true")
	req.Header.Set("X-No-Enterprise-Id", "true")
	req.Header.Set("X-No-Department-Info", "true")
	req.Header.Set("X-Product", "SaaS")
	req.Header.Set("User-Agent", ClientUserAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codebuddy state request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("codebuddy state: close body error: %v", errClose)
		}
	}()
	data, err := c.decodeEnvelope(resp, "fetch state")
	if err != nil {
		return nil, err
	}
	var result StateResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("codebuddy: decode state: %w", err)
	}
	if strings.TrimSpace(result.State) == "" || strings.TrimSpace(result.AuthURL) == "" {
		return nil, fmt.Errorf("codebuddy: invalid state response")
	}
	return &result, nil
}

// FetchToken exchanges a state for tokens. A non-zero envelope code usually
// means the user has not finished browser authorization yet.
func (c *Client) FetchToken(ctx context.Context, state string) (*TokenResponse, error) {
	state = strings.TrimSpace(state)
	if state == "" {
		return nil, fmt.Errorf("codebuddy: state is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, BuildAuthTokenURL(c.baseURL, state), nil)
	if err != nil {
		return nil, fmt.Errorf("codebuddy: build token request: %w", err)
	}
	req.Header.Set("X-No-Authorization", "true")
	req.Header.Set("X-No-User-Id", "true")
	req.Header.Set("X-No-Enterprise-Id", "true")
	req.Header.Set("X-No-Department-Info", "true")
	req.Header.Set("X-Product", "SaaS")
	req.Header.Set("User-Agent", ClientUserAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codebuddy token request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("codebuddy token: close body error: %v", errClose)
		}
	}()
	data, err := c.decodeEnvelope(resp, "fetch token")
	if err != nil {
		return nil, err
	}
	var token TokenResponse
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, fmt.Errorf("codebuddy: decode token: %w", err)
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return nil, fmt.Errorf("codebuddy: empty access token in response")
	}
	return &token, nil
}

// WaitForToken polls the token endpoint until the user completes browser
// authorization or the deadline expires. This is part of credential
// acquisition, so a bounded wait is allowed here.
func (c *Client) WaitForToken(ctx context.Context, state string) (*TokenResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(maxPollDuration)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		token, err := c.FetchToken(ctx, state)
		if err == nil {
			return token, nil
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("codebuddy: context cancelled: %w", ctx.Err())
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("codebuddy: timed out waiting for authorization: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("codebuddy: context cancelled: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// GetAccountInfo returns the account information for a state that has been authorized.
func (c *Client) GetAccountInfo(ctx context.Context, accessToken, state string) (*AccountInfo, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("codebuddy: access token is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, BuildLoginAccountURL(c.baseURL, strings.TrimSpace(state)), nil)
	if err != nil {
		return nil, fmt.Errorf("codebuddy: build account request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-Domain", DefaultDomain)
	req.Header.Set("X-No-User-Id", "true")
	req.Header.Set("X-No-Enterprise-Id", "true")
	req.Header.Set("X-No-Department-Info", "true")
	req.Header.Set("User-Agent", ClientUserAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codebuddy account request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("codebuddy account: close body error: %v", errClose)
		}
	}()
	data, err := c.decodeEnvelope(resp, "get account")
	if err != nil {
		return nil, err
	}
	var info AccountInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("codebuddy: decode account: %w", err)
	}
	return &info, nil
}

// FetchConfig returns the raw data payload of the /v3/config endpoint, which
// contains the models available to the account.
func (c *Client) FetchConfig(ctx context.Context, accessToken, userID string) ([]byte, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("codebuddy: access token is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, BuildConfigURL(c.baseURL), nil)
	if err != nil {
		return nil, fmt.Errorf("codebuddy: build config request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-Domain", DefaultDomain)
	req.Header.Set("X-Product", "SaaS")
	req.Header.Set("User-Agent", ClientUserAgent)
	if userID = strings.TrimSpace(userID); userID != "" {
		req.Header.Set("X-User-Id", userID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codebuddy config request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("codebuddy config: close body error: %v", errClose)
		}
	}()
	return c.decodeEnvelope(resp, "get config")
}

// RefreshToken exchanges a refresh token for a new token pair.
func (c *Client) RefreshToken(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, fmt.Errorf("codebuddy: refresh token is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, BuildAuthTokenRefreshURL(c.baseURL), strings.NewReader("{}"))
	if err != nil {
		return nil, fmt.Errorf("codebuddy: build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Domain", DefaultDomain)
	req.Header.Set("X-Refresh-Token", refreshToken)
	req.Header.Set("X-Auth-Refresh-Source", AuthSourcePlugin)
	req.Header.Set("Authorization", "Bearer "+refreshToken)
	req.Header.Set("User-Agent", ClientUserAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codebuddy refresh request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("codebuddy refresh: close body error: %v", errClose)
		}
	}()
	data, err := c.decodeEnvelope(resp, "refresh token")
	if err != nil {
		return nil, err
	}
	var token TokenResponse
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, fmt.Errorf("codebuddy: decode refresh response: %w", err)
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return nil, fmt.Errorf("codebuddy: empty access token in refresh response")
	}
	return &token, nil
}

// ExpiresAt converts a token response into an absolute expiry timestamp.
func (t *TokenResponse) ExpiresAt() int64 {
	expiresIn := t.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = int64(defaultAccessTokenTTL.Seconds())
	}
	return time.Now().Unix() + expiresIn
}
