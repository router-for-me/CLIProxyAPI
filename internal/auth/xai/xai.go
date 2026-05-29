package xai

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
)

// XAIAuth performs xAI OAuth discovery, token exchange, and refresh.
type XAIAuth struct {
	httpClient *http.Client
}

// NewXAIAuth creates an xAI OAuth helper using config proxy settings.
func NewXAIAuth(cfg *config.Config) *XAIAuth {
	return NewXAIAuthWithProxyURL(cfg, "")
}

// NewXAIAuthWithProxyURL creates an xAI OAuth helper with an explicit proxy URL.
func NewXAIAuthWithProxyURL(cfg *config.Config, proxyURL string) *XAIAuth {
	effectiveProxyURL := strings.TrimSpace(proxyURL)
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effectiveProxyURL
	return &XAIAuth{httpClient: util.SetProxy(&sdkCfg, &http.Client{})}
}

// ValidateOAuthEndpoint validates an endpoint returned by xAI discovery.
func ValidateOAuthEndpoint(rawURL string, field string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("xai discovery %s is empty", field)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("xai discovery %s is invalid: %w", field, err)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("xai discovery %s must use https: %q", field, rawURL)
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host != "x.ai" && !strings.HasSuffix(host, ".x.ai") {
		return "", fmt.Errorf("xai discovery %s host %q is not on x.ai", field, host)
	}
	return rawURL, nil
}

// BuildAuthorizeURL builds the browser URL for xAI OAuth.
func BuildAuthorizeURL(params AuthorizeURLParams) (string, error) {
	endpoint, err := ValidateOAuthEndpoint(params.AuthorizationEndpoint, "authorization_endpoint")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(params.RedirectURI) == "" {
		return "", fmt.Errorf("xai authorize URL: redirect URI is required")
	}
	if strings.TrimSpace(params.CodeChallenge) == "" {
		return "", fmt.Errorf("xai authorize URL: code challenge is required")
	}
	if strings.TrimSpace(params.State) == "" {
		return "", fmt.Errorf("xai authorize URL: state is required")
	}
	if strings.TrimSpace(params.Nonce) == "" {
		return "", fmt.Errorf("xai authorize URL: nonce is required")
	}
	values := url.Values{
		"response_type":         {"code"},
		"client_id":             {ClientID},
		"redirect_uri":          {strings.TrimSpace(params.RedirectURI)},
		"scope":                 {Scope},
		"code_challenge":        {strings.TrimSpace(params.CodeChallenge)},
		"code_challenge_method": {"S256"},
		"state":                 {strings.TrimSpace(params.State)},
		"nonce":                 {strings.TrimSpace(params.Nonce)},
		"plan":                  {"generic"},
		"referrer":              {"cli-proxy-api"},
	}
	return endpoint + "?" + values.Encode(), nil
}

// Discover resolves xAI OAuth endpoints through OIDC discovery.
func (a *XAIAuth) Discover(ctx context.Context) (*Discovery, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DiscoveryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("xai discovery: create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xai discovery: request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("xai discovery: close response body error: %v", errClose)
		}
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("xai discovery: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("xai discovery failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
	}
	if err = json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("xai discovery: parse response: %w", err)
	}
	authorizationEndpoint, err := ValidateOAuthEndpoint(payload.AuthorizationEndpoint, "authorization_endpoint")
	if err != nil {
		return nil, err
	}
	tokenEndpoint, err := ValidateOAuthEndpoint(payload.TokenEndpoint, "token_endpoint")
	if err != nil {
		return nil, err
	}
	return &Discovery{AuthorizationEndpoint: authorizationEndpoint, TokenEndpoint: tokenEndpoint}, nil
}

// ExchangeCodeForTokens exchanges an authorization code for xAI OAuth tokens.
func (a *XAIAuth) ExchangeCodeForTokens(ctx context.Context, code, redirectURI string, pkceCodes *PKCECodes, tokenEndpoint string) (*AuthBundle, error) {
	if pkceCodes == nil {
		return nil, fmt.Errorf("xai token exchange: PKCE codes are required")
	}
	if strings.TrimSpace(code) == "" {
		return nil, fmt.Errorf("xai token exchange: authorization code is required")
	}
	if strings.TrimSpace(redirectURI) == "" {
		return nil, fmt.Errorf("xai token exchange: redirect URI is required")
	}
	if strings.TrimSpace(tokenEndpoint) == "" {
		discovery, errDiscover := a.Discover(ctx)
		if errDiscover != nil {
			return nil, errDiscover
		}
		tokenEndpoint = discovery.TokenEndpoint
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {strings.TrimSpace(code)},
		"redirect_uri":  {strings.TrimSpace(redirectURI)},
		"client_id":     {ClientID},
		"code_verifier": {pkceCodes.CodeVerifier},
	}
	tokenData, err := a.postTokenForm(ctx, tokenEndpoint, form)
	if err != nil {
		return nil, err
	}
	return &AuthBundle{
		TokenData:     *tokenData,
		LastRefresh:   time.Now().UTC().Format(time.RFC3339),
		BaseURL:       DefaultAPIBaseURL,
		RedirectURI:   strings.TrimSpace(redirectURI),
		TokenEndpoint: strings.TrimSpace(tokenEndpoint),
	}, nil
}

// RefreshTokens refreshes an xAI access token.
func (a *XAIAuth) RefreshTokens(ctx context.Context, refreshToken, tokenEndpoint string) (*TokenData, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, fmt.Errorf("xai token refresh: refresh token is required")
	}
	if strings.TrimSpace(tokenEndpoint) == "" {
		discovery, errDiscover := a.Discover(ctx)
		if errDiscover != nil {
			return nil, errDiscover
		}
		tokenEndpoint = discovery.TokenEndpoint
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {ClientID},
		"refresh_token": {strings.TrimSpace(refreshToken)},
	}
	return a.postTokenForm(ctx, tokenEndpoint, form)
}

func (a *XAIAuth) postTokenForm(ctx context.Context, tokenEndpoint string, form url.Values) (*TokenData, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(tokenEndpoint), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("xai token request: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xai token request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("xai token request: close response body error: %v", errClose)
		}
	}()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("xai token response: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("xai token request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err = json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("xai token response: parse body: %w", err)
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return nil, fmt.Errorf("xai token response missing access_token")
	}
	email, subject := parseJWTIdentity(payload.IDToken)
	return &TokenData{
		AccessToken:  strings.TrimSpace(payload.AccessToken),
		RefreshToken: strings.TrimSpace(payload.RefreshToken),
		IDToken:      strings.TrimSpace(payload.IDToken),
		TokenType:    strings.TrimSpace(payload.TokenType),
		ExpiresIn:    payload.ExpiresIn,
		Expire:       time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second).UTC().Format(time.RFC3339),
		Email:        email,
		Subject:      subject,
	}, nil
}

// CreateTokenStorage converts an auth bundle into persistable storage.
func (a *XAIAuth) CreateTokenStorage(bundle *AuthBundle) *TokenStorage {
	if bundle == nil {
		return nil
	}
	return &TokenStorage{
		Type:          "xai",
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
