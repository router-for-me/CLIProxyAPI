// Package kiro provides AWS SSO OIDC authentication for Kiro.
package kiro

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/oauthhttp"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// AWS SSO OIDC endpoints
	ssoOIDCEndpoint = "https://oidc.us-east-1.amazonaws.com"

	// Kiro's start URL for Builder ID
	builderIDStartURL = "https://view.awsapps.com/start"

	// Polling interval
	pollInterval = 5 * time.Second

	// Authorization code flow callback
	authCodeCallbackPath = "/oauth/callback"
	authCodeCallbackPort = 19877

	// User-Agent to match official Kiro IDE
	kiroUserAgent = "KiroIDE"
)

// SSOOIDCClient handles AWS SSO OIDC authentication.
type SSOOIDCClient struct {
	httpClient *http.Client
	cfg        *config.Config
}

// NewSSOOIDCClient creates a new SSO OIDC client.
func NewSSOOIDCClient(cfg *config.Config) *SSOOIDCClient {
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		client = util.SetOAuthProxy(&cfg.SDKConfig, client)
	}
	return &SSOOIDCClient{
		httpClient: client,
		cfg:        cfg,
	}
}

// RegisterClientResponse from AWS SSO OIDC.
type RegisterClientResponse struct {
	ClientID              string `json:"clientId"`
	ClientSecret          string `json:"clientSecret"`
	ClientIDIssuedAt      int64  `json:"clientIdIssuedAt"`
	ClientSecretExpiresAt int64  `json:"clientSecretExpiresAt"`
}

// StartDeviceAuthResponse from AWS SSO OIDC.
type StartDeviceAuthResponse struct {
	DeviceCode              string `json:"deviceCode"`
	UserCode                string `json:"userCode"`
	VerificationURI         string `json:"verificationUri"`
	VerificationURIComplete string `json:"verificationUriComplete"`
	ExpiresIn               int    `json:"expiresIn"`
	Interval                int    `json:"interval"`
}

// CreateTokenResponse from AWS SSO OIDC.
type CreateTokenResponse struct {
	AccessToken  string `json:"accessToken"`
	TokenType    string `json:"tokenType"`
	ExpiresIn    int    `json:"expiresIn"`
	RefreshToken string `json:"refreshToken"`
}

