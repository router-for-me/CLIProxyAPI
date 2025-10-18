package iflow

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

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// OAuth endpoints and client metadata are derived from the reference Python implementation.
	iFlowOAuthTokenEndpoint     = "https://iflow.cn/oauth/token"
	iFlowOAuthAuthorizeEndpoint = "https://iflow.cn/oauth"
	iFlowUserInfoEndpoint       = "https://iflow.cn/api/oauth/getUserInfo"
	iFlowSuccessRedirectURL     = "https://iflow.cn/oauth/success"

	// Client credentials provided by iFlow for the Code Assist integration.
	iFlowOAuthClientID     = "10009311001"
	iFlowOAuthClientSecret = "4Z3YjXycVsQvyGF1etiNlIBB4RsqSDtW"
)

// DefaultAPIBaseURL is the canonical chat completions endpoint.
const DefaultAPIBaseURL = "https://apis.iflow.cn/v1"

// SuccessRedirectURL is exposed for consumers needing the official success page.
const SuccessRedirectURL = iFlowSuccessRedirectURL

// CallbackPort defines the local port used for OAuth callbacks.
const CallbackPort = 11451

// IFlowAuth encapsulates the HTTP client helpers for the OAuth flow.
type IFlowAuth struct {
	httpClient *http.Client
}

// NewIFlowAuth constructs a new IFlowAuth with proxy-aware transport.
func NewIFlowAuth(cfg *config.Config) *IFlowAuth {
	client := &http.Client{Timeout: 30 * time.Second}
	return &IFlowAuth{httpClient: util.SetProxy(&cfg.SDKConfig, client)}
}

// AuthorizationURL builds the authorization URL and matching redirect URI.
func (ia *IFlowAuth) AuthorizationURL(state string, port int) (authURL, redirectURI string) {
	redirectURI = fmt.Sprintf("http://localhost:%d/oauth2callback", port)
	values := url.Values{}
	values.Set("loginMethod", "phone")
	values.Set("type", "phone")
	values.Set("redirect", redirectURI)
	values.Set("state", state)
	values.Set("client_id", iFlowOAuthClientID)
	authURL = fmt.Sprintf("%s?%s", iFlowOAuthAuthorizeEndpoint, values.Encode())
	return authURL, redirectURI
}

// ExchangeCodeForTokens exchanges an authorization code for access and refresh tokens.
func (ia *IFlowAuth) ExchangeCodeForTokens(ctx context.Context, code, redirectURI string) (*IFlowTokenData, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", iFlowOAuthClientID)
	form.Set("client_secret", iFlowOAuthClientSecret)

	req, err := ia.newTokenRequest(ctx, form)
	if err != nil {
		return nil, err
	}

	return ia.doTokenRequest(ctx, req)
}

// RefreshTokens exchanges a refresh token for a new access token.
func (ia *IFlowAuth) RefreshTokens(ctx context.Context, refreshToken string) (*IFlowTokenData, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", iFlowOAuthClientID)
	form.Set("client_secret", iFlowOAuthClientSecret)

	req, err := ia.newTokenRequest(ctx, form)
	if err != nil {
		return nil, err
	}

	return ia.doTokenRequest(ctx, req)
}

func (ia *IFlowAuth) newTokenRequest(ctx context.Context, form url.Values) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, iFlowOAuthTokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("iflow token: create request failed: %w", err)
	}

	basic := base64.StdEncoding.EncodeToString([]byte(iFlowOAuthClientID + ":" + iFlowOAuthClientSecret))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Basic "+basic)
	return req, nil
}

func (ia *IFlowAuth) doTokenRequest(ctx context.Context, req *http.Request) (*IFlowTokenData, error) {
	resp, err := ia.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("iflow token: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("iflow token: read response failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("iflow token request failed: status=%d body=%s", resp.StatusCode, string(body))
		return nil, fmt.Errorf("iflow token: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tokenResp IFlowTokenResponse
	if err = json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("iflow token: decode response failed: %w", err)
	}

	data := &IFlowTokenData{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		TokenType:    tokenResp.TokenType,
		Scope:        tokenResp.Scope,
		Expire:       time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339),
	}

	if tokenResp.AccessToken == "" {
		log.Debug(string(body))
		return nil, fmt.Errorf("iflow token: missing access token in response")
	}

	info, errAPI := ia.FetchUserInfo(ctx, tokenResp.AccessToken)
	if errAPI != nil {
		return nil, fmt.Errorf("iflow token: fetch user info failed: %w", errAPI)
	}
	if strings.TrimSpace(info.APIKey) == "" {
		return nil, fmt.Errorf("iflow token: empty api key returned")
	}
	email := strings.TrimSpace(info.Email)
	if email == "" {
		email = strings.TrimSpace(info.Phone)
	}
	if email == "" {
		return nil, fmt.Errorf("iflow token: missing account email/phone in user info")
	}
	data.APIKey = info.APIKey
	data.Email = email

	return data, nil
}

// FetchUserInfo retrieves account metadata (including API key) for the provided access token.
func (ia *IFlowAuth) FetchUserInfo(ctx context.Context, accessToken string) (*userInfoData, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("iflow api key: access token is empty")
	}

	endpoint := fmt.Sprintf("%s?accessToken=%s", iFlowUserInfoEndpoint, url.QueryEscape(accessToken))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("iflow api key: create request failed: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := ia.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("iflow api key: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("iflow api key: read response failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("iflow api key failed: status=%d body=%s", resp.StatusCode, string(body))
		return nil, fmt.Errorf("iflow api key: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result userInfoResponse
	if err = json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("iflow api key: decode body failed: %w", err)
	}

	if !result.Success {
		return nil, fmt.Errorf("iflow api key: request not successful")
	}

	if result.Data.APIKey == "" {
		return nil, fmt.Errorf("iflow api key: missing api key in response")
	}

	return &result.Data, nil
}

// CreateTokenStorage converts token data into persistence storage.
func (ia *IFlowAuth) CreateTokenStorage(data *IFlowTokenData) *IFlowTokenStorage {
	if data == nil {
		return nil
	}
	return &IFlowTokenStorage{
		AccessToken:  data.AccessToken,
		RefreshToken: data.RefreshToken,
		LastRefresh:  time.Now().Format(time.RFC3339),
		Expire:       data.Expire,
		APIKey:       data.APIKey,
		Email:        data.Email,
		TokenType:    data.TokenType,
		Scope:        data.Scope,
	}
}

// UpdateTokenStorage updates the persisted token storage with latest token data.
func (ia *IFlowAuth) UpdateTokenStorage(storage *IFlowTokenStorage, data *IFlowTokenData) {
	if storage == nil || data == nil {
		return
	}
	storage.AccessToken = data.AccessToken
	storage.RefreshToken = data.RefreshToken
	storage.LastRefresh = time.Now().Format(time.RFC3339)
	storage.Expire = data.Expire
	if data.APIKey != "" {
		storage.APIKey = data.APIKey
	}
	if data.Email != "" {
		storage.Email = data.Email
	}
	storage.TokenType = data.TokenType
	storage.Scope = data.Scope
}

// IFlowTokenResponse models the OAuth token endpoint response.
type IFlowTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}

// IFlowTokenData captures processed token details.
type IFlowTokenData struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	Scope        string
	Expire       string
	APIKey       string
	Email        string
}

// userInfoResponse represents the structure returned by the user info endpoint.
type userInfoResponse struct {
	Success bool         `json:"success"`
	Data    userInfoData `json:"data"`
}

type userInfoData struct {
	APIKey string `json:"apiKey"`
	Email  string `json:"email"`
	Phone  string `json:"phone"`
}
