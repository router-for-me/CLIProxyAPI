// Package cline provides authentication and token management for the Cline AI
// account API. Login uses the WorkOS OAuth2 Device Authorization Grant:
// Cline's CLI authenticates against WorkOS first, then exchanges the WorkOS
// tokens for Cline account tokens at api.cline.bot.
package cline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/singleflight"
)

const (
	// WorkOSClientID is the WorkOS OAuth client ID used by Cline's own CLI.
	WorkOSClientID = "client_01K3A541FN8TA3EPPHTD2325AR"

	// workOSBaseURL is the WorkOS user-management API origin.
	workOSBaseURL = "https://api.workos.com"

	// DeviceCodeGrantType is the RFC 8628 device code grant type.
	DeviceCodeGrantType = "urn:ietf:params:oauth:grant-type:device_code"

	// DefaultAPIBaseURL is the fixed Cline account API base URL used to serve
	// models over OpenAI-compatible chat completions.
	DefaultAPIBaseURL = "https://api.cline.bot/api/v1"

	// defaultHTTPClientTimeout covers form/token round trips to WorkOS and the
	// Cline account API; API requests themselves run through the executor.
	defaultHTTPClientTimeout = 30 * time.Second

	// defaultPollInterval is the minimum interval between device code polls.
	defaultPollInterval = 5 * time.Second

	// MaxPollDuration is the maximum time to wait for user authorization.
	MaxPollDuration = 15 * time.Minute

	// refreshThresholdSeconds is when to refresh a token before expiry (5 minutes).
	refreshThresholdSeconds = 300
)

var clineRefreshGroup singleflight.Group

// ClineAuth performs the WorkOS device login, the Cline token exchange, and
// refresh-token rotation against the Cline account API.
type ClineAuth struct {
	httpClient      *http.Client
	workosBaseURL   string
	apiBaseURL      string
	minPollInterval time.Duration
}

// NewClineAuth creates a Cline auth helper using config proxy settings.
func NewClineAuth(cfg *config.Config) *ClineAuth {
	return NewClineAuthWithProxyURL(cfg, "")
}

// NewClineAuthWithProxyURL creates a Cline auth helper with an explicit proxy URL.
// proxyURL takes precedence over cfg.ProxyURL when non-empty.
func NewClineAuthWithProxyURL(cfg *config.Config, proxyURL string) *ClineAuth {
	return NewClineAuthWithProxyURLAndBaseURL(cfg, proxyURL, "")
}

// NewClineAuthWithProxyURLAndBaseURL creates a Cline auth helper with an
// explicit proxy URL and an API base override. An empty apiBaseURL keeps the
// public Cline account API; an override targets enterprise/staging mirrors.
func NewClineAuthWithProxyURLAndBaseURL(cfg *config.Config, proxyURL, apiBaseURL string) *ClineAuth {
	effectiveProxyURL := strings.TrimSpace(proxyURL)
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effectiveProxyURL
	resolvedBase := strings.TrimRight(strings.TrimSpace(apiBaseURL), "/")
	if resolvedBase == "" {
		resolvedBase = DefaultAPIBaseURL
	}
	return &ClineAuth{
		httpClient:    util.SetProxy(&sdkCfg, &http.Client{Timeout: defaultHTTPClientTimeout}),
		workosBaseURL: workOSBaseURL,
		apiBaseURL:    resolvedBase,
	}
}

// DeviceCodeResponse represents WorkOS's device authorization response.
type DeviceCodeResponse struct {
	// DeviceCode is the device verification code.
	DeviceCode string `json:"device_code"`
	// UserCode is the code the user must enter at the verification URI.
	UserCode string `json:"user_code"`
	// VerificationURI is the URL where the user should enter the code.
	VerificationURI string `json:"verification_uri,omitempty"`
	// VerificationURIComplete is the URL with the code pre-filled.
	VerificationURIComplete string `json:"verification_uri_complete"`
	// ExpiresIn is the number of seconds until the device code expires.
	ExpiresIn int `json:"expires_in"`
	// Interval is the minimum number of seconds to wait between polling requests.
	Interval int `json:"interval"`
}