// RegisterClient registers a new OIDC client with AWS.
func (c *SSOOIDCClient) RegisterClient(ctx context.Context) (*RegisterClientResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	payload := map[string]interface{}{
		"clientName": "Kiro IDE",
		"clientType": "public",
		"scopes":     []string{"codewhisperer:completions", "codewhisperer:analysis", "codewhisperer:conversations", "codewhisperer:transformations", "codewhisperer:taskassist"},
		"grantTypes": []string{"urn:ietf:params:oauth:grant-type:device_code", "refresh_token"},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	endpoint := ssoOIDCEndpoint + "/client/register"
	status, _, respBody, err := oauthhttp.Do(
		ctx,
		c.httpClient,
		func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json")
			req.Header.Set("User-Agent", kiroUserAgent)
			return req, nil
		},
		oauthhttp.DefaultRetryConfig(),
	)
	if err != nil && status == 0 {
		return nil, err
	}

	if status != http.StatusOK {
		log.Debugf("register client failed (status %d): %s", status, string(respBody))
		if err != nil {
			return nil, fmt.Errorf("register client failed (status %d): %w", status, err)
		}
		return nil, fmt.Errorf("register client failed (status %d)", status)
	}
	if err != nil {
		return nil, err
	}

	var result RegisterClientResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// StartDeviceAuthorization starts the device authorization flow.
func (c *SSOOIDCClient) StartDeviceAuthorization(ctx context.Context, clientID, clientSecret string) (*StartDeviceAuthResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	payload := map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"startUrl":     builderIDStartURL,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	endpoint := ssoOIDCEndpoint + "/device_authorization"
	status, _, respBody, err := oauthhttp.Do(
		ctx,
		c.httpClient,
		func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json")
			req.Header.Set("User-Agent", kiroUserAgent)
			return req, nil
		},
		oauthhttp.DefaultRetryConfig(),
	)
	if err != nil && status == 0 {
		return nil, err
	}

	if status != http.StatusOK {
		log.Debugf("start device auth failed (status %d): %s", status, string(respBody))
		if err != nil {
			return nil, fmt.Errorf("start device auth failed (status %d): %w", status, err)
		}
		return nil, fmt.Errorf("start device auth failed (status %d)", status)
	}
	if err != nil {
		return nil, err
	}

	var result StartDeviceAuthResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// CreateToken polls for the access token after user authorization.
func (c *SSOOIDCClient) CreateToken(ctx context.Context, clientID, clientSecret, deviceCode string) (*CreateTokenResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	payload := map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"deviceCode":   deviceCode,
		"grantType":    "urn:ietf:params:oauth:grant-type:device_code",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	endpoint := ssoOIDCEndpoint + "/token"
	status, _, respBody, err := oauthhttp.Do(
		ctx,
		c.httpClient,
		func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json")
			req.Header.Set("User-Agent", kiroUserAgent)
			return req, nil
		},
		oauthhttp.DefaultRetryConfig(),
	)
	if err != nil && status == 0 {
		return nil, err
	}

	// Check for pending authorization
	if status == http.StatusBadRequest {
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(respBody, &errResp) == nil {
			if errResp.Error == "authorization_pending" {
				return nil, fmt.Errorf("authorization_pending")
			}
			if errResp.Error == "slow_down" {
				return nil, fmt.Errorf("slow_down")
			}
		}
		log.Debugf("create token failed: %s", string(respBody))
		return nil, fmt.Errorf("create token failed")
	}

	if status != http.StatusOK {
		log.Debugf("create token failed (status %d): %s", status, string(respBody))
		if err != nil {
			return nil, fmt.Errorf("create token failed (status %d): %w", status, err)
		}
		return nil, fmt.Errorf("create token failed (status %d)", status)
	}
	if err != nil {
		return nil, err
	}

	var result CreateTokenResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// RefreshToken refreshes an access token using the refresh token.
func (c *SSOOIDCClient) RefreshToken(ctx context.Context, clientID, clientSecret, refreshToken string) (*KiroTokenData, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	payload := map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"refreshToken": refreshToken,
		"grantType":    "refresh_token",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	endpoint := ssoOIDCEndpoint + "/token"
	status, _, respBody, err := oauthhttp.Do(
		ctx,
		c.httpClient,
		func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json")
			req.Header.Set("User-Agent", kiroUserAgent)
			return req, nil
		},
		oauthhttp.DefaultRetryConfig(),
	)
	if err != nil && status == 0 {
		return nil, err
	}

	if status != http.StatusOK {
		log.Debugf("token refresh failed (status %d): %s", status, string(respBody))
		if err != nil {
			return nil, fmt.Errorf("token refresh failed (status %d): %w", status, err)
		}
		return nil, fmt.Errorf("token refresh failed (status %d)", status)
	}
	if err != nil {
		return nil, err
	}

	var result CreateTokenResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	expiresAt := time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)

	return &KiroTokenData{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		ExpiresAt:    expiresAt.Format(time.RFC3339),
		AuthMethod:   "builder-id",
		Provider:     "AWS",
		ClientID:     clientID,
		ClientSecret: clientSecret,
	}, nil
}

