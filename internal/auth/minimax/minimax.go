// Package minimax provides authentication and token management for the MiniMax
// platform. It handles the RFC 8628 OAuth2 Device Authorization Grant with PKCE.
package minimax

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/singleflight"
)

const (
	// ClientID is the public OAuth client id used by the official MiniMax CLI.
	ClientID = "659cf4c1-615c-45f6-a5f6-4bf15eb476e5"

	// RegionGlobal is the global (international) MiniMax region.
	RegionGlobal = "global"
	// RegionCN is the mainland China MiniMax region.
	RegionCN = "cn"

	// OAuthHostGlobal is the account host serving the OAuth endpoints globally.
	OAuthHostGlobal = "https://account.minimax.io"
	// OAuthHostCN is the account host serving the OAuth endpoints in China.
	OAuthHostCN = "https://account.minimaxi.com"

	// APIBaseURLGlobal is the API base URL for the global region.
	APIBaseURLGlobal = "https://api.minimax.io"
	// APIBaseURLCN is the API base URL for the China region.
	APIBaseURLCN = "https://api.minimax.cn"

	// AnthropicPathPrefix is prepended to the API base URL so the shared Claude
	// request builder resolves to {base}/anthropic/v1/messages.
	AnthropicPathPrefix = "/anthropic"

	// DeviceCodeGrantType is the RFC 8628 device authorization grant type.
	DeviceCodeGrantType = "urn:ietf:params:oauth:grant-type:device_code"

	// defaultPollInterval is the minimum delay between token polls.
	defaultPollInterval = 3 * time.Second
	// MaxPollDuration caps how long the login waits for user authorization.
	MaxPollDuration = 15 * time.Minute
	// refreshThreshold is when a token is considered due for refresh.
	refreshThreshold = 5 * time.Minute
	// httpClientTimeout only guards OAuth credential acquisition calls.
	httpClientTimeout = 30 * time.Second
)

// Scopes are the OAuth scopes requested during the device flow.
var Scopes = []string{"openid", "profile", "coding_plan"}

var minimaxRefreshGroup singleflight.Group

// IsCNRegion reports whether the supplied region string is the China region.
func IsCNRegion(region string) bool {
	return strings.EqualFold(strings.TrimSpace(region), RegionCN)
}

// NormalizeRegion canonicalizes a region string, defaulting to global.
func NormalizeRegion(region string) string {
	if IsCNRegion(region) {
		return RegionCN
	}
	return RegionGlobal
}

// ResolveOAuthHost returns the account host for the given region.
func ResolveOAuthHost(region string) string {
	if IsCNRegion(region) {
		return OAuthHostCN
	}
	return OAuthHostGlobal
}

// ResolveAPIBaseURL returns the API base URL for the given region.
func ResolveAPIBaseURL(region string) string {
	if IsCNRegion(region) {
		return APIBaseURLCN
	}
	return APIBaseURLGlobal
}

// ResolveClaudeBaseURL returns the Anthropic-compatible base URL for the region.
// The trailing slash is trimmed so callers can append "/v1/messages" directly.
func ResolveClaudeBaseURL(region string) string {
	return strings.TrimRight(ResolveAPIBaseURL(region), "/") + AnthropicPathPrefix
}

// ProviderCN is the provider key for the China region. Global and China are
// separate keys, matching the way kimi.com and kimi.ai are split, so each
// credential records which region it belongs to.
const ProviderCN = "minimax-cn"

// ProviderForRegion returns the provider key for a region.
func ProviderForRegion(region string) string {
	if IsCNRegion(region) {
		return ProviderCN
	}
	return "minimax"
}

// IsCNProvider reports whether a provider key targets the China region.
func IsCNProvider(provider string) bool {
	return strings.EqualFold(strings.TrimSpace(provider), ProviderCN)
}

