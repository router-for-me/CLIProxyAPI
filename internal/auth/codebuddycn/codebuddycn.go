// Package codebuddycn implements the CodeBuddy CN browser authorization polling flow.
package codebuddycn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/singleflight"
)

const (
	APIBaseURL        = "https://copilot.tencent.com/v2"
	StateURL          = "https://copilot.tencent.com/v2/plugin/auth/state"
	TokenURL          = "https://copilot.tencent.com/v2/plugin/auth/token"
	RefreshURL        = "https://copilot.tencent.com/v2/plugin/auth/token/refresh"
	UserAgent         = "CLI/2.63.2 CodeBuddy/2.63.2"
	defaultPollPeriod = 5 * time.Second
	maxPollDuration   = 15 * time.Minute
)

var refreshGroup singleflight.Group

// DeviceCode contains the state and browser URL returned by CodeBuddy.
type DeviceCode struct {
	State     string
	AuthURL   string
	Interval  time.Duration
	ExpiresIn int
}

// TokenData contains CodeBuddy access and refresh tokens.
type TokenData struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	ExpiresIn    int
	ExpiresAt    time.Time
}

// Client performs CodeBuddy credential-acquisition requests.
type Client struct {
	httpClient *http.Client
	stateURL   string
	tokenURL   string
	refreshURL string
}

// NewClient creates a proxy-aware CodeBuddy OAuth client.
func NewClient(cfg *config.Config) *Client {
	return NewClientWithProxyURL(cfg, "")
}

// NewClientWithProxyURL creates a client with an optional per-auth proxy override.
func NewClientWithProxyURL(cfg *config.Config, proxyURL string) *Client {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
	}
	if strings.TrimSpace(proxyURL) != "" {
		sdkCfg.ProxyURL = strings.TrimSpace(proxyURL)
	}
	httpClient = util.SetProxy(&sdkCfg, httpClient)
	return newClient(httpClient, StateURL, TokenURL, RefreshURL)
}

func newClient(httpClient *http.Client, stateURL, tokenURL, refreshURL string) *Client {
	return &Client{httpClient: httpClient, stateURL: stateURL, tokenURL: tokenURL, refreshURL: refreshURL}
}

// StartDeviceFlow requests the browser authorization URL.
func (c *Client) StartDeviceFlow(ctx context.Context) (*DeviceCode, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	endpoint, err := url.Parse(c.stateURL)
	if err != nil {
		return nil, fmt.Errorf("codebuddy-cn: parse state URL: %w", err)
	}
	query := endpoint.Query()
	query.Set("platform", "CLI")
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewBufferString("{}"))
	if err != nil {
		return nil, fmt.Errorf("codebuddy-cn: create state request: %w", err)
	}
	applyHeaders(req, false)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codebuddy-cn: state request failed: %w", err)
	}
	defer closeBody("state", resp.Body)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("codebuddy-cn: read state response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("codebuddy-cn: state request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			State   string `json:"state"`
			AuthURL string `json:"authUrl"`
		} `json:"data"`
	}
	if err = json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("codebuddy-cn: parse state response: %w", err)
	}
	if payload.Code != 0 || strings.TrimSpace(payload.Data.State) == "" || strings.TrimSpace(payload.Data.AuthURL) == "" {
		return nil, fmt.Errorf("codebuddy-cn: state request rejected (code %d): %s", payload.Code, strings.TrimSpace(payload.Msg))
	}
	return &DeviceCode{State: payload.Data.State, AuthURL: payload.Data.AuthURL, Interval: defaultPollPeriod, ExpiresIn: int(maxPollDuration / time.Second)}, nil
}

// WaitForAuthorization polls until the browser authorization completes.
func (c *Client) WaitForAuthorization(ctx context.Context, device *DeviceCode) (*TokenData, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if device == nil || strings.TrimSpace(device.State) == "" {
		return nil, fmt.Errorf("codebuddy-cn: authorization state is required")
	}
	interval := device.Interval
	if interval <= 0 {
		interval = defaultPollPeriod
	}
	deadline := time.Now().Add(maxPollDuration)
	if device.ExpiresIn > 0 {
		deadline = time.Now().Add(time.Duration(device.ExpiresIn) * time.Second)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("codebuddy-cn: authorization cancelled: %w", ctx.Err())
		case <-ticker.C:
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("codebuddy-cn: authorization expired")
			}
			token, pending, errPoll := c.pollToken(ctx, device.State)
			if errPoll != nil {
				return nil, errPoll
			}
			if !pending {
				return token, nil
			}
		}
	}
}