// LoginWithBuilderID performs the full device code flow for AWS Builder ID.
func (c *SSOOIDCClient) LoginWithBuilderID(ctx context.Context) (*KiroTokenData, error) {
	fmt.Println("\n╔══════════════════════════════════════════════════════════╗")
	fmt.Println("║         Kiro Authentication (AWS Builder ID)              ║")
	fmt.Println("╚══════════════════════════════════════════════════════════╝")

	// Step 1: Register client
	fmt.Println("\nRegistering client...")
	regResp, err := c.RegisterClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to register client: %w", err)
	}
	log.Debugf("Client registered: %s", regResp.ClientID)

	// Step 2: Start device authorization
	fmt.Println("Starting device authorization...")
	authResp, err := c.StartDeviceAuthorization(ctx, regResp.ClientID, regResp.ClientSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to start device auth: %w", err)
	}

	// Step 3: Show user the verification URL
	fmt.Printf("\n")
	fmt.Println("════════════════════════════════════════════════════════════")
	fmt.Printf("  Open this URL in your browser:\n")
	fmt.Printf("  %s\n", authResp.VerificationURIComplete)
	fmt.Println("════════════════════════════════════════════════════════════")
	fmt.Printf("\n  Or go to: %s\n", authResp.VerificationURI)
	fmt.Printf("  And enter code: %s\n\n", authResp.UserCode)

	// Set incognito mode based on config (defaults to true for Kiro, can be overridden with --no-incognito)
	// Incognito mode enables multi-account support by bypassing cached sessions
	if c.cfg != nil {
		browser.SetIncognitoMode(c.cfg.IncognitoBrowser)
		if !c.cfg.IncognitoBrowser {
			log.Info("kiro: using normal browser mode (--no-incognito). Note: You may not be able to select a different account.")
		} else {
			log.Debug("kiro: using incognito mode for multi-account support")
		}
	} else {
		browser.SetIncognitoMode(true) // Default to incognito if no config
		log.Debug("kiro: using incognito mode for multi-account support (default)")
	}

	// Open browser using cross-platform browser package
	if err := browser.OpenURL(authResp.VerificationURIComplete); err != nil {
		log.Warnf("Could not open browser automatically: %v", err)
		fmt.Println("  Please open the URL manually in your browser.")
	} else {
		fmt.Println("  (Browser opened automatically)")
	}

	// Step 4: Poll for token
	fmt.Println("Waiting for authorization...")

	interval := pollInterval
	if authResp.Interval > 0 {
		interval = time.Duration(authResp.Interval) * time.Second
	}

	deadline := time.Now().Add(time.Duration(authResp.ExpiresIn) * time.Second)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			browser.CloseBrowser() // Cleanup on cancel
			return nil, ctx.Err()
		case <-time.After(interval):
			tokenResp, err := c.CreateToken(ctx, regResp.ClientID, regResp.ClientSecret, authResp.DeviceCode)
			if err != nil {
				errStr := err.Error()
				if strings.Contains(errStr, "authorization_pending") {
					fmt.Print(".")
					continue
				}
				if strings.Contains(errStr, "slow_down") {
					interval += 5 * time.Second
					continue
				}
				// Close browser on error before returning
				browser.CloseBrowser()
				return nil, fmt.Errorf("token creation failed: %w", err)
			}

			fmt.Println("\n\n✓ Authorization successful!")

			// Close the browser window
			if err := browser.CloseBrowser(); err != nil {
				log.Debugf("Failed to close browser: %v", err)
			}

			// Step 5: Get profile ARN from CodeWhisperer API
			fmt.Println("Fetching profile information...")
			profileArn := c.fetchProfileArn(ctx, tokenResp.AccessToken)

			// Fetch user email (tries CodeWhisperer API first, then userinfo endpoint, then JWT parsing)
			email := FetchUserEmailWithFallback(ctx, c.cfg, tokenResp.AccessToken)
			if email != "" {
				fmt.Printf("  Logged in as: %s\n", email)
			}

			expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

			return &KiroTokenData{
				AccessToken:  tokenResp.AccessToken,
				RefreshToken: tokenResp.RefreshToken,
				ProfileArn:   profileArn,
				ExpiresAt:    expiresAt.Format(time.RFC3339),
				AuthMethod:   "builder-id",
				Provider:     "AWS",
				ClientID:     regResp.ClientID,
				ClientSecret: regResp.ClientSecret,
				Email:        email,
			}, nil
		}
	}

	// Close browser on timeout for better UX
	if err := browser.CloseBrowser(); err != nil {
		log.Debugf("Failed to close browser on timeout: %v", err)
	}
	return nil, fmt.Errorf("authorization timed out")
}

// FetchUserEmail retrieves the user's email from AWS SSO OIDC userinfo endpoint.
// Falls back to JWT parsing if userinfo fails.
func (c *SSOOIDCClient) FetchUserEmail(ctx context.Context, accessToken string) string {
	// Method 1: Try userinfo endpoint (standard OIDC)
	email := c.tryUserInfoEndpoint(ctx, accessToken)
	if email != "" {
		return email
	}

	// Method 2: Fallback to JWT parsing
	return ExtractEmailFromJWT(accessToken)
}

