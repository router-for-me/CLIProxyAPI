package meta

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

// Auth performs Meta OAuth discovery, device-code login, and token refresh.
type Auth struct {
	httpClient *http.Client
}

var metaRefreshGroup singleflight.Group

// NewMetaAuth creates a Meta OAuth helper using config proxy settings.
func NewMetaAuth(cfg *config.Config) *Auth {
	return NewMetaAuthWithProxyURL(cfg, "")
}

// NewMetaAuthWithProxyURL creates a Meta OAuth helper with an explicit proxy URL.
func NewMetaAuthWithProxyURL(cfg *config.Config, proxyURL string) *Auth {
	effectiveProxyURL := strings.TrimSpace(proxyURL)
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effectiveProxyURL
	return &Auth{httpClient: util.SetProxy(&sdkCfg, &http.Client{Timeout: httpClientTimeout})}
}

// ValidateOAuthEndpoint validates an endpoint returned by Meta discovery.
func ValidateOAuthEndpoint(rawURL string, field string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("meta discovery %s is empty", field)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("meta discovery %s is invalid: %w", field, err)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("meta discovery %s must use https: %q", field, rawURL)
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host != "meta.com" && !strings.HasSuffix(host, ".meta.com") {
		return "", fmt.Errorf("meta discovery %s host %q is not on meta.com", field, host)
	}
	return rawURL, nil
}

// Discover resolves Meta OAuth endpoints through OIDC discovery.
func (a *Auth) Discover(ctx context.Context) (*Discovery, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DiscoveryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("meta discovery: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("meta discovery: request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("meta discovery: close response body error: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return nil, fmt.Errorf("meta discovery: read response: %w", errRead)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("meta discovery failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
		TokenEndpoint               string `json:"token_endpoint"`
	}
	if err = json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("meta discovery: parse response: %w", err)
	}
	deviceAuthorizationEndpoint, err := ValidateOAuthEndpoint(payload.DeviceAuthorizationEndpoint, "device_authorization_endpoint")
	if err != nil {
		return nil, err
	}
	tokenEndpoint, err := ValidateOAuthEndpoint(payload.TokenEndpoint, "token_endpoint")
	if err != nil {
		return nil, err
	}
	return &Discovery{
		DeviceAuthorizationEndpoint: deviceAuthorizationEndpoint,
		TokenEndpoint:               tokenEndpoint,
	}, nil
}

// StartDeviceFlow requests a device code from Meta.
func (a *Auth) StartDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {
	discovery, errDiscover := a.Discover(ctx)
	if errDiscover != nil {
		return nil, errDiscover
	}
	return a.RequestDeviceCode(ctx, discovery.DeviceAuthorizationEndpoint, discovery.TokenEndpoint)
}

