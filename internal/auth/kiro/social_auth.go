// Package kiro provides social authentication (Google/GitHub) for Kiro via AuthServiceClient.
package kiro

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/oauthhttp"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
	"golang.org/x/term"
)

const (
	// Kiro AuthService endpoint
	kiroAuthServiceEndpoint = "https://prod.us-east-1.auth.desktop.kiro.dev"

	// OAuth timeout
	socialAuthTimeout = 10 * time.Minute
)

// SocialProvider represents the social login provider.
type SocialProvider string

const (
	// ProviderGoogle is Google OAuth provider
	ProviderGoogle SocialProvider = "Google"
	// ProviderGitHub is GitHub OAuth provider
	ProviderGitHub SocialProvider = "Github"
	// Note: AWS Builder ID is NOT supported by Kiro's auth service.
	// It only supports: Google, Github, Cognito
	// AWS Builder ID must use device code flow via SSO OIDC.
)

// CreateTokenRequest is sent to Kiro's /oauth/token endpoint.
type CreateTokenRequest struct {
	Code           string `json:"code"`
	CodeVerifier   string `json:"code_verifier"`
	RedirectURI    string `json:"redirect_uri"`
	InvitationCode string `json:"invitation_code,omitempty"`
}

// SocialTokenResponse from Kiro's /oauth/token endpoint for social auth.
type SocialTokenResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ProfileArn   string `json:"profileArn"`
	ExpiresIn    int    `json:"expiresIn"`
}

// RefreshTokenRequest is sent to Kiro's /refreshToken endpoint.
type RefreshTokenRequest struct {
	RefreshToken string `json:"refreshToken"`
}

// SocialAuthClient handles social authentication with Kiro.
type SocialAuthClient struct {
	httpClient      *http.Client
	cfg             *config.Config
	protocolHandler *ProtocolHandler
}

// NewSocialAuthClient creates a new social auth client.
func NewSocialAuthClient(cfg *config.Config) *SocialAuthClient {
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		client = util.SetOAuthProxy(&cfg.SDKConfig, client)
	}
	return &SocialAuthClient{
		httpClient:      client,
		cfg:             cfg,
		protocolHandler: NewProtocolHandler(),
	}
}

// generatePKCE generates PKCE code verifier and challenge.
func generatePKCE() (verifier, challenge string, err error) {
	// Generate 32 bytes of random data for verifier
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)

	// Generate SHA256 hash of verifier for challenge
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])

	return verifier, challenge, nil
}

// generateState generates a random state parameter.
func generateStateParam() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// buildLoginURL constructs the Kiro OAuth login URL.
// The login endpoint expects a GET request with query parameters.
// Format: /login?idp=Google&redirect_uri=...&code_challenge=...&code_challenge_method=S256&state=...&prompt=select_account
// The prompt=select_account parameter forces the account selection screen even if already logged in.
func (c *SocialAuthClient) buildLoginURL(provider, redirectURI, codeChallenge, state string) string {
	return fmt.Sprintf("%s/login?idp=%s&redirect_uri=%s&code_challenge=%s&code_challenge_method=S256&state=%s&prompt=select_account",
		kiroAuthServiceEndpoint,
		provider,
		url.QueryEscape(redirectURI),
		codeChallenge,
		state,
	)
}

// CreateToken exchanges the authorization code for tokens.
func (c *SocialAuthClient) CreateToken(ctx context.Context, req *CreateTokenRequest) (*SocialTokenResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal token request: %w", err)
	}

	tokenURL := kiroAuthServiceEndpoint + "/oauth/token"
	status, _, respBody, err := oauthhttp.Do(
		ctx,
		c.httpClient,
		func() (*http.Request, error) {
			httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(body))
			if err != nil {
				return nil, fmt.Errorf("failed to create token request: %w", err)
			}
			httpReq.Header.Set("Content-Type", "application/json")
			httpReq.Header.Set("Accept", "application/json")
			httpReq.Header.Set("User-Agent", "cli-proxy-api/1.0.0")
			return httpReq, nil
		},
		oauthhttp.DefaultRetryConfig(),
	)
	if err != nil && status == 0 {
		return nil, fmt.Errorf("token request failed: %w", err)
	}

	if status != http.StatusOK {
		log.Debugf("token exchange failed (status %d): %s", status, string(respBody))
		if err != nil {
			return nil, fmt.Errorf("token exchange failed (status %d): %w", status, err)
		}
		return nil, fmt.Errorf("token exchange failed (status %d)", status)
	}
	if err != nil {
		return nil, fmt.Errorf("token request failed: %w", err)
	}

	var tokenResp SocialTokenResponse
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	return &tokenResp, nil
}