// tryUserInfoEndpoint attempts to get user info from AWS SSO OIDC userinfo endpoint.
func (c *SSOOIDCClient) tryUserInfoEndpoint(ctx context.Context, accessToken string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ssoOIDCEndpoint+"/userinfo", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.Debugf("userinfo request failed: %v", err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		log.Debugf("userinfo endpoint returned status %d: %s", resp.StatusCode, string(respBody))
		return ""
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}

	log.Debugf("userinfo response: %s", string(respBody))

	var userInfo struct {
		Email             string `json:"email"`
		Sub               string `json:"sub"`
		PreferredUsername string `json:"preferred_username"`
		Name              string `json:"name"`
	}

	if err := json.Unmarshal(respBody, &userInfo); err != nil {
		return ""
	}

	if userInfo.Email != "" {
		return userInfo.Email
	}
	if userInfo.PreferredUsername != "" && strings.Contains(userInfo.PreferredUsername, "@") {
		return userInfo.PreferredUsername
	}
	return ""
}

// fetchProfileArn retrieves the profile ARN from CodeWhisperer API.
// This is needed for file naming since AWS SSO OIDC doesn't return profile info.
func (c *SSOOIDCClient) fetchProfileArn(ctx context.Context, accessToken string) string {
	// Try ListProfiles API first
	profileArn := c.tryListProfiles(ctx, accessToken)
	if profileArn != "" {
		return profileArn
	}

	// Fallback: Try ListAvailableCustomizations
	return c.tryListCustomizations(ctx, accessToken)
}

func (c *SSOOIDCClient) tryListProfiles(ctx context.Context, accessToken string) string {
	payload := map[string]interface{}{
		"origin": "AI_EDITOR",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return ""
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://codewhisperer.us-east-1.amazonaws.com", strings.NewReader(string(body)))
	if err != nil {
		return ""
	}

	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("x-amz-target", "AmazonCodeWhispererService.ListProfiles")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		log.Debugf("ListProfiles failed (status %d): %s", resp.StatusCode, string(respBody))
		return ""
	}

	log.Debugf("ListProfiles response: %s", string(respBody))

	var result struct {
		Profiles []struct {
			Arn string `json:"arn"`
		} `json:"profiles"`
		ProfileArn string `json:"profileArn"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return ""
	}

	if result.ProfileArn != "" {
		return result.ProfileArn
	}

	if len(result.Profiles) > 0 {
		return result.Profiles[0].Arn
	}

	return ""
}

func (c *SSOOIDCClient) tryListCustomizations(ctx context.Context, accessToken string) string {
	payload := map[string]interface{}{
		"origin": "AI_EDITOR",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return ""
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://codewhisperer.us-east-1.amazonaws.com", strings.NewReader(string(body)))
	if err != nil {
		return ""
	}

	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("x-amz-target", "AmazonCodeWhispererService.ListAvailableCustomizations")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		log.Debugf("ListAvailableCustomizations failed (status %d): %s", resp.StatusCode, string(respBody))
		return ""
	}

	log.Debugf("ListAvailableCustomizations response: %s", string(respBody))

	var result struct {
		Customizations []struct {
			Arn string `json:"arn"`
		} `json:"customizations"`
		ProfileArn string `json:"profileArn"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return ""
	}

	if result.ProfileArn != "" {
		return result.ProfileArn
	}

	if len(result.Customizations) > 0 {
		return result.Customizations[0].Arn
	}

	return ""
}

