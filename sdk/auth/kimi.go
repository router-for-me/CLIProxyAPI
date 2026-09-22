package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kimi"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// kimiRefreshLead is the duration before token expiry when refresh should occur.
var kimiRefreshLead = 5 * time.Minute

// KimiAuthenticator implements the OAuth device flow login for Kimi (Moonshot AI).
type KimiAuthenticator struct {
	domain   string
	provider string
}

// NewKimiAuthenticator constructs a new Kimi authenticator for kimi.com.
func NewKimiAuthenticator() Authenticator {
	return &KimiAuthenticator{domain: kimi.KimiDefaultDomain, provider: "kimi"}
}

// NewKimiAIAuthenticator constructs a new Kimi authenticator for kimi.ai with provider "kimi-ai".
func NewKimiAIAuthenticator() Authenticator {
	return &KimiAuthenticator{domain: kimi.KimiAIDomain, provider: "kimi-ai"}
}

// NewKimiAIDotAuthenticator constructs a new Kimi authenticator for kimi.ai with provider "kimi.ai".
func NewKimiAIDotAuthenticator() Authenticator {
	return &KimiAuthenticator{domain: kimi.KimiAIDomain, provider: "kimi.ai"}
}

// Provider returns the provider key for kimi.
func (a KimiAuthenticator) Provider() string {
	if a.provider != "" {
		return a.provider
	}
	if kimi.IsKimiAIDomain(a.domain) {
		return "kimi-ai"
	}
	return "kimi"
}

// RefreshLead returns the duration before token expiry when refresh should occur.
// Kimi tokens expire and need to be refreshed before expiry.
func (KimiAuthenticator) RefreshLead() *time.Duration {
	return &kimiRefreshLead
}

// Login initiates the Kimi device flow authentication.
func (a KimiAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	domain := a.domain
	if domain == "" {
		domain = kimi.KimiDefaultDomain
	}
	isAI := kimi.IsKimiAIDomain(domain)
	displayName := "Kimi"
	providerKey := a.Provider()
	filePrefix := "kimi"
	baseURL := kimi.KimiAPIBaseURL
	if isAI {
		displayName = "Kimi.ai"
		filePrefix = "kimi-ai"
		baseURL = kimi.KimiAIAPIBaseURL
	}

	authSvc := kimi.NewKimiAuthWithDomain(cfg, domain)

	// Start the device flow
	fmt.Printf("Starting %s authentication...\n", displayName)
	deviceCode, err := authSvc.StartDeviceFlow(ctx)
	if err != nil {
		return nil, fmt.Errorf("kimi: failed to start device flow: %w", err)
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

	// Wait for user authorization
	authBundle, err := authSvc.WaitForAuthorization(ctx, deviceCode)
	if err != nil {
		return nil, fmt.Errorf("kimi: %w", err)
	}

	// Create the token storage
	tokenStorage := authSvc.CreateTokenStorage(authBundle)
	if isAI {
		tokenStorage.Type = providerKey
	}

	// Build metadata with token information
	metadata := map[string]any{
		"type":          providerKey,
		"access_token":  authBundle.TokenData.AccessToken,
		"refresh_token": authBundle.TokenData.RefreshToken,
		"token_type":    authBundle.TokenData.TokenType,
		"scope":         authBundle.TokenData.Scope,
		"timestamp":     time.Now().UnixMilli(),
		"domain":        domain,
		"base_url":      baseURL,
	}

	if authBundle.TokenData.ExpiresAt > 0 {
		exp := time.Unix(authBundle.TokenData.ExpiresAt, 0).UTC().Format(time.RFC3339)
		metadata["expired"] = exp
	}
	if strings.TrimSpace(authBundle.DeviceID) != "" {
		metadata["device_id"] = strings.TrimSpace(authBundle.DeviceID)
	}

	// Generate a unique filename
	fileName := fmt.Sprintf("%s-%d.json", filePrefix, time.Now().UnixMilli())

	fmt.Printf("\n%s authentication successful!\n", displayName)

	return &coreauth.Auth{
		ID:       fileName,
		Provider: providerKey,
		FileName: fileName,
		Label:    fmt.Sprintf("%s User", displayName),
		Storage:  tokenStorage,
		Metadata: metadata,
		Attributes: map[string]string{
			"base_url": baseURL,
			"domain":   domain,
		},
	}, nil
}