// RefreshSocialToken refreshes an expired social auth token.
func (c *SocialAuthClient) RefreshSocialToken(ctx context.Context, refreshToken string) (*KiroTokenData, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	body, err := json.Marshal(&RefreshTokenRequest{RefreshToken: refreshToken})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal refresh request: %w", err)
	}

	refreshURL := kiroAuthServiceEndpoint + "/refreshToken"
	status, _, respBody, err := oauthhttp.Do(
		ctx,
		c.httpClient,
		func() (*http.Request, error) {
			httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, refreshURL, bytes.NewReader(body))
			if err != nil {
				return nil, fmt.Errorf("failed to create refresh request: %w", err)
			}
			httpReq.Header.Set("Content-Type", "application/json")
			httpReq.Header.Set("Accept", "application/json")
			httpReq.Header.Set("User-Agent", "cli-proxy-api/1.0.0")
			return httpReq, nil
		},
		oauthhttp.DefaultRetryConfig(),
	)
	if err != nil && status == 0 {
		return nil, fmt.Errorf("refresh request failed: %w", err)
	}

	if status != http.StatusOK {
		log.Debugf("token refresh failed (status %d): %s", status, string(respBody))
		if err != nil {
			return nil, fmt.Errorf("token refresh failed (status %d): %w", status, err)
		}
		return nil, fmt.Errorf("token refresh failed (status %d)", status)
	}
	if err != nil {
		return nil, fmt.Errorf("refresh request failed: %w", err)
	}

	var tokenResp SocialTokenResponse
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse refresh response: %w", err)
	}

	// Validate ExpiresIn - use default 1 hour if invalid
	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600 // Default 1 hour
	}
	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)

	return &KiroTokenData{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ProfileArn:   tokenResp.ProfileArn,
		ExpiresAt:    expiresAt.Format(time.RFC3339),
		AuthMethod:   "social",
		Provider:     "", // Caller should preserve original provider
	}, nil
}