// RegisterClientForAuthCode registers a new OIDC client for authorization code flow.
func (c *SSOOIDCClient) RegisterClientForAuthCode(ctx context.Context, redirectURI string) (*RegisterClientResponse, error) {
	payload := map[string]interface{}{
		"clientName":   "Kiro IDE",
		"clientType":   "public",
		"scopes":       []string{"codewhisperer:completions", "codewhisperer:analysis", "codewhisperer:conversations", "codewhisperer:transformations", "codewhisperer:taskassist"},
		"grantTypes":   []string{"authorization_code", "refresh_token"},
		"redirectUris": []string{redirectURI},
		"issuerUrl":    builderIDStartURL,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ssoOIDCEndpoint+"/client/register", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", kiroUserAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("register client for auth code failed (status %d): %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("register client failed (status %d)", resp.StatusCode)
	}

	var result RegisterClientResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// AuthCodeCallbackResult contains the result from authorization code callback.
type AuthCodeCallbackResult struct {
	Code  string
	State string
	Error string
}

// startAuthCodeCallbackServer starts a local HTTP server to receive the authorization code callback.
func (c *SSOOIDCClient) startAuthCodeCallbackServer(ctx context.Context, expectedState string) (string, <-chan AuthCodeCallbackResult, error) {
	// Try to find an available port
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", authCodeCallbackPort))
	if err != nil {
		// Try with dynamic port
		log.Warnf("sso oidc: default port %d is busy, falling back to dynamic port", authCodeCallbackPort)
		listener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return "", nil, fmt.Errorf("failed to start callback server: %w", err)
		}
	}

	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d%s", port, authCodeCallbackPath)
	resultChan := make(chan AuthCodeCallbackResult, 1)

	server := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
	}

	mux := http.NewServeMux()
	mux.HandleFunc(authCodeCallbackPath, func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")
		errParam := r.URL.Query().Get("error")

		// Send response to browser
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if errParam != "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><title>Login Failed</title></head>
<body><h1>Login Failed</h1><p>Error: %s</p><p>You can close this window.</p></body></html>`, html.EscapeString(errParam))
			resultChan <- AuthCodeCallbackResult{Error: errParam}
			return
		}

		if state != expectedState {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `<!DOCTYPE html>
<html><head><title>Login Failed</title></head>
<body><h1>Login Failed</h1><p>Invalid state parameter</p><p>You can close this window.</p></body></html>`)
			resultChan <- AuthCodeCallbackResult{Error: "state mismatch"}
			return
		}

		fmt.Fprint(w, `<!DOCTYPE html>
<html><head><title>Login Successful</title></head>
<body><h1>Login Successful!</h1><p>You can close this window and return to the terminal.</p>
<script>window.close();</script></body></html>`)
		resultChan <- AuthCodeCallbackResult{Code: code, State: state}
	})

	server.Handler = mux

	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Debugf("auth code callback server error: %v", err)
		}
	}()

	go func() {
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Minute):
		case <-resultChan:
		}
		_ = server.Shutdown(context.Background())
	}()

	return redirectURI, resultChan, nil
}

// generatePKCEForAuthCode generates PKCE code verifier and challenge for authorization code flow.
func generatePKCEForAuthCode() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge, nil
}

// generateStateForAuthCode generates a random state parameter.
func generateStateForAuthCode() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// CreateTokenWithAuthCode exchanges authorization code for tokens.
func (c *SSOOIDCClient) CreateTokenWithAuthCode(ctx context.Context, clientID, clientSecret, code, codeVerifier, redirectURI string) (*CreateTokenResponse, error) {
	payload := map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"code":         code,
		"codeVerifier": codeVerifier,
		"redirectUri":  redirectURI,
		"grantType":    "authorization_code",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ssoOIDCEndpoint+"/token", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", kiroUserAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("create token with auth code failed (status %d): %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("create token failed (status %d)", resp.StatusCode)
	}

	var result CreateTokenResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// LoginWithBuilderIDAuthCode performs the authorization code flow for AWS Builder ID.
// This provides a better UX than device code flow as it uses automatic browser callback.
func (c *SSOOIDCClient) LoginWithBuilderIDAuthCode(ctx context.Context) (*KiroTokenData, error) {
	fmt.Println("\n╔══════════════════════════════════════════════════════════╗")
	fmt.Println("║     Kiro Authentication (AWS Builder ID - Auth Code)      ║")
	fmt.Println("╚══════════════════════════════════════════════════════════╝")

	// Step 1: Generate PKCE and state
	codeVerifier, codeChallenge, err := generatePKCEForAuthCode()
	if err != nil {
		return nil, fmt.Errorf("failed to generate PKCE: %w", err)
	}

	state, err := generateStateForAuthCode()
	if err != nil {
		return nil, fmt.Errorf("failed to generate state: %w", err)
	}

	// Step 2: Start callback server
	fmt.Println("\nStarting callback server...")
	redirectURI, resultChan, err := c.startAuthCodeCallbackServer(ctx, state)
	if err != nil {
		return nil, fmt.Errorf("failed to start callback server: %w", err)
	}
	log.Debugf("Callback server started, redirect URI: %s", redirectURI)

	// Step 3: Register client with auth code grant type
	fmt.Println("Registering client...")
	regResp, err := c.RegisterClientForAuthCode(ctx, redirectURI)
	if err != nil {
		return nil, fmt.Errorf("failed to register client: %w", err)
	}
	log.Debugf("Client registered: %s", regResp.ClientID)

	// Step 4: Build authorization URL
	scopes := "codewhisperer:completions,codewhisperer:analysis,codewhisperer:conversations"
	authURL := fmt.Sprintf("%s/authorize?response_type=code&client_id=%s&redirect_uri=%s&scopes=%s&state=%s&code_challenge=%s&code_challenge_method=S256",
		ssoOIDCEndpoint,
		regResp.ClientID,
		redirectURI,
		scopes,
		state,
		codeChallenge,
	)

	// Step 5: Open browser
	fmt.Println("\n════════════════════════════════════════════════════════════")
	fmt.Println("  Opening browser for authentication...")
	fmt.Println("════════════════════════════════════════════════════════════")
	fmt.Printf("\n  URL: %s\n\n", authURL)

	// Set incognito mode
	if c.cfg != nil {
		browser.SetIncognitoMode(c.cfg.IncognitoBrowser)
	} else {
		browser.SetIncognitoMode(true)
	}

	if err := browser.OpenURL(authURL); err != nil {
		log.Warnf("Could not open browser automatically: %v", err)
		fmt.Println("  ⚠ Could not open browser automatically.")
		fmt.Println("  Please open the URL above in your browser manually.")
	} else {
		fmt.Println("  (Browser opened automatically)")
	}

	fmt.Println("\n  Waiting for authorization callback...")

	// Step 6: Wait for callback
	select {
	case <-ctx.Done():
		browser.CloseBrowser()
		return nil, ctx.Err()
	case <-time.After(10 * time.Minute):
		browser.CloseBrowser()
		return nil, fmt.Errorf("authorization timed out")
	case result := <-resultChan:
		if result.Error != "" {
			browser.CloseBrowser()
			return nil, fmt.Errorf("authorization failed: %s", result.Error)
		}

		fmt.Println("\n✓ Authorization received!")

		// Close browser
		if err := browser.CloseBrowser(); err != nil {
			log.Debugf("Failed to close browser: %v", err)
		}

		// Step 7: Exchange code for tokens
		fmt.Println("Exchanging code for tokens...")
		tokenResp, err := c.CreateTokenWithAuthCode(ctx, regResp.ClientID, regResp.ClientSecret, result.Code, codeVerifier, redirectURI)
		if err != nil {
			return nil, fmt.Errorf("failed to exchange code for tokens: %w", err)
		}

		fmt.Println("\n✓ Authentication successful!")

		// Step 8: Get profile ARN
		fmt.Println("Fetching profile information...")
		profileArn := c.fetchProfileArn(ctx, tokenResp.AccessToken)

		// Fetch user email (tries CodeWhisperer API first, then userinfo endpoint, then JWT parsing)
		email := FetchUserEmailWithFallback(ctx, c.cfg, tokenResp.AccessToken)
		if email != "" {
			fmt.Printf("  Logged in as: %s\n", email)
		}

		expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

		return &KiroTokenData{
			AccessToken:  tokenResp.AccessToken,
			RefreshToken: tokenResp.RefreshToken,
			ProfileArn:   profileArn,
			ExpiresAt:    expiresAt.Format(time.RFC3339),
			AuthMethod:   "builder-id",
			Provider:     "AWS",
			ClientID:     regResp.ClientID,
			ClientSecret: regResp.ClientSecret,
			Email:        email,
		}, nil
	}
}