// ResolveRegionFromAuth detects the region an Auth belongs to. Explicit
// configuration (region, base_url, resource_url) takes precedence, and the
// provider key itself is authoritative so the two regions never share state.
func ResolveRegionFromAuth(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return RegionGlobal
	}
	// The provider key is the strongest signal: it is what the user picked.
	if IsCNProvider(auth.Provider) {
		return RegionCN
	}
	if auth.Provider == "minimax" {
		return RegionGlobal
	}
	if auth.Attributes != nil {
		if region := auth.Attributes["region"]; strings.TrimSpace(region) != "" {
			return NormalizeRegion(region)
		}
		for _, key := range []string{"base_url", "resource_url"} {
			if isCNBaseURL(auth.Attributes[key]) {
				return RegionCN
			}
		}
	}
	if auth.Metadata != nil {
		if region, ok := auth.Metadata["region"].(string); ok && strings.TrimSpace(region) != "" {
			return NormalizeRegion(region)
		}
		for _, key := range []string{"base_url", "resource_url"} {
			if value, ok := auth.Metadata[key].(string); ok && isCNBaseURL(value) {
				return RegionCN
			}
		}
	}
	if storage, ok := auth.Storage.(*MinimaxTokenStorage); ok && storage != nil {
		if strings.TrimSpace(storage.Region) != "" {
			return NormalizeRegion(storage.Region)
		}
		if isCNBaseURL(storage.BaseURL) || isCNBaseURL(storage.ResourceURL) {
			return RegionCN
		}
	}
	return RegionGlobal
}

func isCNBaseURL(raw string) bool {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return false
	}
	return strings.Contains(value, "minimax.cn") || strings.Contains(value, "minimaxi.com")
}

// ResolveBaseURL resolves the upstream API base URL, honoring an explicit
// base_url or resource_url from the credential before falling back to the region.
func ResolveBaseURL(auth *cliproxyauth.Auth) string {
	if auth != nil {
		if auth.Attributes != nil {
			for _, key := range []string{"base_url", "resource_url"} {
				if raw := strings.TrimRight(strings.TrimSpace(auth.Attributes[key]), "/"); raw != "" {
					return raw
				}
			}
		}
		if auth.Metadata != nil {
			for _, key := range []string{"base_url", "resource_url"} {
				if raw, ok := auth.Metadata[key].(string); ok {
					if raw = strings.TrimRight(strings.TrimSpace(raw), "/"); raw != "" {
						return raw
					}
				}
			}
		}
	}
	return ResolveAPIBaseURL(ResolveRegionFromAuth(auth))
}

// ResolveClaudeUpstreamURL returns the Anthropic-compatible messages URL.
// The Claude request builder appends "/v1/messages", so the returned base must
// end with the Anthropic path prefix.
func ResolveClaudeUpstreamURL(auth *cliproxyauth.Auth) string {
	base := ResolveBaseURL(auth)
	if strings.HasSuffix(base, AnthropicPathPrefix) {
		return base
	}
	return base + AnthropicPathPrefix
}

// PKCECodes holds a PKCE verifier and its S256 challenge.
type PKCECodes struct {
	// Verifier is the high-entropy secret kept by the client.
	Verifier string
	// Challenge is the base64url-encoded SHA-256 of the verifier.
	Challenge string
}

// GeneratePKCECodes creates a new PKCE verifier/challenge pair.
func GeneratePKCECodes() (*PKCECodes, error) {
	verifier, err := randomBase64URL(32)
	if err != nil {
		return nil, fmt.Errorf("minimax: failed to generate PKCE verifier: %w", err)
	}
	sum := sha256.Sum256([]byte(verifier))
	return &PKCECodes{
		Verifier:  verifier,
		Challenge: base64.RawURLEncoding.EncodeToString(sum[:]),
	}, nil
}