func (c *Client) pollToken(ctx context.Context, state string) (*TokenData, bool, error) {
	endpoint, err := url.Parse(c.tokenURL)
	if err != nil {
		return nil, false, fmt.Errorf("codebuddy-cn: parse token URL: %w", err)
	}
	query := endpoint.Query()
	query.Set("state", state)
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, false, fmt.Errorf("codebuddy-cn: create token request: %w", err)
	}
	applyHeaders(req, true)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("codebuddy-cn: token request failed: %w", err)
	}
	defer closeBody("token", resp.Body)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("codebuddy-cn: read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("codebuddy-cn: token request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	payload, err := parseTokenResponse(body)
	if err != nil {
		var statusError *responseError
		if errors.As(err, &statusError) && statusError.Code == 11217 {
			return nil, true, nil
		}
		return nil, false, err
	}
	return payload, false, nil
}

// Refresh exchanges a refresh token for a new token pair.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (*TokenData, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, fmt.Errorf("codebuddy-cn: refresh token is required")
	}
	result, err, _ := refreshGroup.Do(refreshToken, func() (any, error) {
		return c.refresh(context.WithoutCancel(ctx), refreshToken)
	})
	if err != nil {
		return nil, err
	}
	token, ok := result.(*TokenData)
	if !ok || token == nil {
		return nil, fmt.Errorf("codebuddy-cn: invalid refresh result")
	}
	return token, nil
}

func (c *Client) refresh(ctx context.Context, refreshToken string) (*TokenData, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.refreshURL, bytes.NewBufferString("{}"))
	if err != nil {
		return nil, fmt.Errorf("codebuddy-cn: create refresh request: %w", err)
	}
	applyHeaders(req, false)
	req.Header.Set("X-Refresh-Token", refreshToken)
	req.Header.Set("X-Auth-Refresh-Source", "plugin")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codebuddy-cn: refresh request failed: %w", err)
	}
	defer closeBody("refresh", resp.Body)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("codebuddy-cn: read refresh response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("codebuddy-cn: refresh failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return parseTokenResponse(body)
}

type responseError struct {
	Code int
	Msg  string
}

func (e *responseError) Error() string {
	return fmt.Sprintf("codebuddy-cn: token response rejected (code %d): %s", e.Code, e.Msg)
}

func parseTokenResponse(body []byte) (*TokenData, error) {
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			TokenType    string `json:"tokenType"`
			ExpiresIn    int    `json:"expiresIn"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("codebuddy-cn: parse token response: %w", err)
	}
	if payload.Code != 0 {
		return nil, &responseError{Code: payload.Code, Msg: strings.TrimSpace(payload.Msg)}
	}
	if strings.TrimSpace(payload.Data.AccessToken) == "" {
		return nil, fmt.Errorf("codebuddy-cn: token response missing access token")
	}
	tokenType := strings.TrimSpace(payload.Data.TokenType)
	if tokenType == "" {
		tokenType = "Bearer"
	}
	token := &TokenData{AccessToken: payload.Data.AccessToken, RefreshToken: payload.Data.RefreshToken, TokenType: tokenType, ExpiresIn: payload.Data.ExpiresIn}
	if token.ExpiresIn > 0 {
		token.ExpiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	return token, nil
}

func applyHeaders(req *http.Request, polling bool) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-Domain", "copilot.tencent.com")
	req.Header.Set("X-No-Authorization", "true")
	req.Header.Set("X-No-User-Id", "true")
	req.Header.Set("X-Product", "SaaS")
	if req.Method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if polling {
		req.Header.Set("X-No-Enterprise-Id", "true")
		req.Header.Set("X-No-Department-Info", "true")
	}
}

func closeBody(operation string, body io.Closer) {
	if err := body.Close(); err != nil {
		log.Errorf("codebuddy-cn %s: close response body: %v", operation, err)
	}
}