// WorkOSTokens holds the tokens returned by the WorkOS authenticate endpoint.
type WorkOSTokens struct {
	// AccessToken is the WorkOS access token.
	AccessToken string `json:"access_token"`
	// RefreshToken is the WorkOS refresh token.
	RefreshToken string `json:"refresh_token"`
	// TokenType is the token type, typically "Bearer".
	TokenType string `json:"token_type"`
}

// ClineUserInfo carries identity fields returned by the Cline register endpoint.
type ClineUserInfo struct {
	// Subject is the WorkOS subject identifier.
	Subject string `json:"subject,omitempty"`
	// Email is the account email.
	Email string `json:"email,omitempty"`
	// Name is the account display name.
	Name string `json:"name,omitempty"`
	// ClineUserID is the Cline-internal user identifier.
	ClineUserID string `json:"clineUserId,omitempty"`
}

// ClineTokenRecord holds the Cline account tokens returned by /auth/register
// and /auth/refresh. Refresh tokens rotate on every refresh; always persist
// the new refresh token.
type ClineTokenRecord struct {
	// AccessToken is the Cline account access token, used as the Bearer token
	// for model calls.
	AccessToken string `json:"accessToken"`
	// RefreshToken is the Cline account refresh token.
	RefreshToken string `json:"refreshToken"`
	// TokenType is the token type, typically "Bearer".
	TokenType string `json:"tokenType,omitempty"`
	// ExpiresAt is the RFC3339 timestamp when the access token expires.
	ExpiresAt string `json:"expiresAt,omitempty"`
	// UserInfo describes the authenticated account.
	UserInfo *ClineUserInfo `json:"userInfo,omitempty"`
}

// ClineAuthBundle bundles the Cline token record with the refresh timestamp.
type ClineAuthBundle struct {
	// TokenRecord contains the exchanged Cline account tokens.
	TokenRecord *ClineTokenRecord
	// Models holds the per-account detected model catalog fetched right after
	// token acquisition. Empty when detection failed or the account exposes no
	// models; the presence of the metadata key (not the list) marks detection.
	Models []ClineModelInfo
	// ModelsDetected reports whether model detection succeeded (possibly with
	// an empty result for a model-less account). Distinguishes "detected-but-
	// empty" from "detection failed", so callers only persist the marker on
	// success and later lifecycle events may retry after a failure.
	ModelsDetected bool
	// LastRefresh is the RFC3339 timestamp of the exchange.
	LastRefresh string
}

func (a *ClineAuth) deviceAuthorizeURL() string {
	return strings.TrimSuffix(a.workosBaseURL, "/") + "/user_management/authorize/device"
}

func (a *ClineAuth) deviceAuthenticateURL() string {
	return strings.TrimSuffix(a.workosBaseURL, "/") + "/user_management/authenticate"
}

func (a *ClineAuth) registerURL() string {
	return strings.TrimSuffix(a.apiBaseURL, "/") + "/auth/register"
}

func (a *ClineAuth) refreshURL() string {
	return strings.TrimSuffix(a.apiBaseURL, "/") + "/auth/refresh"
}

