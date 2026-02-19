// Package kiro provides OAuth2 authentication for Kiro using native Google login.
package kiro

import (
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

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// Kiro auth endpoint
	kiroAuthEndpoint = "https://prod.us-east-1.auth.desktop.kiro.dev"
	
	// Default callback port
	defaultCallbackPort = 9876
	
	// Auth timeout
	authTimeout = 10 * time.Minute
)

// KiroTokenResponse represents the response from Kiro token endpoint.
type KiroTokenResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ProfileArn   string `json:"profileArn"`
	ExpiresIn    int    `json:"expiresIn"`
}

// KiroOAuth handles the OAuth flow for Kiro authentication.
type KiroOAuth struct {
	httpClient *http.Client
	cfg        *config.Config
}

// NewKiroOAuth creates a new Kiro OAuth handler.
func NewKiroOAuth(cfg *config.Config) *KiroOAuth {
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		client = util.SetProxy(&cfg.SDKConfig, client)
	}
	return &KiroOAuth{
		httpClient: client,
		cfg:        cfg,
	}
}

// generateCodeVerifier generates a random code verifier for PKCE.
func generateCodeVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// generateCodeChallenge generates the code challenge from verifier.
func generateCodeChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// generateState generates a random state parameter.
func generateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// AuthResult contains the authorization code and state from callback.
type AuthResult struct {
	Code  string
	State string
	Error string
}

