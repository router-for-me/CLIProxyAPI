// Package antigravity provides authentication and token management functionality
// for Google's Antigravity AI services. It handles OAuth2 authentication flows,
// including obtaining tokens via web-based authorization, storing tokens,
// and refreshing them when they expire.
package antigravity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	// OAuth endpoints and client metadata for Google Antigravity
	antigravityOAuthTokenEndpoint     = "https://oauth2.googleapis.com/token"
	antigravityOAuthAuthorizeEndpoint = "https://accounts.google.com/o/oauth2/v2/auth"
	antigravityUserInfoEndpoint       = "https://www.googleapis.com/oauth2/v1/userinfo"
	antigravitySuccessRedirectURL     = "https://accounts.google.com/o/oauth2/approval"
	errorRedirectURL                  = "https://accounts.google.com/o/oauth2/error"

	// Client credentials for Google Antigravity
	antigravityOAuthClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	antigravityOAuthClientSecret = "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"
)

var (
	// OAuth scopes required for Google Antigravity
	antigravityOauthScopes = []string{
		"https://www.googleapis.com/auth/cloud-platform",
		"https://www.googleapis.com/auth/userinfo.email",
		"https://www.googleapis.com/auth/userinfo.profile",
	}
)

// OAuthResult captures the outcome of the local OAuth callback.
type OAuthResult struct {
	Code  string
	State string
	Error string
}

// AntigravityAuth encapsulates the HTTP client helpers for the OAuth flow.
type AntigravityAuth struct {
	httpClient *http.Client
	server     *http.Server
	result     chan *OAuthResult
	mu         sync.Mutex
	running    bool
}

// NewAntigravityAuth constructs a new AntigravityAuth with proxy-aware transport.
func NewAntigravityAuth(cfg *config.Config) *AntigravityAuth {
	client := &http.Client{Timeout: 30 * time.Second}
	return &AntigravityAuth{
		httpClient: util.SetProxy(&cfg.SDKConfig, client),
		result:     make(chan *OAuthResult, 1),
	}
}

// GenerateAuthUrl builds the authorization URL and matching redirect URI.
func (aa *AntigravityAuth) GenerateAuthUrl(port int) string {
	redirectURI := fmt.Sprintf("http://localhost:%d/oauth-callback", port)

	values := url.Values{}
	values.Set("client_id", antigravityOAuthClientID)
	values.Set("redirect_uri", redirectURI)
	values.Set("scope", strings.Join(antigravityOauthScopes, " "))
	values.Set("response_type", "code")
	values.Set("access_type", "offline")
	values.Set("prompt", "consent")
	values.Set("state", "antigravity-state")

	authURL := fmt.Sprintf("%s?%s", antigravityOAuthAuthorizeEndpoint, values.Encode())
	return authURL
}

// HandleCallback processes the OAuth callback from Google's auth server.
// It extracts the authorization code and exchanges it for tokens.
func (aa *AntigravityAuth) HandleCallback(ctx context.Context, code, redirectURI string) (*AntigravityTokenData, error) {
	if strings.TrimSpace(code) == "" {
		return nil, fmt.Errorf("antigravity auth: authorization code is empty")
	}

	clientSecret := antigravityOAuthClientSecret

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", antigravityOAuthClientID)
	form.Set("client_secret", clientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, antigravityOAuthTokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("antigravity token: create request failed: %w", err)
	}

	basic := base64.StdEncoding.EncodeToString([]byte(antigravityOAuthClientID + ":" + clientSecret))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Basic "+basic)

	resp, err := aa.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("antigravity token: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("antigravity token: read response failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("antigravity token request failed: status=%d body=%s", resp.StatusCode, string(body))
		return nil, fmt.Errorf("antigravity token: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tokenResp AntigravityTokenResponse
	if err = json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("antigravity token: decode response failed: %w", err)
	}

	if tokenResp.AccessToken == "" {
		log.Debug(string(body))
		return nil, fmt.Errorf("antigravity token: missing access token in response")
	}

	// Fetch user information
	userInfo, err := aa.FetchUserInfo(ctx, tokenResp.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("antigravity token: fetch user info failed: %w", err)
	}

	// Check if user is Google internal
	isGoogleInternal := strings.HasSuffix(userInfo.Email, "@google.com")

	data := &AntigravityTokenData{
		AccessToken:      tokenResp.AccessToken,
		RefreshToken:     tokenResp.RefreshToken,
		TokenType:        tokenResp.TokenType,
		Scope:            tokenResp.Scope,
		Expire:           time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339),
		Email:            userInfo.Email,
		IsGoogleInternal: isGoogleInternal,
	}

	return data, nil
}

