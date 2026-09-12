package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// XAIAuthenticator implements the xAI Grok OAuth device-code flow.
type XAIAuthenticator struct{}

// NewXAIAuthenticator constructs a new xAI authenticator.
func NewXAIAuthenticator() Authenticator {
	return &XAIAuthenticator{}
}

// Provider returns the provider key for xAI.
func (XAIAuthenticator) Provider() string {
	return "xai"
}

// RefreshLead instructs the manager to refresh before token expiry.
func (XAIAuthenticator) RefreshLead() *time.Duration {
	lead := xaiauth.RefreshLead()
	return &lead
}

// Login launches the OAuth device-code flow to obtain xAI tokens and persists them.
func (a XAIAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	authSvc := xaiauth.NewXAIAuth(cfg)

	fmt.Println("Starting xAI authentication...")
	deviceCode, err := authSvc.StartDeviceFlow(ctx)
	if err != nil {
		return nil, fmt.Errorf("xai: failed to start device flow: %w", err)
	}

	verificationURL := strings.TrimSpace(deviceCode.VerificationURIComplete)
	if verificationURL == "" {
		verificationURL = strings.TrimSpace(deviceCode.VerificationURI)
	}

	fmt.Printf("\nTo authenticate, please visit:\n%s\n\n", verificationURL)
	if deviceCode.UserCode != "" {
		fmt.Printf("Then enter this code: %s\n\n", deviceCode.UserCode)
	}

	if !opts.NoBrowser {
		if browser.IsAvailable() {
			if errOpen := browser.OpenURL(verificationURL); errOpen != nil {
				log.Warnf("Failed to open browser automatically: %v", errOpen)
			} else {
				fmt.Println("Browser opened automatically.")
			}
		} else {
			log.Warn("No browser available; please open the URL manually")
		}
	}

	fmt.Println("Waiting for authorization...")
	if deviceCode.ExpiresIn > 0 {
		fmt.Printf("(This will timeout in %d seconds if not authorized)\n", deviceCode.ExpiresIn)
	}

	bundle, errWait := authSvc.WaitForAuthorization(ctx, deviceCode)
	if errWait != nil {
		return nil, fmt.Errorf("xai: %w", errWait)
	}

	tokenStorage := authSvc.CreateTokenStorage(bundle)
	if tokenStorage == nil || strings.TrimSpace(tokenStorage.AccessToken) == "" {
		return nil, fmt.Errorf("xai token storage missing access token")
	}

	fileName := xaiauth.CredentialFileName(tokenStorage.Email, tokenStorage.Subject)
	label := strings.TrimSpace(tokenStorage.Email)
	if label == "" {
		label = "xAI"
	}

	metadata := map[string]any{
		"type":           "xai",
		"access_token":   tokenStorage.AccessToken,
		"refresh_token":  tokenStorage.RefreshToken,
		"id_token":       tokenStorage.IDToken,
		"token_type":     tokenStorage.TokenType,
		"expires_in":     tokenStorage.ExpiresIn,
		"expired":        tokenStorage.Expire,
		"last_refresh":   tokenStorage.LastRefresh,
		"base_url":       tokenStorage.BaseURL,
		"token_endpoint": tokenStorage.TokenEndpoint,
		"auth_kind":      "oauth",
	}
	if tokenStorage.Email != "" {
		metadata["email"] = tokenStorage.Email
	}
	if tokenStorage.Subject != "" {
		metadata["sub"] = tokenStorage.Subject
	}

	fmt.Println("xAI authentication successful")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    label,
		Storage:  tokenStorage,
		Metadata: metadata,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  tokenStorage.BaseURL,
		},
	}, nil
}