// startCallbackServer starts a local HTTP server to receive the OAuth callback.
func (o *KiroOAuth) startCallbackServer(ctx context.Context, expectedState string) (string, <-chan AuthResult, error) {
	// Try to find an available port - use localhost like Kiro does
	listener, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", defaultCallbackPort))
	if err != nil {
		// Try with dynamic port (RFC 8252 allows dynamic ports for native apps)
		log.Warnf("kiro oauth: default port %d is busy, falling back to dynamic port", defaultCallbackPort)
		listener, err = net.Listen("tcp", "localhost:0")
		if err != nil {
			return "", nil, fmt.Errorf("failed to start callback server: %w", err)
		}
	}

	port := listener.Addr().(*net.TCPAddr).Port
	// Use http scheme for local callback server
	redirectURI := fmt.Sprintf("http://localhost:%d/oauth/callback", port)
	resultChan := make(chan AuthResult, 1)

	server := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")
		errParam := r.URL.Query().Get("error")

		if errParam != "" {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `<html><body><h1>Login Failed</h1><p>%s</p><p>You can close this window.</p></body></html>`, html.EscapeString(errParam))
			resultChan <- AuthResult{Error: errParam}
			return
		}

		if state != expectedState {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `<html><body><h1>Login Failed</h1><p>Invalid state parameter</p><p>You can close this window.</p></body></html>`)
			resultChan <- AuthResult{Error: "state mismatch"}
			return
		}

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><h1>Login Successful!</h1><p>You can close this window and return to the terminal.</p></body></html>`)
		resultChan <- AuthResult{Code: code, State: state}
	})

	server.Handler = mux

	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Debugf("callback server error: %v", err)
		}
	}()

	go func() {
		select {
		case <-ctx.Done():
		case <-time.After(authTimeout):
		case <-resultChan:
		}
		_ = server.Shutdown(context.Background())
	}()

	return redirectURI, resultChan, nil
}

// LoginWithBuilderID performs OAuth login with AWS Builder ID using device code flow.
func (o *KiroOAuth) LoginWithBuilderID(ctx context.Context) (*KiroTokenData, error) {
	ssoClient := NewSSOOIDCClient(o.cfg)
	return ssoClient.LoginWithBuilderID(ctx)
}

// LoginWithBuilderIDAuthCode performs OAuth login with AWS Builder ID using authorization code flow.
// This provides a better UX than device code flow as it uses automatic browser callback.
func (o *KiroOAuth) LoginWithBuilderIDAuthCode(ctx context.Context) (*KiroTokenData, error) {
	ssoClient := NewSSOOIDCClient(o.cfg)
	return ssoClient.LoginWithBuilderIDAuthCode(ctx)
}

// exchangeCodeForToken exchanges the authorization code for tokens.
func (o *KiroOAuth) exchangeCodeForToken(ctx context.Context, code, codeVerifier, redirectURI string) (*KiroTokenData, error) {
	payload := map[string]string{
		"code":          code,
		"code_verifier": codeVerifier,
		"redirect_uri":  redirectURI,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	tokenURL := kiroAuthEndpoint + "/oauth/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "KiroIDE-0.7.45-cli-proxy-api")

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("token exchange failed (status %d): %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("token exchange failed (status %d)", resp.StatusCode)
	}

	var tokenResp KiroTokenResponse
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	// Validate ExpiresIn - use default 1 hour if invalid
	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)

	return &KiroTokenData{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ProfileArn:   tokenResp.ProfileArn,
		ExpiresAt:    expiresAt.Format(time.RFC3339),
		AuthMethod:   "social",
		Provider:     "", // Caller should preserve original provider
		Region:       "us-east-1",
	}, nil
}

// RefreshToken refreshes an expired access token.
// Uses KiroIDE-style User-Agent to match official Kiro IDE behavior.
func (o *KiroOAuth) RefreshToken(ctx context.Context, refreshToken string) (*KiroTokenData, error) {
	return o.RefreshTokenWithFingerprint(ctx, refreshToken, "")
}

// RefreshTokenWithFingerprint refreshes an expired access token with a specific fingerprint.
// tokenKey is used to generate a consistent fingerprint for the token.
func (o *KiroOAuth) RefreshTokenWithFingerprint(ctx context.Context, refreshToken, tokenKey string) (*KiroTokenData, error) {
	payload := map[string]string{
		"refreshToken": refreshToken,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	refreshURL := kiroAuthEndpoint + "/refreshToken"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, refreshURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Use KiroIDE-style User-Agent to match official Kiro IDE behavior
	// This helps avoid 403 errors from server-side User-Agent validation
	userAgent := buildKiroUserAgent(tokenKey)
	req.Header.Set("User-Agent", userAgent)

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("token refresh failed (status %d): %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("token refresh failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var tokenResp KiroTokenResponse
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	// Validate ExpiresIn - use default 1 hour if invalid
	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)

	return &KiroTokenData{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ProfileArn:   tokenResp.ProfileArn,
		ExpiresAt:    expiresAt.Format(time.RFC3339),
		AuthMethod:   "social",
		Provider:     "", // Caller should preserve original provider
		Region:       "us-east-1",
	}, nil
}

// buildKiroUserAgent builds a KiroIDE-style User-Agent string.
// If tokenKey is provided, uses fingerprint manager for consistent fingerprint.
// Otherwise generates a simple KiroIDE User-Agent.
func buildKiroUserAgent(tokenKey string) string {
	if tokenKey != "" {
		fm := NewFingerprintManager()
		fp := fm.GetFingerprint(tokenKey)
		return fmt.Sprintf("KiroIDE-%s-%s", fp.KiroVersion, fp.KiroHash[:16])
	}
	// Default KiroIDE User-Agent matching kiro-openai-gateway format
	return "KiroIDE-0.7.45-cli-proxy-api"
}

// LoginWithGoogle performs OAuth login with Google using Kiro's social auth.
// This uses a custom protocol handler (kiro://) to receive the callback.
func (o *KiroOAuth) LoginWithGoogle(ctx context.Context) (*KiroTokenData, error) {
	socialClient := NewSocialAuthClient(o.cfg)
	return socialClient.LoginWithGoogle(ctx)
}

// LoginWithGitHub performs OAuth login with GitHub using Kiro's social auth.
// This uses a custom protocol handler (kiro://) to receive the callback.
func (o *KiroOAuth) LoginWithGitHub(ctx context.Context) (*KiroTokenData, error) {
	socialClient := NewSocialAuthClient(o.cfg)
	return socialClient.LoginWithGitHub(ctx)
}
