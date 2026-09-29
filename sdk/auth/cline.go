package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cline"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// clineRefreshLead is the duration before token expiry when refresh should occur.
var clineRefreshLead = 5 * time.Minute

// ClineAuthenticator implements the WorkOS device flow login for Cline.
type ClineAuthenticator struct{}

// NewClineAuthenticator constructs a new Cline authenticator.
func NewClineAuthenticator() Authenticator {
	return &ClineAuthenticator{}
}

// Provider returns the provider key for cline.
func (a ClineAuthenticator) Provider() string { return "cline" }

// RefreshLead returns the duration before token expiry when refresh should occur.
// Cline tokens expire and the refresh token rotates on every refresh.
func (ClineAuthenticator) RefreshLead() *time.Duration {
	return &clineRefreshLead
}

// Login initiates the Cline device flow authentication.
func (a ClineAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	authSvc := cline.NewClineAuth(cfg)

	// Start the device flow
	fmt.Println("Starting Cline authentication...")
	deviceCode, err := authSvc.StartDeviceFlow(ctx)
	if err != nil {
		return nil, fmt.Errorf("cline: failed to start device flow: %w", err)
	}

	// Display the verification URL
	verificationURL := deviceCode.VerificationURIComplete
	if verificationURL == "" {
		verificationURL = deviceCode.VerificationURI
	}

	fmt.Printf("\nTo authenticate, please visit:\n%s\n\n", verificationURL)
	if deviceCode.UserCode != "" {
		fmt.Printf("User code: %s\n\n", deviceCode.UserCode)
	}

	// Try to open the browser automatically
	if !opts.NoBrowser {
		if browser.IsAvailable() {
			if errOpen := browser.OpenURL(verificationURL); errOpen != nil {
				log.Warnf("Failed to open browser automatically: %v", errOpen)
			} else {
				fmt.Println("Browser opened automatically.")
			}
		}
	}

	fmt.Println("Waiting for authorization...")
	if deviceCode.ExpiresIn > 0 {
		fmt.Printf("(This will timeout in %d seconds if not authorized)\n", deviceCode.ExpiresIn)
	}

	// Wait for user authorization and exchange for Cline account tokens
	authBundle, err := authSvc.WaitForAuthorization(ctx, deviceCode)
	if err != nil {
		return nil, fmt.Errorf("cline: %w", err)
	}

	// Create the token storage
	tokenStorage := authSvc.CreateTokenStorage(authBundle)
	if tokenStorage == nil {
		return nil, fmt.Errorf("cline: empty token record")
	}

	// Build metadata with token information
	metadata := map[string]any{
		"type":          "cline",
		"access_token":  tokenStorage.AccessToken,
		"refresh_token": tokenStorage.RefreshToken,
		"token_type":    tokenStorage.TokenType,
		"timestamp":     time.Now().UnixMilli(),
		"expired":       tokenStorage.Expired,
		"last_refresh":  tokenStorage.LastRefresh,
		"base_url":      tokenStorage.BaseURL,
		"auth_kind":     "oauth",
	}
	if tokenStorage.Email != "" {
		metadata["email"] = tokenStorage.Email
	}
	if tokenStorage.Name != "" {
		metadata["name"] = tokenStorage.Name
	}
	if tokenStorage.Subject != "" {
		metadata["sub"] = tokenStorage.Subject
	}
	// Persist the detected models in metadata: the key's presence marks
	// detection as done even when the account exposes no models. On fetch
	// failure the marker is left out so the lazy first-import path retries.
	if authBundle != nil && authBundle.ModelsDetected {
		metadata[cline.ModelsMetadataKey] = authBundle.Models
	}

	fileName := cline.CredentialFileName(tokenStorage.Email, tokenStorage.Subject)
	label := strings.TrimSpace(tokenStorage.Email)
	if label == "" {
		label = "Cline"
	}

	fmt.Println("\nCline authentication successful!")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: "cline",
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