func randomBase64URL(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// DeviceCodeResponse is the MiniMax response to a device authorization request.
type DeviceCodeResponse struct {
	// UserCode is the short code the user must enter.
	UserCode string `json:"user_code"`
	// VerificationURI is where the user enters the code.
	VerificationURI string `json:"verification_uri"`
	// ExpiredIn is the absolute Unix time in milliseconds at which the code expires.
	// MiniMax deviates from RFC 8628 here: this is a deadline, not a duration.
	ExpiredIn int64 `json:"expired_in"`
	// Interval is the minimum delay in milliseconds between polls.
	Interval int64 `json:"interval"`
	// State echoes the client-supplied state and is verified by the client.
	State string `json:"state"`
}

// expiryDeadline converts the absolute expiry timestamp into a duration.
func (d *DeviceCodeResponse) expiryDeadline() time.Duration {
	if d == nil || d.ExpiredIn <= 0 {
		return MaxPollDuration
	}
	remaining := time.Until(time.UnixMilli(d.ExpiredIn))
	if remaining <= 0 {
		return time.Millisecond
	}
	if remaining > MaxPollDuration {
		return MaxPollDuration
	}
	return remaining
}

func (d *DeviceCodeResponse) pollInterval() time.Duration {
	if d == nil || d.Interval <= 0 {
		return defaultPollInterval
	}
	interval := time.Duration(d.Interval) * time.Millisecond
	if interval < defaultPollInterval {
		return defaultPollInterval
	}
	return interval
}

// TokenResponse is the payload returned by the token endpoint. MiniMax signals
// pending authorization with HTTP 200 and a "status" field rather than the
// RFC 8628 "authorization_pending" error code.
type TokenResponse struct {
	// Status is "success" or "pending" while the user has not yet authorized.
	Status string `json:"status"`
	// Error and ErrorDescription carry OAuth-style failures.
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	// AccessToken is the bearer token for the platform API.
	AccessToken string `json:"access_token"`
	// RefreshToken renews the access token.
	RefreshToken string `json:"refresh_token"`
	// TokenType is typically "Bearer".
	TokenType string `json:"token_type"`
	// ExpiredIn is the absolute Unix time in milliseconds at which the access token expires.
	ExpiredIn int64 `json:"expired_in"`
	// Scope is the granted scope list.
	Scope string `json:"scope"`
	// ResourceURL optionally overrides the API base URL.
	ResourceURL string `json:"resource_url"`
}

// expiresAt returns the access token expiry as a time, or zero when unknown.
func (t *TokenResponse) expiresAt() time.Time {
	if t == nil || t.ExpiredIn <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(t.ExpiredIn)
}

// TokenData is the normalized token payload used for storage.
type TokenData struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	Scope        string
	ExpiresAt    time.Time
	ResourceURL  string
}

// AuthBundle bundles the result of a completed device flow.
type AuthBundle struct {
	TokenData *TokenData
	Region    string
}

// DeviceFlowClient performs the OAuth device flow against MiniMax.
type DeviceFlowClient struct {
	httpClient         *http.Client
	region             string
	deviceCodeEndpoint string
	tokenEndpoint      string
}

// NewDeviceFlowClient creates a device flow client for the given region.
func NewDeviceFlowClient(cfg *config.Config, region string) *DeviceFlowClient {
	client := &http.Client{Timeout: httpClientTimeout}
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		sdkCfg.ProxyURL = strings.TrimSpace(cfg.ProxyURL)
	}
	client = util.SetProxy(&sdkCfg, client)
	return &DeviceFlowClient{
		httpClient: client,
		region:     NormalizeRegion(region),
	}
}

// SetHTTPClient overrides the internal HTTP client, primarily for tests.
func (c *DeviceFlowClient) SetHTTPClient(client *http.Client) {
	if client != nil {
		c.httpClient = client
	}
}

// SetEndpoints overrides the OAuth endpoints so the flow can be exercised
// against a local server instead of the live MiniMax account host.
func (c *DeviceFlowClient) SetEndpoints(deviceCodeURL, tokenURL string) {
	if trimmed := strings.TrimSpace(deviceCodeURL); trimmed != "" {
		c.deviceCodeEndpoint = trimmed
	}
	if trimmed := strings.TrimSpace(tokenURL); trimmed != "" {
		c.tokenEndpoint = trimmed
	}
}

func (c *DeviceFlowClient) deviceCodeURL() string {
	if c.deviceCodeEndpoint != "" {
		return c.deviceCodeEndpoint
	}
	return ResolveOAuthHost(c.region) + "/oauth2/device/code"
}

func (c *DeviceFlowClient) tokenURL() string {
	if c.tokenEndpoint != "" {
		return c.tokenEndpoint
	}
	return ResolveOAuthHost(c.region) + "/oauth2/token"
}

