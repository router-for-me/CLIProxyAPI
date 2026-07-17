package copilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

const (
	DeviceCodeURL          = "https://github.com/login/device/code"
	AccessTokenURL         = "https://github.com/login/oauth/access_token"
	CopilotTokenURL        = "https://api.github.com/copilot_internal/v2/token"
	DefaultEndpointURL     = "https://api.githubcopilot.com"
	DefaultVerificationURL = "https://github.com/login/device"
	ClientID               = "Iv1.b507a08c87ecfe98"
	DefaultScope           = "read:user"
)

var refreshLead = 5 * time.Minute

type Auth struct {
	httpClient *http.Client
}

type DeviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type SessionToken struct {
	Token     string
	Endpoint  string
	ExpiresAt time.Time
}

type modelListResponse struct {
	Data      []modelListEntry `json:"data"`
	RawModels json.RawMessage  `json:"models"`
}

type modelListEntry struct {
	ID string `json:"id"`
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

type copilotTokenResponse struct {
	Token     string `json:"token"`
	Endpoint  string `json:"endpoint"`
	ExpiresAt any    `json:"expires_at"`
	ExpiresIn int64  `json:"expires_in"`
}

func NewAuth(cfg *config.Config, proxyURL string) *Auth {
	effectiveProxyURL := strings.TrimSpace(proxyURL)
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effectiveProxyURL
	return &Auth{httpClient: util.SetProxy(&sdkCfg, &http.Client{})}
}

func RefreshLead() time.Duration {
	return refreshLead
}

func DefaultRequestHeaders() map[string]string {
	return map[string]string{
		"editor-version":         "vscode/1.100.0",
		"editor-plugin-version":  "copilot-chat/0.30.0",
		"copilot-integration-id": "vscode-chat",
	}
}

func (a *Auth) StartDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {
	form := url.Values{
		"client_id": {ClientID},
		"scope":     {DefaultScope},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, DeviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("copilot: create device code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("copilot: request device code failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("copilot: read device code response failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("copilot: device code request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed DeviceCodeResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("copilot: decode device code response failed: %w", err)
	}
	if parsed.VerificationURI == "" {
		parsed.VerificationURI = DefaultVerificationURL
	}
	if parsed.Interval <= 0 {
		parsed.Interval = 5
	}
	if strings.TrimSpace(parsed.DeviceCode) == "" {
		return nil, fmt.Errorf("copilot: missing device_code in response")
	}
	return &parsed, nil
}

func (a *Auth) WaitForAuthorization(ctx context.Context, device *DeviceCodeResponse) (string, error) {
	if device == nil || strings.TrimSpace(device.DeviceCode) == "" {
		return "", fmt.Errorf("copilot: device code is required")
	}
	interval := time.Duration(device.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(device.ExpiresIn) * time.Second)
	if device.ExpiresIn <= 0 {
		deadline = time.Now().Add(15 * time.Minute)
	}

	for {
		if time.Now().After(deadline) {
			return "", fmt.Errorf("copilot: device authorization timed out")
		}
		token, pending, slowDown, err := a.pollAccessToken(ctx, strings.TrimSpace(device.DeviceCode))
		if err != nil {
			return "", err
		}
		if token != "" {
			return token, nil
		}
		waitFor := interval
		if slowDown {
			waitFor += 5 * time.Second
		}
		if !pending && !slowDown {
			waitFor = interval
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(waitFor):
		}
	}
}

func (a *Auth) pollAccessToken(ctx context.Context, deviceCode string) (token string, pending bool, slowDown bool, err error) {
	form := url.Values{
		"client_id":   {ClientID},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, AccessTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", false, false, fmt.Errorf("copilot: create access token request failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", false, false, fmt.Errorf("copilot: poll access token request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", false, false, fmt.Errorf("copilot: read access token response failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false, false, fmt.Errorf("copilot: poll access token failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed tokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", false, false, fmt.Errorf("copilot: decode access token response failed: %w", err)
	}
	if parsed.AccessToken != "" {
		return strings.TrimSpace(parsed.AccessToken), false, false, nil
	}
	switch strings.TrimSpace(parsed.Error) {
	case "authorization_pending":
		return "", true, false, nil
	case "slow_down":
		return "", true, true, nil
	case "expired_token":
		return "", false, false, fmt.Errorf("copilot: device code expired")
	case "access_denied":
		return "", false, false, fmt.Errorf("copilot: authorization denied")
	default:
		desc := strings.TrimSpace(parsed.Description)
		if desc == "" {
			desc = strings.TrimSpace(parsed.Error)
		}
		if desc == "" {
			desc = "unknown authorization error"
		}
		return "", false, false, fmt.Errorf("copilot: authorization failed: %s", desc)
	}
}

func (a *Auth) FetchSessionToken(ctx context.Context, githubAccessToken string) (*SessionToken, error) {
	githubAccessToken = strings.TrimSpace(githubAccessToken)
	if githubAccessToken == "" {
		return nil, fmt.Errorf("copilot: github access token is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL, bytes.NewReader(nil))
	if err != nil {
		return nil, fmt.Errorf("copilot: create session token request failed: %w", err)
	}
	req.Header.Set("Authorization", "token "+githubAccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "GitHubCopilotChat/0.30.0")
	for key, value := range DefaultRequestHeaders() {
		req.Header.Set(key, value)
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("copilot: request session token failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("copilot: read session token response failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("copilot: session token request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed copilotTokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("copilot: decode session token response failed: %w", err)
	}
	token := strings.TrimSpace(parsed.Token)
	if token == "" {
		return nil, fmt.Errorf("copilot: session token response missing token")
	}
	endpoint := strings.TrimSpace(parsed.Endpoint)
	if endpoint == "" {
		endpoint = DefaultEndpointURL
	}
	expiry := parseExpiry(parsed.ExpiresAt, parsed.ExpiresIn)

	return &SessionToken{
		Token:     token,
		Endpoint:  endpoint,
		ExpiresAt: expiry,
	}, nil
}

func (a *Auth) FetchAvailableModels(ctx context.Context, sessionToken, endpoint string) ([]string, error) {
	sessionToken = strings.TrimSpace(sessionToken)
	if sessionToken == "" {
		return nil, fmt.Errorf("copilot: session token is required")
	}
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		endpoint = DefaultEndpointURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("copilot: create models request failed: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+sessionToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "GitHubCopilotChat/0.30.0")
	for key, value := range DefaultRequestHeaders() {
		req.Header.Set(key, value)
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("copilot: request models failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("copilot: read models response failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("copilot: models request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed modelListResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("copilot: decode models response failed: %w", err)
	}

	seen := make(map[string]struct{}, len(parsed.Data))
	models := make([]string, 0, len(parsed.Data))
	add := func(modelID string) {
		modelID = strings.TrimSpace(modelID)
		if modelID == "" {
			return
		}
		if _, ok := seen[modelID]; ok {
			return
		}
		seen[modelID] = struct{}{}
		models = append(models, modelID)
	}
	for _, item := range parsed.Data {
		add(item.ID)
	}
	if raw := bytes.TrimSpace(parsed.RawModels); len(raw) > 0 {
		var modelList []modelListEntry
		if errArray := json.Unmarshal(raw, &modelList); errArray == nil {
			for _, item := range modelList {
				add(item.ID)
			}
		} else {
			var modelMap map[string]json.RawMessage
			if errMap := json.Unmarshal(raw, &modelMap); errMap == nil {
				for key := range modelMap {
					add(key)
				}
			}
		}
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("copilot: models response missing model identifiers")
	}
	return models, nil
}

func parseExpiry(value any, expiresIn int64) time.Time {
	now := time.Now().UTC()
	switch v := value.(type) {
	case float64:
		if v > 0 {
			return time.Unix(int64(v), 0).UTC()
		}
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed != "" {
			if unix, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
				return time.Unix(unix, 0).UTC()
			}
			if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
				return parsed.UTC()
			}
			if parsed, err := time.Parse(time.RFC3339Nano, trimmed); err == nil {
				return parsed.UTC()
			}
		}
	}
	if expiresIn > 0 {
		return now.Add(time.Duration(expiresIn) * time.Second)
	}
	return now.Add(30 * time.Minute)
}
