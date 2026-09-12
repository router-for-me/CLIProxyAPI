package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	metaauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/meta"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// MetaAuthenticator implements the Meta Muse OAuth device-code flow.
type MetaAuthenticator struct{}

// NewMetaAuthenticator constructs a new Meta authenticator.
func NewMetaAuthenticator() Authenticator {
	return &MetaAuthenticator{}
}

// Provider returns the provider key for Meta.
func (MetaAuthenticator) Provider() string {
	return "meta"
}

// RefreshLead instructs the manager on token refresh lead time.
// Meta credentials do not advertise an API key expiration, so scheduled refresh
// is disabled and recovery is triggered on demand (e.g. on 401 or request preparation).
func (MetaAuthenticator) RefreshLead() *time.Duration {
	return nil
}

// Login launches the OAuth device-code flow to obtain Meta tokens and persists them.
func (a MetaAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	authSvc := metaauth.NewMetaAuth(cfg)

	fmt.Println("Starting Meta (Muse) authentication...")
	deviceCode, err := authSvc.StartDeviceFlow(ctx)
	if err != nil {
		return nil, fmt.Errorf("meta: failed to start device flow: %w", err)
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
		return nil, fmt.Errorf("meta: %w", errWait)
	}

	tokenStorage := authSvc.CreateTokenStorage(bundle)
	if tokenStorage == nil || strings.TrimSpace(tokenStorage.AccessToken) == "" {
		return nil, fmt.Errorf("meta token storage missing access token")
	}

	fileName := metaauth.CredentialFileName(tokenStorage.Email, tokenStorage.DCAToken)
	label := strings.TrimSpace(tokenStorage.Email)
	if label == "" {
		label = "Meta"
	}

	metadata := map[string]any{
		"type":         "meta",
		"access_token": tokenStorage.AccessToken,
		"token_type":   tokenStorage.TokenType,
		"expires_in":   tokenStorage.ExpiresIn,
		"expired":      tokenStorage.Expired,
		"last_refresh": tokenStorage.LastRefresh,
		"base_url":     tokenStorage.BaseURL,
		"auth_kind":    "oauth",
	}
	if tokenStorage.DCAExpired != "" {
		metadata["dca_expired"] = tokenStorage.DCAExpired
	}
	if tokenStorage.DCAExpiresAt > 0 {
		metadata["dca_expires_at"] = tokenStorage.DCAExpiresAt
	}
	if tokenStorage.APIKey != "" {
		metadata["api_key"] = tokenStorage.APIKey
	}
	if tokenStorage.DCAToken != "" {
		metadata["dca_token"] = tokenStorage.DCAToken
	}
	if tokenStorage.Email != "" {
		metadata["email"] = tokenStorage.Email
	}
	if tokenStorage.Name != "" {
		metadata["name"] = tokenStorage.Name
	}

	attrs := map[string]string{
		"auth_kind": "oauth",
		"base_url":  tokenStorage.BaseURL,
	}
	if tokenStorage.APIKey != "" {
		attrs["api_key"] = tokenStorage.APIKey
	}
	if tokenStorage.DCAToken != "" {
		attrs["dca_token"] = tokenStorage.DCAToken
	}
	if tokenStorage.Email != "" {
		attrs["email"] = tokenStorage.Email
	}

	fmt.Println("Meta authentication successful")

	return &coreauth.Auth{
		ID:         fileName,
		Provider:   a.Provider(),
		FileName:   fileName,
		Label:      label,
		Storage:    tokenStorage,
		Metadata:   metadata,
		Attributes: attrs,
	}, nil
}
