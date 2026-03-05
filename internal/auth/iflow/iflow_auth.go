package iflow

import (
	"compress/gzip"
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

	// Cookie authentication endpoints
	iFlowAPIKeyEndpoint = "https://platform.iflow.cn/api/openapi/apikey"

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
	Cookie       string
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

// iFlowAPIKeyResponse represents the response from the API key endpoint
type iFlowAPIKeyResponse struct {
	Success bool         `json:"success"`
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Data    iFlowKeyData `json:"data"`
	Extra   interface{}  `json:"extra"`
}

// iFlowKeyData contains the API key information
type iFlowKeyData struct {
	HasExpired bool   `json:"hasExpired"`
	ExpireTime string `json:"expireTime"`
	Name       string `json:"name"`
	APIKey     string `json:"apiKey"`
	APIKeyMask string `json:"apiKeyMask"`
}

// iFlowRefreshRequest represents the request body for refreshing API key
type iFlowRefreshRequest struct {
	Name string `json:"name"`
}

// AuthenticateWithCookie performs authentication using browser cookies
func (ia *IFlowAuth) AuthenticateWithCookie(ctx context.Context, cookie string) (*IFlowTokenData, error) {
	if strings.TrimSpace(cookie) == "" {
		return nil, fmt.Errorf("iflow cookie authentication: cookie is empty")
	}

	// First, get initial API key information using GET request to obtain the name
	keyInfo, err := ia.fetchAPIKeyInfo(ctx, cookie)
	if err != nil {
		return nil, fmt.Errorf("iflow cookie authentication: fetch initial API key info failed: %w", err)
	}

	// Refresh the API key using POST request
	refreshedKeyInfo, err := ia.RefreshAPIKey(ctx, cookie, keyInfo.Name)
	if err != nil {
		return nil, fmt.Errorf("iflow cookie authentication: refresh API key failed: %w", err)
	}

	// Convert to token data format using refreshed key
	data := &IFlowTokenData{
		APIKey: refreshedKeyInfo.APIKey,
		Expire: refreshedKeyInfo.ExpireTime,
		Email:  refreshedKeyInfo.Name,
		Cookie: cookie,
	}

	return data, nil
}

// fetchAPIKeyInfo retrieves API key information using GET request with cookie
func (ia *IFlowAuth) fetchAPIKeyInfo(ctx context.Context, cookie string) (*iFlowKeyData, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, iFlowAPIKeyEndpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("iflow cookie: create GET request failed: %w", err)
	}

	// Set cookie and other headers to mimic browser
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")

	resp, err := ia.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("iflow cookie: GET request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Handle gzip compression
	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gzipReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("iflow cookie: create gzip reader failed: %w", err)
		}
		defer func() { _ = gzipReader.Close() }()
		reader = gzipReader
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("iflow cookie: read GET response failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("iflow cookie GET request failed: status=%d body=%s", resp.StatusCode, string(body))
		return nil, fmt.Errorf("iflow cookie: GET request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var keyResp iFlowAPIKeyResponse
	if err = json.Unmarshal(body, &keyResp); err != nil {
		return nil, fmt.Errorf("iflow cookie: decode GET response failed: %w", err)
	}

	if !keyResp.Success {
		return nil, fmt.Errorf("iflow cookie: GET request not successful: %s", keyResp.Message)
	}

	// Handle initial response where apiKey field might be apiKeyMask
	if keyResp.Data.APIKey == "" && keyResp.Data.APIKeyMask != "" {
		keyResp.Data.APIKey = keyResp.Data.APIKeyMask
	}

	return &keyResp.Data, nil
}

// RefreshAPIKey refreshes the API key using POST request
func (ia *IFlowAuth) RefreshAPIKey(ctx context.Context, cookie, name string) (*iFlowKeyData, error) {
	if strings.TrimSpace(cookie) == "" {
		return nil, fmt.Errorf("iflow cookie refresh: cookie is empty")
	}
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("iflow cookie refresh: name is empty")
	}

	// Prepare request body
	refreshReq := iFlowRefreshRequest{
		Name: name,
	}

	bodyBytes, err := json.Marshal(refreshReq)
	if err != nil {
		return nil, fmt.Errorf("iflow cookie refresh: marshal request failed: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, iFlowAPIKeyEndpoint, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return nil, fmt.Errorf("iflow cookie refresh: create POST request failed: %w", err)
	}

	// Set cookie and other headers to mimic browser
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Origin", "https://platform.iflow.cn")
	req.Header.Set("Referer", "https://platform.iflow.cn/")

	resp, err := ia.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("iflow cookie refresh: POST request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Handle gzip compression
	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gzipReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("iflow cookie refresh: create gzip reader failed: %w", err)
		}
		defer func() { _ = gzipReader.Close() }()
		reader = gzipReader
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("iflow cookie refresh: read POST response failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("iflow cookie POST request failed: status=%d body=%s", resp.StatusCode, string(body))
		return nil, fmt.Errorf("iflow cookie refresh: POST request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var keyResp iFlowAPIKeyResponse
	if err = json.Unmarshal(body, &keyResp); err != nil {
		return nil, fmt.Errorf("iflow cookie refresh: decode POST response failed: %w", err)
	}

	if !keyResp.Success {
		return nil, fmt.Errorf("iflow cookie refresh: POST request not successful: %s", keyResp.Message)
	}

	return &keyResp.Data, nil
}

// ShouldRefreshAPIKey checks if the API key needs to be refreshed (within 2 days of expiry)
func ShouldRefreshAPIKey(expireTime string) (bool, time.Duration, error) {
	if strings.TrimSpace(expireTime) == "" {
		return false, 0, fmt.Errorf("iflow cookie: expire time is empty")
	}

	expire, err := time.Parse("2006-01-02 15:04", expireTime)
	if err != nil {
		return false, 0, fmt.Errorf("iflow cookie: parse expire time failed: %w", err)
	}

	now := time.Now()
	twoDaysFromNow := now.Add(48 * time.Hour)

	needsRefresh := expire.Before(twoDaysFromNow)
	timeUntilExpiry := expire.Sub(now)

	return needsRefresh, timeUntilExpiry, nil
}

// CreateCookieTokenStorage converts cookie-based token data into persistence storage
func (ia *IFlowAuth) CreateCookieTokenStorage(data *IFlowTokenData) *IFlowTokenStorage {
	if data == nil {
		return nil
	}

	// Only save the BXAuth field from the cookie
	bxAuth := ExtractBXAuth(data.Cookie)
	cookieToSave := ""
	if bxAuth != "" {
		cookieToSave = "BXAuth=" + bxAuth + ";"
	}

	return &IFlowTokenStorage{
		APIKey:      data.APIKey,
		Email:       data.Email,
		Expire:      data.Expire,
		Cookie:      cookieToSave,
		LastRefresh: time.Now().Format(time.RFC3339),
		Type:        "iflow",
	}
}

// UpdateCookieTokenStorage updates the persisted token storage with refreshed API key data
func (ia *IFlowAuth) UpdateCookieTokenStorage(storage *IFlowTokenStorage, keyData *iFlowKeyData) {
	if storage == nil || keyData == nil {
		return
	}

	storage.APIKey = keyData.APIKey
	storage.Expire = keyData.ExpireTime
	storage.LastRefresh = time.Now().Format(time.RFC3339)
}