// StartDeviceFlow initiates the WorkOS device authorization flow.
func (a *ClineAuth) StartDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	form := url.Values{"client_id": {WorkOSClientID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.deviceAuthorizeURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("cline device code: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cline device code request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("cline device code: close response body error: %v", errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cline device code: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cline device code request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var deviceCode DeviceCodeResponse
	if err = json.Unmarshal(body, &deviceCode); err != nil {
		return nil, fmt.Errorf("cline device code: parse response: %w", err)
	}
	if strings.TrimSpace(deviceCode.DeviceCode) == "" {
		return nil, fmt.Errorf("cline device code: response missing device_code")
	}
	if strings.TrimSpace(deviceCode.UserCode) == "" {
		return nil, fmt.Errorf("cline device code: response missing user_code")
	}
	if strings.TrimSpace(deviceCode.VerificationURI) == "" && strings.TrimSpace(deviceCode.VerificationURIComplete) == "" {
		return nil, fmt.Errorf("cline device code: response missing verification URI")
	}
	return &deviceCode, nil
}

// WaitForAuthorization polls for user authorization, then exchanges the WorkOS
// tokens for a Cline account token record.
func (a *ClineAuth) WaitForAuthorization(ctx context.Context, deviceCode *DeviceCodeResponse) (*ClineAuthBundle, error) {
	workosTokens, err := a.PollDeviceFlow(ctx, deviceCode)
	if err != nil {
		return nil, err
	}
	record, err := a.RegisterClineToken(ctx, workosTokens.AccessToken, workosTokens.RefreshToken)
	if err != nil {
		return nil, err
	}
	bundle := &ClineAuthBundle{
		TokenRecord: record,
		LastRefresh: time.Now().UTC().Format(time.RFC3339),
	}
	// Fetch the per-account model catalog right after token acquisition.
	// Detection failure must never fail the login; callers save tokens anyway.
	models, errModels := a.FetchAvailableModels(ctx, record.AccessToken)
	if errModels != nil {
		log.Warnf("cline: account model detection failed, saving credential without models: %v", errModels)
	} else {
		bundle.Models = models
		bundle.ModelsDetected = true
	}
	return bundle, nil
}

// PollDeviceFlow polls the WorkOS authenticate endpoint until the user
// approves the device code or it expires.
func (a *ClineAuth) PollDeviceFlow(ctx context.Context, deviceCode *DeviceCodeResponse) (*WorkOSTokens, error) {
	if deviceCode == nil {
		return nil, fmt.Errorf("cline device code: response is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	minInterval := defaultPollInterval
	if a != nil && a.minPollInterval > 0 {
		minInterval = a.minPollInterval
	}
	interval := time.Duration(deviceCode.Interval) * time.Second
	if interval < minInterval {
		interval = minInterval
	}

	deadline := time.Now().Add(MaxPollDuration)
	if deviceCode.ExpiresIn > 0 {
		codeDeadline := time.Now().Add(time.Duration(deviceCode.ExpiresIn) * time.Second)
		if codeDeadline.Before(deadline) {
			deadline = codeDeadline
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("cline device code: context cancelled: %w", ctx.Err())
		case <-ticker.C:
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("cline device code expired")
			}
			tokens, pollErr, shouldContinue := a.exchangeDeviceCode(ctx, deviceCode.DeviceCode)
			if tokens != nil {
				return tokens, nil
			}
			if !shouldContinue {
				return nil, pollErr
			}
			// Continue polling.
		}
	}
}

// exchangeDeviceCode attempts to exchange the device code for WorkOS tokens.
// Returns (tokens, error, shouldContinue).
func (a *ClineAuth) exchangeDeviceCode(ctx context.Context, deviceCode string) (*WorkOSTokens, error, bool) {
	form := url.Values{
		"client_id":   {WorkOSClientID},
		"device_code": {strings.TrimSpace(deviceCode)},
		"grant_type":  {DeviceCodeGrantType},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.deviceAuthenticateURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("cline device token: create request: %w", err), false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cline device token request failed: %w", err), false
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("cline device token: close response body error: %v", errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cline device token: read response: %w", err), false
	}

	var payload struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		TokenType        string `json:"token_type"`
	}
	if err = json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("cline device token: parse response: %w", err), false
	}

	if payload.Error != "" {
		switch payload.Error {
		case "authorization_pending", "slow_down":
			return nil, nil, true
		case "expired_token":
			return nil, fmt.Errorf("cline device code expired"), false
		case "access_denied":
			return nil, fmt.Errorf("cline device authorization denied"), false
		default:
			desc := strings.TrimSpace(payload.ErrorDescription)
			if desc != "" {
				return nil, fmt.Errorf("cline device token error: %s: %s", payload.Error, desc), false
			}
			return nil, fmt.Errorf("cline device token error: %s", payload.Error), false
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cline device token request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body))), false
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return nil, fmt.Errorf("cline device token response missing access_token"), false
	}

	return &WorkOSTokens{
		AccessToken:  strings.TrimSpace(payload.AccessToken),
		RefreshToken: strings.TrimSpace(payload.RefreshToken),
		TokenType:    strings.TrimSpace(payload.TokenType),
	}, nil, false
}