// FetchUserInfo retrieves account metadata for the provided access token.
func (aa *AntigravityAuth) FetchUserInfo(ctx context.Context, accessToken string) (*userInfoData, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("antigravity user info: access token is empty")
	}

	endpoint := fmt.Sprintf("%s?alt=json", antigravityUserInfoEndpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("antigravity user info: create request failed: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", accessToken))

	resp, err := aa.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("antigravity user info: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("antigravity user info: read response failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("antigravity user info failed: status=%d body=%s", resp.StatusCode, string(body))
		return nil, fmt.Errorf("antigravity user info: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	emailResult := gjson.GetBytes(body, "email")
	if !emailResult.Exists() || emailResult.Type != gjson.String {
		return nil, fmt.Errorf("antigravity user info: missing email in response")
	}

	return &userInfoData{
		Email: emailResult.String(),
	}, nil
}

// CreateTokenStorage converts token data into persistence storage.
func (aa *AntigravityAuth) CreateTokenStorage(data *AntigravityTokenData) *AntigravityTokenStorage {
	if data == nil {
		return nil
	}
	return &AntigravityTokenStorage{
		AccessToken:      data.AccessToken,
		RefreshTokenStr:  data.RefreshToken,
		LastRefresh:      time.Now().Format(time.RFC3339),
		Expire:           data.Expire,
		Email:            data.Email,
		TokenType:        data.TokenType,
		Scope:            data.Scope,
		IsGoogleInternal: data.IsGoogleInternal,
	}
}

// UpdateTokenStorage updates the persisted token storage with latest token data.
func (aa *AntigravityAuth) UpdateTokenStorage(storage *AntigravityTokenStorage, data *AntigravityTokenData) {
	if storage == nil || data == nil {
		return
	}
	storage.AccessToken = data.AccessToken
	storage.RefreshTokenStr = data.RefreshToken
	storage.LastRefresh = time.Now().Format(time.RFC3339)
	storage.Expire = data.Expire
	if data.Email != "" {
		storage.Email = data.Email
	}
	storage.TokenType = data.TokenType
	storage.Scope = data.Scope
	storage.IsGoogleInternal = data.IsGoogleInternal
}

// StartOAuthFlow initiates the complete OAuth flow for Google Antigravity.
func (aa *AntigravityAuth) StartOAuthFlow(ctx context.Context, noBrowser ...bool) (*AntigravityTokenData, error) {
	port, err := aa.findAvailablePort()
	if err != nil {
		return nil, fmt.Errorf("antigravity oauth: failed to find available port: %w", err)
	}

	log.Infof("Antigravity OAuth server starting on port %d", port)

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth-callback", aa.handleCallback)

	aa.mu.Lock()
	if aa.running {
		aa.mu.Unlock()
		return nil, fmt.Errorf("antigravity oauth server already running")
	}

	aa.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}
	aa.running = true
	aa.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := aa.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Errorf("OAuth server error: %v", err)
		}
	}()

	authURL := aa.GenerateAuthUrl(port)
	redirectURI := fmt.Sprintf("http://localhost:%d/oauth-callback", port)

	if len(noBrowser) == 1 && !noBrowser[0] {
		fmt.Printf("Opening browser for Google Antigravity authentication on port %d...\n", port)

		if !browser.IsAvailable() {
			log.Warn("No browser available on this system")
			fmt.Printf("Please manually open this URL in your browser:\n\n%s\n", authURL)
		} else {
			if err := browser.OpenURL(authURL); err != nil {
				fmt.Printf("Please manually open this URL in your browser:\n\n%s\n", authURL)

				platformInfo := browser.GetPlatformInfo()
				log.Debugf("Browser platform info: %+v", platformInfo)
			} else {
				log.Debug("Browser opened successfully")
			}
		}
	} else {
		fmt.Printf("Please open this URL in your browser (using port %d):\n\n%s\n", port, authURL)
	}

	fmt.Println("Waiting for Google Antigravity authentication callback...")

	result, err := aa.waitForCallback(5 * time.Minute)
	if err != nil {
		return nil, fmt.Errorf("antigravity oauth: callback failed: %w", err)
	}

	aa.stopServer()

	if result.Error != "" {
		return nil, fmt.Errorf("antigravity oauth: authentication failed: %s", result.Error)
	}

	tokenData, err := aa.HandleCallback(ctx, result.Code, redirectURI)
	if err != nil {
		return nil, fmt.Errorf("antigravity oauth: token exchange failed: %w", err)
	}

	fmt.Printf("Google Antigravity authentication successful! User: %s\n", tokenData.Email)
	if tokenData.IsGoogleInternal {
		fmt.Println("Google internal user detected - special features enabled")
	}

	return tokenData, nil
}