// RequestDeviceCode starts the device authorization flow.
func (c *DeviceFlowClient) RequestDeviceCode(ctx context.Context) (*DeviceCodeResponse, *PKCECodes, error) {
	pkce, err := GeneratePKCECodes()
	if err != nil {
		return nil, nil, err
	}
	state, err := randomBase64URL(16)
	if err != nil {
		return nil, nil, fmt.Errorf("minimax: failed to generate state: %w", err)
	}

	form := url.Values{}
	form.Set("client_id", ClientID)
	form.Set("scope", strings.Join(Scopes, " "))
	form.Set("code_challenge", pkce.Challenge)
	form.Set("code_challenge_method", "S256")
	form.Set("state", state)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.deviceCodeURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, fmt.Errorf("minimax: failed to create device code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	body, err := c.do(req, "device code")
	if err != nil {
		return nil, nil, err
	}

	var response DeviceCodeResponse
	if err = json.Unmarshal(body, &response); err != nil {
		return nil, nil, fmt.Errorf("minimax: failed to parse device code response: %w", err)
	}
	if strings.TrimSpace(response.UserCode) == "" || strings.TrimSpace(response.VerificationURI) == "" {
		return nil, nil, fmt.Errorf("minimax: device code response missing user_code or verification_uri")
	}
	// The server must echo the state we generated; a mismatch means the response
	// does not belong to this flow.
	if strings.TrimSpace(response.State) == "" || response.State != state {
		return nil, nil, fmt.Errorf("minimax: device code response state mismatch")
	}
	return &response, pkce, nil
}

// PollForToken polls the token endpoint until the user authorizes or the code expires.
func (c *DeviceFlowClient) PollForToken(ctx context.Context, deviceCode *DeviceCodeResponse, pkce *PKCECodes) (*TokenData, error) {
	if deviceCode == nil {
		return nil, fmt.Errorf("minimax: device code is nil")
	}
	if pkce == nil {
		return nil, fmt.Errorf("minimax: PKCE codes are nil")
	}

	interval := deviceCode.pollInterval()
	deadline := time.Now().Add(deviceCode.expiryDeadline())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("minimax: context cancelled: %w", ctx.Err())
		case <-ticker.C:
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("minimax: device code expired")
			}
			form := url.Values{}
			form.Set("grant_type", DeviceCodeGrantType)
			form.Set("client_id", ClientID)
			form.Set("user_code", deviceCode.UserCode)
			form.Set("code_verifier", pkce.Verifier)

			token, pending, errExchange := c.postToken(ctx, form, "device code")
			if errExchange != nil {
				return nil, errExchange
			}
			if pending {
				continue
			}
			return token, nil
		}
	}
}

