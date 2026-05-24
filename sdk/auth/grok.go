package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/grok"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// grokRefreshLead is how far before access-token expiry we proactively refresh.
// Matches grok.AccessTokenRefreshSkew (120s).
var grokRefreshLead = grok.AccessTokenRefreshSkew

// GrokAuthenticator drives the xAI SuperGrok OAuth flow (browser loopback +
// device-code fallback) and persists tokens for downstream use by the Grok
// executor.
type GrokAuthenticator struct{}

// NewGrokAuthenticator constructs a fresh authenticator for use with the SDK
// manager.
func NewGrokAuthenticator() *GrokAuthenticator { return &GrokAuthenticator{} }

// Provider reports the canonical provider key for routing and storage.
func (a *GrokAuthenticator) Provider() string { return "grok" }

// RefreshLead returns how far before access-token expiry we should proactively
// refresh. Matches grok.AccessTokenRefreshSkew (120s).
func (a *GrokAuthenticator) RefreshLead() *time.Duration {
	return &grokRefreshLead
}

// Login drives the SuperGrok OAuth flow. Uses loopback browser flow by
// default; falls back to RFC 8628 device-code when browser.IsAvailable() is
// false OR when port 56121 is occupied (xAI rejects any other redirect port
// for the shared Grok-CLI client, so device-code is the only honest fallback).
//
// opts.NoBrowser forces the device-code path immediately without probing
// browser availability.
func (a *GrokAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	// Log and ignore any caller-supplied callback port override — port 56121 is
	// registered with xAI for the shared Grok-CLI client and cannot be changed.
	if opts.CallbackPort > 0 && opts.CallbackPort != grok.OAuthCallbackPort {
		log.Warnf("grok: ignoring CallbackPort override (%d); port %d is xAI-registered and cannot be changed",
			opts.CallbackPort, grok.OAuthCallbackPort)
	}

	// Decide browser vs device-code path.
	if opts.NoBrowser {
		return a.loginWithDeviceCode(ctx, cfg)
	}

	// Attempt browser / loopback flow. Fall back to device-code on port-in-use.
	result, err := a.loginWithBrowser(ctx, cfg)
	if err != nil {
		if grok.IsAuthenticationError(err) {
			authErr := err.(*grok.AuthenticationError)
			if authErr.Type == grok.ErrPortInUse.Type {
				log.Warn("grok: loopback port 56121 is in use; falling back to device-code flow")
				return a.loginWithDeviceCode(ctx, cfg)
			}
		}
		return nil, err
	}
	return result, nil
}

// loginWithBrowser runs the authorization-code + PKCE loopback flow.
func (a *GrokAuthenticator) loginWithBrowser(ctx context.Context, cfg *config.Config) (*coreauth.Auth, error) {
	pkce, err := grok.GeneratePKCECodes()
	if err != nil {
		return nil, fmt.Errorf("grok pkce generation failed: %w", err)
	}

	state, err := misc.GenerateRandomState()
	if err != nil {
		return nil, fmt.Errorf("grok state generation failed: %w", err)
	}

	// Generate a nonce (reuse state generator — same entropy requirements).
	nonce, err := misc.GenerateRandomState()
	if err != nil {
		return nil, fmt.Errorf("grok nonce generation failed: %w", err)
	}

	authSvc := grok.NewGrokAuth(cfg)

	authURL, err := authSvc.GenerateAuthURL(state, nonce, pkce)
	if err != nil {
		return nil, fmt.Errorf("grok authorization url generation failed: %w", err)
	}

	oauthServer := grok.NewOAuthServer()
	if err = oauthServer.Start(); err != nil {
		return nil, err // already typed as AuthenticationError(ErrPortInUse, ...)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if stopErr := oauthServer.Stop(stopCtx); stopErr != nil {
			log.Warnf("grok oauth server stop error: %v", stopErr)
		}
	}()

	fmt.Println("Opening browser for Grok (xAI) authentication")
	if !browser.IsAvailable() {
		log.Warn("No browser available; please open the URL manually")
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	} else if err = browser.OpenURL(authURL); err != nil {
		log.Warnf("Failed to open browser automatically: %v", err)
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	}

	fmt.Println("Waiting for Grok authentication callback...")

	result, err := oauthServer.WaitForCallback(5 * time.Minute)
	if err != nil {
		return nil, err
	}

	if result.Error != "" {
		return nil, &grok.OAuthError{Code: result.Error}
	}

	if result.State != state {
		return nil, grok.NewAuthenticationError(grok.ErrInvalidState, fmt.Errorf("state mismatch"))
	}

	log.Debug("Grok authorization code received; exchanging for tokens")

	tokenResp, err := authSvc.ExchangeCodeForTokens(ctx, result.Code, pkce)
	if err != nil {
		return nil, grok.NewAuthenticationError(grok.ErrCodeExchangeFailed, err)
	}

	return a.buildAuthRecord(tokenResp, cfg)
}

// loginWithDeviceCode runs the RFC 8628 device-authorization flow.
func (a *GrokAuthenticator) loginWithDeviceCode(ctx context.Context, cfg *config.Config) (*coreauth.Auth, error) {
	authSvc := grok.NewGrokAuth(cfg)

	fmt.Println("Starting Grok device-code authentication...")

	deviceResp, err := authSvc.RequestDeviceCode(ctx)
	if err != nil {
		return nil, fmt.Errorf("grok: device code request failed: %w", err)
	}

	verificationURI := deviceResp.VerificationURIComplete
	if strings.TrimSpace(verificationURI) == "" {
		verificationURI = deviceResp.VerificationURI
	}

	fmt.Printf("\nTo authenticate, visit:\n  %s\n", verificationURI)
	fmt.Printf("User code: %s\n\n", deviceResp.UserCode)
	fmt.Println("Waiting for authorization...")

	tokenResp, err := authSvc.PollDeviceCodeToken(ctx, deviceResp, grok.DeviceCodePollOptions{})
	if err != nil {
		return nil, fmt.Errorf("grok: device authorization failed: %w", err)
	}

	return a.buildAuthRecord(tokenResp, cfg)
}

// buildAuthRecord converts a TokenResponse into a coreauth.Auth. Persistence is
// handled by the configured auth store so --config auth-dir is respected.
func (a *GrokAuthenticator) buildAuthRecord(tok *grok.TokenResponse, cfg *config.Config) (*coreauth.Auth, error) {
	_ = cfg // reserved for future proxy / path config

	storage := &grok.GrokTokenStorage{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		IDToken:      tok.IDToken,
		LastRefresh:  time.Now().UTC().Format(time.RFC3339),
	}
	if tok.ExpiresIn > 0 {
		storage.Expire = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}

	// Use a stable filename. Without an email in the token response we derive
	// a name from the current Unix timestamp, matching the kimi convention.
	fileName := fmt.Sprintf("grok-%d.json", time.Now().UnixMilli())
	if storage.Email != "" {
		fileName = fmt.Sprintf("grok-%s.json", storage.Email)
	}

	metadata := map[string]any{
		"type":          "grok",
		"access_token":  tok.AccessToken,
		"refresh_token": tok.RefreshToken,
	}
	if tok.ExpiresIn > 0 {
		metadata["expired"] = storage.Expire
	}

	fmt.Println("Grok authentication successful")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  storage,
		Metadata: metadata,
	}, nil
}