// LoginWithSocial performs OAuth login with Google.
func (c *SocialAuthClient) LoginWithSocial(ctx context.Context, provider SocialProvider) (*KiroTokenData, error) {
	providerName := string(provider)

	fmt.Println("\n╔══════════════════════════════════════════════════════════╗")
	fmt.Printf("║         Kiro Authentication (%s)                    ║\n", providerName)
	fmt.Println("╚══════════════════════════════════════════════════════════╝")

	// Step 1: Setup protocol handler
	fmt.Println("\nSetting up authentication...")

	// Start the local callback server
	handlerPort, err := c.protocolHandler.Start(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to start callback server: %w", err)
	}
	defer c.protocolHandler.Stop()

	// Ensure protocol handler is installed and set as default
	if err := SetupProtocolHandlerIfNeeded(handlerPort); err != nil {
		fmt.Println("\n⚠ Protocol handler setup failed. Trying alternative method...")
		fmt.Println("  If you see a browser 'Open with' dialog, select your default browser.")
		fmt.Println("  For manual setup instructions, run: cliproxy kiro --help-protocol")
		log.Debugf("kiro: protocol handler setup error: %v", err)
		// Continue anyway - user might have set it up manually or select browser manually
	} else {
		// Force set our handler as default (prevents "Open with" dialog)
		forceDefaultProtocolHandler()
	}

	// Step 2: Generate PKCE codes
	codeVerifier, codeChallenge, err := generatePKCE()
	if err != nil {
		return nil, fmt.Errorf("failed to generate PKCE: %w", err)
	}

	// Step 3: Generate state
	state, err := generateStateParam()
	if err != nil {
		return nil, fmt.Errorf("failed to generate state: %w", err)
	}

	// Step 4: Build the login URL (Kiro uses GET request with query params)
	authURL := c.buildLoginURL(providerName, KiroRedirectURI, codeChallenge, state)

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

	// Step 5: Open browser for user authentication
	fmt.Println("\n════════════════════════════════════════════════════════════")
	fmt.Printf("  Opening browser for %s authentication...\n", providerName)
	fmt.Println("════════════════════════════════════════════════════════════")
	fmt.Printf("\n  URL: %s\n\n", authURL)

	if err := browser.OpenURL(authURL); err != nil {
		log.Warnf("Could not open browser automatically: %v", err)
		fmt.Println("  ⚠ Could not open browser automatically.")
		fmt.Println("  Please open the URL above in your browser manually.")
	} else {
		fmt.Println("  (Browser opened automatically)")
	}

	fmt.Println("\n  Waiting for authentication callback...")

	// Step 6: Wait for callback
	callback, err := c.protocolHandler.WaitForCallback(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to receive callback: %w", err)
	}

	if callback.Error != "" {
		return nil, fmt.Errorf("authentication error: %s", callback.Error)
	}

	if callback.State != state {
		// Log state values for debugging, but don't expose in user-facing error
		log.Debugf("kiro: OAuth state mismatch - expected %s, got %s", state, callback.State)
		return nil, fmt.Errorf("OAuth state validation failed - please try again")
	}

	if callback.Code == "" {
		return nil, fmt.Errorf("no authorization code received")
	}

	fmt.Println("\n✓ Authorization received!")

	// Step 7: Exchange code for tokens
	fmt.Println("Exchanging code for tokens...")

	tokenReq := &CreateTokenRequest{
		Code:         callback.Code,
		CodeVerifier: codeVerifier,
		RedirectURI:  KiroRedirectURI,
	}

	tokenResp, err := c.CreateToken(ctx, tokenReq)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange code for tokens: %w", err)
	}

	fmt.Println("\n✓ Authentication successful!")

	// Close the browser window
	if err := browser.CloseBrowser(); err != nil {
		log.Debugf("Failed to close browser: %v", err)
	}

	// Validate ExpiresIn - use default 1 hour if invalid
	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)

	// Try to extract email from JWT access token first
	email := ExtractEmailFromJWT(tokenResp.AccessToken)

	// If no email in JWT, ask user for account label (only in interactive mode)
	if email == "" && isInteractiveTerminal() {
		fmt.Print("\n  Enter account label for file naming (optional, press Enter to skip): ")
		reader := bufio.NewReader(os.Stdin)
		var err error
		email, err = reader.ReadString('\n')
		if err != nil {
			log.Debugf("Failed to read account label: %v", err)
		}
		email = strings.TrimSpace(email)
	}

	return &KiroTokenData{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ProfileArn:   tokenResp.ProfileArn,
		ExpiresAt:    expiresAt.Format(time.RFC3339),
		AuthMethod:   "social",
		Provider:     providerName,
		Email:        email, // JWT email or user-provided label
	}, nil
}

// LoginWithGoogle performs OAuth login with Google.
func (c *SocialAuthClient) LoginWithGoogle(ctx context.Context) (*KiroTokenData, error) {
	return c.LoginWithSocial(ctx, ProviderGoogle)
}

// LoginWithGitHub performs OAuth login with GitHub.
func (c *SocialAuthClient) LoginWithGitHub(ctx context.Context) (*KiroTokenData, error) {
	return c.LoginWithSocial(ctx, ProviderGitHub)
}

// forceDefaultProtocolHandler sets our protocol handler as the default for kiro:// URLs.
// This prevents the "Open with" dialog from appearing on Linux.
// On non-Linux platforms, this is a no-op as they use different mechanisms.
func forceDefaultProtocolHandler() {
	if runtime.GOOS != "linux" {
		return // Non-Linux platforms use different handler mechanisms
	}

	// Set our handler as default using xdg-mime
	cmd := exec.Command("xdg-mime", "default", "kiro-oauth-handler.desktop", "x-scheme-handler/kiro")
	if err := cmd.Run(); err != nil {
		log.Warnf("Failed to set default protocol handler: %v. You may see a handler selection dialog.", err)
	}
}

// isInteractiveTerminal checks if stdin is connected to an interactive terminal.
// Returns false in CI/automated environments or when stdin is piped.
func isInteractiveTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}