// RequestDeviceCode requests a device authorization code from the given endpoint.
func (a *Auth) RequestDeviceCode(ctx context.Context, deviceAuthorizationEndpoint, tokenEndpoint string) (*DeviceCodeResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	deviceAuthorizationEndpoint = strings.TrimSpace(deviceAuthorizationEndpoint)
	if deviceAuthorizationEndpoint == "" {
		return nil, fmt.Errorf("meta device code: device authorization endpoint is required")
	}

	form := url.Values{
		"client_id": {ClientID},
		"scope":     {Scope},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, deviceAuthorizationEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("meta device code: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, errDo := a.httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("meta device code request failed: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("meta device code: close response body error: %v", errClose)
		}
	}()

	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return nil, fmt.Errorf("meta device code: read response: %w", errRead)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("meta device code request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var deviceCode DeviceCodeResponse
	if err = json.Unmarshal(body, &deviceCode); err != nil {
		return nil, fmt.Errorf("meta device code: parse response: %w", err)
	}
	if strings.TrimSpace(deviceCode.DeviceCode) == "" {
		return nil, fmt.Errorf("meta device code: response missing device_code")
	}
	if strings.TrimSpace(deviceCode.UserCode) == "" {
		return nil, fmt.Errorf("meta device code: response missing user_code")
	}
	if strings.TrimSpace(deviceCode.VerificationURI) == "" && strings.TrimSpace(deviceCode.VerificationURIComplete) == "" {
		return nil, fmt.Errorf("meta device code: response missing verification URI")
	}
	deviceCode.TokenEndpoint = strings.TrimSpace(tokenEndpoint)
	return &deviceCode, nil
}

// WaitForAuthorization polls until the user authorizes the device code and returns tokens.
func (a *Auth) WaitForAuthorization(ctx context.Context, deviceCode *DeviceCodeResponse) (*AuthBundle, error) {
	tokenData, err := a.PollForToken(ctx, deviceCode)
	if err != nil {
		return nil, err
	}
	tokenEndpoint := ""
	if deviceCode != nil {
		tokenEndpoint = strings.TrimSpace(deviceCode.TokenEndpoint)
	}
	return &AuthBundle{
		TokenData:     *tokenData,
		LastRefresh:   time.Now().UTC().Format(time.RFC3339),
		BaseURL:       DefaultAPIBaseURL,
		TokenEndpoint: tokenEndpoint,
	}, nil
}

// PollForToken polls the token endpoint until the user authorizes or the device code expires.
func (a *Auth) PollForToken(ctx context.Context, deviceCode *DeviceCodeResponse) (*TokenData, error) {
	if deviceCode == nil {
		return nil, fmt.Errorf("meta device code response is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	tokenEndpoint := strings.TrimSpace(deviceCode.TokenEndpoint)
	if tokenEndpoint == "" {
		discovery, errDiscover := a.Discover(ctx)
		if errDiscover != nil {
			return nil, errDiscover
		}
		tokenEndpoint = discovery.TokenEndpoint
	}

	interval := time.Duration(deviceCode.Interval) * time.Second
	if interval < defaultPollInterval {
		interval = defaultPollInterval
	}

	deadline := time.Now().Add(MaxPollDuration)
	if deviceCode.ExpiresIn > 0 {
		codeDeadline := time.Now().Add(time.Duration(deviceCode.ExpiresIn) * time.Second)
		if codeDeadline.Before(deadline) {
			deadline = codeDeadline
		}
	}

	firstAttempt := true
	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("meta device code: context cancelled: %w", ctx.Err())
		case <-timer.C:
			if !firstAttempt && time.Now().After(deadline) {
				return nil, fmt.Errorf("meta device code expired")
			}
			firstAttempt = false

			token, pollErr, nextInterval, shouldContinue := a.exchangeDeviceCode(ctx, tokenEndpoint, deviceCode.DeviceCode, interval)
			if token != nil {
				return token, nil
			}
			if !shouldContinue {
				return nil, pollErr
			}
			interval = nextInterval
			timer.Reset(interval)
		}
	}
}

// exchangeDeviceCode attempts to exchange a device code for tokens.
// Returns (token, error, nextInterval, shouldContinue).
func (a *Auth) exchangeDeviceCode(ctx context.Context, tokenEndpoint, deviceCode string, interval time.Duration) (*TokenData, error, time.Duration, bool) {
	form := url.Values{
		"grant_type":  {DeviceCodeGrantType},
		"device_code": {strings.TrimSpace(deviceCode)},
		"client_id":   {ClientID},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(tokenEndpoint), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("meta device token: create request: %w", err), interval, false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("meta device token request failed: %w", err), interval, false
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("meta device token: close response body error: %v", errClose)
		}
	}()

	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return nil, fmt.Errorf("meta device token: read response: %w", errRead), interval, false
	}

	var payload struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		IDToken          string `json:"id_token"`
		TokenType        string `json:"token_type"`
		ExpiresIn        int    `json:"expires_in"`
	}
	if err = json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("meta device token: parse response: %w", err), interval, false
	}

	if payload.Error != "" {
		switch payload.Error {
		case "authorization_pending":
			return nil, nil, interval, true
		case "slow_down":
			nextInterval := interval + defaultPollInterval
			return nil, nil, nextInterval, true
		case "expired_token":
			return nil, fmt.Errorf("meta device code expired"), interval, false
		case "access_denied":
			return nil, fmt.Errorf("meta device authorization denied"), interval, false
		default:
			desc := strings.TrimSpace(payload.ErrorDescription)
			if desc != "" {
				return nil, fmt.Errorf("meta device token error: %s: %s", payload.Error, desc), interval, false
			}
			return nil, fmt.Errorf("meta device token error: %s", payload.Error), interval, false
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("meta device token request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body))), interval, false
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return nil, fmt.Errorf("meta device token response missing access_token"), interval, false
	}

	email, subject := parseJWTIdentity(payload.IDToken)
	return buildTokenData(payload.AccessToken, payload.RefreshToken, payload.IDToken, payload.TokenType, payload.ExpiresIn, email, subject), nil, interval, false
}

// RefreshTokens refreshes a Meta access token.
func (a *Auth) RefreshTokens(ctx context.Context, refreshToken, tokenEndpoint string) (*TokenData, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, fmt.Errorf("meta token refresh: refresh token is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	refreshToken = strings.TrimSpace(refreshToken)
	if strings.TrimSpace(tokenEndpoint) == "" {
		discovery, errDiscover := a.Discover(ctx)
		if errDiscover != nil {
			return nil, errDiscover
		}
		tokenEndpoint = discovery.TokenEndpoint
	}
	tokenEndpoint = strings.TrimSpace(tokenEndpoint)

	result, err, _ := metaRefreshGroup.Do(refreshToken, func() (interface{}, error) {
		return a.refreshTokensSingleFlight(context.WithoutCancel(ctx), refreshToken, tokenEndpoint)
	})
	if err != nil {
		return nil, err
	}
	tokenData, ok := result.(*TokenData)
	if !ok || tokenData == nil {
		return nil, fmt.Errorf("meta token refresh failed: invalid single-flight result")
	}
	return tokenData, nil
}

func (a *Auth) refreshTokensSingleFlight(ctx context.Context, refreshToken, tokenEndpoint string) (*TokenData, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {ClientID},
		"refresh_token": {refreshToken},
	}
	return a.postTokenForm(ctx, tokenEndpoint, form)
}

func (a *Auth) postTokenForm(ctx context.Context, tokenEndpoint string, form url.Values) (*TokenData, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(tokenEndpoint), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("meta token request: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, errDo := a.httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("meta token request failed: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("meta token request: close response body error: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return nil, fmt.Errorf("meta token response: read body: %w", errRead)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("meta token request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err = json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("meta token response: parse body: %w", err)
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return nil, fmt.Errorf("meta token response missing access_token")
	}
	email, subject := parseJWTIdentity(payload.IDToken)
	return buildTokenData(payload.AccessToken, payload.RefreshToken, payload.IDToken, payload.TokenType, payload.ExpiresIn, email, subject), nil
}

// CreateTokenStorage converts an auth bundle into persistable storage.
func (a *Auth) CreateTokenStorage(bundle *AuthBundle) *TokenStorage {
	if bundle == nil {
		return nil
	}
	return &TokenStorage{
		Type:          "meta",
		AccessToken:   bundle.TokenData.AccessToken,
		RefreshToken:  bundle.TokenData.RefreshToken,
		IDToken:       bundle.TokenData.IDToken,
		TokenType:     bundle.TokenData.TokenType,
		ExpiresIn:     bundle.TokenData.ExpiresIn,
		Expire:        bundle.TokenData.Expire,
		LastRefresh:   bundle.LastRefresh,
		Email:         strings.TrimSpace(bundle.TokenData.Email),
		Subject:       bundle.TokenData.Subject,
		BaseURL:       firstNonEmpty(bundle.BaseURL, DefaultAPIBaseURL),
		RedirectURI:   bundle.RedirectURI,
		TokenEndpoint: bundle.TokenEndpoint,
		AuthKind:      "oauth",
	}
}

func buildTokenData(accessToken, refreshToken, idToken, tokenType string, expiresIn int, email, subject string) *TokenData {
	tokenData := &TokenData{
		AccessToken:  strings.TrimSpace(accessToken),
		RefreshToken: strings.TrimSpace(refreshToken),
		IDToken:      strings.TrimSpace(idToken),
		TokenType:    strings.TrimSpace(tokenType),
		ExpiresIn:    expiresIn,
		Email:        email,
		Subject:      subject,
	}
	if expiresIn > 0 {
		tokenData.Expire = time.Now().Add(time.Duration(expiresIn) * time.Second).UTC().Format(time.RFC3339)
	}
	return tokenData
}

func parseJWTIdentity(token string) (email string, subject string) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "", ""
	}
	payload := parts[1]
	payload += strings.Repeat("=", (4-len(payload)%4)%4)
	raw, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return "", ""
	}
	var claims map[string]any
	if err = json.Unmarshal(raw, &claims); err != nil {
		return "", ""
	}
	if v, ok := claims["email"].(string); ok {
		email = strings.TrimSpace(v)
	}
	if v, ok := claims["sub"].(string); ok {
		subject = strings.TrimSpace(v)
	}
	return email, subject
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