// RefreshToken exchanges a refresh token for a new access token.
func (c *DeviceFlowClient) RefreshToken(ctx context.Context, refreshToken string) (*TokenData, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, fmt.Errorf("minimax: refresh token is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	refreshToken = strings.TrimSpace(refreshToken)
	flightKey := c.tokenURL() + ":" + refreshToken

	result, err, _ := minimaxRefreshGroup.Do(flightKey, func() (interface{}, error) {
		return c.refreshTokenSingleFlight(context.WithoutCancel(ctx), refreshToken)
	})
	if err != nil {
		return nil, err
	}
	tokenData, ok := result.(*TokenData)
	if !ok || tokenData == nil {
		return nil, fmt.Errorf("minimax: refresh failed: invalid single-flight result")
	}
	return tokenData, nil
}

func (c *DeviceFlowClient) refreshTokenSingleFlight(ctx context.Context, refreshToken string) (*TokenData, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", ClientID)
	form.Set("refresh_token", refreshToken)
	token, _, err := c.postToken(ctx, form, "refresh")
	return token, err
}

// postToken performs a token-endpoint call. It returns a nil token with a nil
// error while authorization is still pending, which the caller treats as a
// signal to keep polling.
func (c *DeviceFlowClient) postToken(ctx context.Context, form url.Values, label string) (*TokenData, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, false, fmt.Errorf("minimax: failed to create %s request: %w", label, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	body, err := c.do(req, label)
	if err != nil {
		return nil, false, err
	}

	var response TokenResponse
	if err = json.Unmarshal(body, &response); err != nil {
		return nil, false, fmt.Errorf("minimax: failed to parse %s response: %w", label, err)
	}

	// MiniMax answers pending polls with HTTP 200 and {"status":"pending"}.
	if strings.EqualFold(strings.TrimSpace(response.Status), "pending") {
		return nil, true, nil
	}
	if response.Error != "" {
		switch response.Error {
		case "authorization_pending", "slow_down":
			return nil, true, nil
		case "expired_token":
			return nil, false, fmt.Errorf("minimax: device code expired")
		case "access_denied":
			return nil, false, fmt.Errorf("minimax: access denied by user")
		default:
			return nil, false, fmt.Errorf("minimax: OAuth error: %s - %s", response.Error, response.ErrorDescription)
		}
	}
	if strings.TrimSpace(response.AccessToken) == "" {
		return nil, false, fmt.Errorf("minimax: empty access token in %s response", label)
	}

	return &TokenData{
		AccessToken:  response.AccessToken,
		RefreshToken: response.RefreshToken,
		TokenType:    response.TokenType,
		Scope:        response.Scope,
		ExpiresAt:    response.expiresAt(),
		ResourceURL:  strings.TrimSpace(response.ResourceURL),
	}, false, nil
}

// do executes an OAuth request and returns the response body, surfacing the
// standard error body on non-2xx responses.
func (c *DeviceFlowClient) do(req *http.Request, label string) ([]byte, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("minimax: %s request failed: %w", label, err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("minimax %s: close body error: %v", label, errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("minimax: failed to read %s response: %w", label, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResponse TokenResponse
		if json.Unmarshal(body, &errResponse) == nil && errResponse.Error != "" {
			return nil, fmt.Errorf("minimax: %s request failed with status %d: %s - %s",
				label, resp.StatusCode, errResponse.Error, errResponse.ErrorDescription)
		}
		return nil, fmt.Errorf("minimax: %s request failed with status %d: %s", label, resp.StatusCode, string(body))
	}
	return body, nil
}

// MinimaxAuth drives the MiniMax device authorization flow.
type MinimaxAuth struct {
	deviceClient *DeviceFlowClient
	region       string
}

// NewMinimaxAuth creates an auth service for the given region.
func NewMinimaxAuth(cfg *config.Config, region string) *MinimaxAuth {
	normRegion := NormalizeRegion(region)
	return &MinimaxAuth{
		deviceClient: NewDeviceFlowClient(cfg, normRegion),
		region:       normRegion,
	}
}

// StartDeviceFlow initiates the device flow authentication.
func (m *MinimaxAuth) StartDeviceFlow(ctx context.Context) (*DeviceCodeResponse, *PKCECodes, error) {
	return m.deviceClient.RequestDeviceCode(ctx)
}

// WaitForAuthorization polls for user authorization and returns the auth bundle.
func (m *MinimaxAuth) WaitForAuthorization(ctx context.Context, deviceCode *DeviceCodeResponse, pkce *PKCECodes) (*AuthBundle, error) {
	tokenData, err := m.deviceClient.PollForToken(ctx, deviceCode, pkce)
	if err != nil {
		return nil, err
	}
	return &AuthBundle{TokenData: tokenData, Region: m.region}, nil
}

// CreateTokenStorage converts an auth bundle into persistable storage.
func (m *MinimaxAuth) CreateTokenStorage(bundle *AuthBundle) *MinimaxTokenStorage {
	if bundle == nil || bundle.TokenData == nil {
		return nil
	}
	region := NormalizeRegion(bundle.Region)
	if region == "" {
		region = m.region
	}
	storage := &MinimaxTokenStorage{
		Type:         ProviderForRegion(region),
		Region:       region,
		AccessToken:  bundle.TokenData.AccessToken,
		RefreshToken: bundle.TokenData.RefreshToken,
		TokenType:    bundle.TokenData.TokenType,
		Scope:        bundle.TokenData.Scope,
		ResourceURL:  bundle.TokenData.ResourceURL,
		BaseURL:      ResolveAPIBaseURL(region),
		LastRefresh:  time.Now().UTC().Format(time.RFC3339),
	}
	if storage.ResourceURL != "" {
		storage.BaseURL = storage.ResourceURL
	}
	if !bundle.TokenData.ExpiresAt.IsZero() {
		storage.Expired = bundle.TokenData.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return storage
}