// RegisterClineToken exchanges WorkOS tokens for Cline account tokens.
func (a *ClineAuth) RegisterClineToken(ctx context.Context, workosAccessToken, workosRefreshToken string) (*ClineTokenRecord, error) {
	if strings.TrimSpace(workosAccessToken) == "" {
		return nil, fmt.Errorf("cline register: access token is required")
	}
	requestBody, errMarshal := json.Marshal(map[string]string{
		"accessToken":  workosAccessToken,
		"refreshToken": workosRefreshToken,
	})
	if errMarshal != nil {
		return nil, fmt.Errorf("cline register: marshal request: %w", errMarshal)
	}
	return a.postTokenJSON(ctx, a.registerURL(), requestBody, "cline register")
}

// RefreshClineToken rotates Cline account tokens. The refresh token rotates on
// every call; callers must persist the returned refresh token.
func (a *ClineAuth) RefreshClineToken(ctx context.Context, refreshToken string) (*ClineTokenRecord, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, fmt.Errorf("cline token refresh: refresh token is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	refreshToken = strings.TrimSpace(refreshToken)
	flightKey := a.refreshURL() + ":" + refreshToken

	result, err, _ := clineRefreshGroup.Do(flightKey, func() (interface{}, error) {
		return a.refreshTokenSingleFlight(context.WithoutCancel(ctx), refreshToken)
	})
	if err != nil {
		return nil, err
	}
	record, ok := result.(*ClineTokenRecord)
	if !ok || record == nil {
		return nil, fmt.Errorf("cline token refresh failed: invalid single-flight result")
	}
	return record, nil
}

func (a *ClineAuth) refreshTokenSingleFlight(ctx context.Context, refreshToken string) (*ClineTokenRecord, error) {
	requestBody, errMarshal := json.Marshal(map[string]string{
		"refreshToken": refreshToken,
		"grantType":    "refresh_token",
	})
	if errMarshal != nil {
		return nil, fmt.Errorf("cline token refresh: marshal request: %w", errMarshal)
	}
	return a.postTokenJSON(ctx, a.refreshURL(), requestBody, "cline token refresh")
}

// postTokenJSON posts a JSON body to a Cline token endpoint and parses the
// success-wrapped token record.
func (a *ClineAuth) postTokenJSON(ctx context.Context, endpoint string, requestBody []byte, opName string) (*ClineTokenRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(endpoint), bytes.NewReader(requestBody))
	if err != nil {
		return nil, fmt.Errorf("%s: create request: %w", opName, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s request failed: %w", opName, err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("%s: close response body error: %v", opName, errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: read response: %w", opName, err)
	}

	var envelope struct {
		Success bool `json:"success"`
		Data    struct {
			ClineTokenRecord
		} `json:"data"`
	}
	if err = json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("%s: parse response: %w", opName, err)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%s rejected (status %d)", opName, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s failed with status %d: %s", opName, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if !envelope.Success {
		return nil, fmt.Errorf("%s rejected by the Cline account API", opName)
	}

	record := &envelope.Data.ClineTokenRecord
	if strings.TrimSpace(record.AccessToken) == "" {
		return nil, fmt.Errorf("%s response missing accessToken", opName)
	}
	return record, nil
}