// handleCallback processes the OAuth callback from Google's auth server.
func (aa *AntigravityAuth) handleCallback(w http.ResponseWriter, r *http.Request) {
	// Add panic recovery to ensure response is always sent
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("Panic in antigravity callback handler: %v", r)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	}()

	// Set CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	query := r.URL.Query()
	if errParam := strings.TrimSpace(query.Get("error")); errParam != "" {
		aa.sendResult(&OAuthResult{Error: errParam})
		http.Redirect(w, r, errorRedirectURL, http.StatusFound)
		return
	}

	code := strings.TrimSpace(query.Get("code"))
	if code == "" {
		aa.sendResult(&OAuthResult{Error: "missing_code"})
		http.Redirect(w, r, errorRedirectURL, http.StatusFound)
		return
	}

	state := query.Get("state")
	aa.sendResult(&OAuthResult{Code: code, State: state})

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`<html><head><meta charset="utf-8"><title>Authentication successful</title><script>setTimeout(function(){window.close();},5000);</script></head><body><h1>Authentication successful!</h1><p>You can close this window.</p><p>This window will close automatically in 5 seconds.</p></body></html>`))
}

// waitForCallback blocks until a callback result, or timeout occurs.
func (aa *AntigravityAuth) waitForCallback(timeout time.Duration) (*OAuthResult, error) {
	select {
	case res := <-aa.result:
		return res, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout waiting for OAuth callback")
	}
}

// stopServer gracefully terminates the callback listener.
func (aa *AntigravityAuth) stopServer() {
	aa.mu.Lock()
	defer aa.mu.Unlock()

	if !aa.running || aa.server == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := aa.server.Shutdown(ctx); err != nil {
		log.Warnf("antigravity oauth server stop error: %v", err)
	}

	aa.running = false
	aa.server = nil
}

// sendResult sends the OAuth result to the result channel.
func (aa *AntigravityAuth) sendResult(res *OAuthResult) {
	select {
	case aa.result <- res:
	default:
		log.Debug("antigravity oauth result channel full, dropping result")
	}
}

// findAvailablePort finds an available port for the OAuth callback server.
func (aa *AntigravityAuth) findAvailablePort() (int, error) {
	for port := 8080; port <= 8090; port++ {
		listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", fmt.Sprintf(":%d", port))
		if err == nil {
			listener.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available ports found in range 8080-8090")
}

// AntigravityTokenResponse models the OAuth token endpoint response.
type AntigravityTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}

// AntigravityTokenData captures processed token details.
type AntigravityTokenData struct {
	AccessToken      string
	RefreshToken     string
	TokenType        string
	Scope            string
	Expire           string
	Email            string
	IsGoogleInternal bool
}

// userInfoData represents the user information from Google's OAuth2 endpoint.
type userInfoData struct {
	Email string `json:"email"`
}
