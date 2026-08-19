package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	zaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/zai"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// ZAIAuthenticator implements the ZCode CLI OAuth login for Z.AI / BigModel
// coding plans. The minted token targets an Anthropic-compatible endpoint, so no
// API key is required.
type ZAIAuthenticator struct{}

// NewZAIAuthenticator constructs a new Z.AI authenticator.
func NewZAIAuthenticator() Authenticator {
	return &ZAIAuthenticator{}
}

// Provider returns the provider key for Z.AI.
func (ZAIAuthenticator) Provider() string {
	return "zai"
}

// RefreshLead returns nil: the coding-plan token is long-lived and the flow does
// not issue a refresh token, so there is no proactive refresh. When the token is
// rejected the user re-runs the login.
func (ZAIAuthenticator) RefreshLead() *time.Duration {
	return nil
}

// Login runs the ZCode CLI OAuth flow for the selected identity provider.
//
// The identity provider is taken from opts.Metadata["provider"] and may be "zai"
// (international) or "bigmodel" (China mainland). It defaults to "zai".
func (a ZAIAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	provider := zaiauth.ProviderZAI
	if opts.Metadata != nil {
		if v := strings.TrimSpace(opts.Metadata["provider"]); v != "" {
			provider = v
		}
	}
	provider = zaiauth.NormalizeProvider(provider)

	authSvc := zaiauth.NewZAIAuth(cfg, provider, "", opts.CallbackPort)

	fmt.Printf("Starting Z.AI authentication (provider: %s)...\n", provider)
	init, err := authSvc.StartFlow(ctx)
	if err != nil {
		return nil, fmt.Errorf("zai: failed to start authentication: %w", err)
	}

	fmt.Printf("\nTo authenticate, please visit:\n%s\n\n", init.AuthorizeURL)

	if !opts.NoBrowser {
		if browser.IsAvailable() {
			if errOpen := browser.OpenURL(init.AuthorizeURL); errOpen != nil {
				log.Warnf("Failed to open browser automatically: %v", errOpen)
			} else {
				fmt.Println("Browser opened automatically.")
			}
		}
	}

	fmt.Println("Waiting for authorization...")
	authResult, err := authSvc.WaitForAuthorization(ctx, init)
	if err != nil {
		return nil, fmt.Errorf("zai: %w", err)
	}

	// Exchange the OAuth login for a standard coding-plan API key (the same
	// provisioning the official client performs). The minted key targets the
	// standard Anthropic-compatible endpoint, avoiding the ZCode-only coding-plan
	// captcha.
	fmt.Println("Provisioning coding-plan API key...")
	apiKey, baseURL, err := authSvc.MintAPIKey(ctx, authResult)
	if err != nil {
		return nil, fmt.Errorf("zai: failed to provision API key: %w", err)
	}

	tokenStorage := authSvc.CreateTokenStorage(authResult, apiKey, baseURL)

	metadata := map[string]any{
		"type":         "zai",
		"provider":     provider,
		"access_token": apiKey,
		"base_url":     baseURL,
		"timestamp":    time.Now().UnixMilli(),
	}
	if strings.TrimSpace(authResult.ZAIAccessToken) != "" {
		metadata["zai_access_token"] = authResult.ZAIAccessToken
	}
	if strings.TrimSpace(authResult.Email) != "" {
		metadata["email"] = authResult.Email
	}
	if strings.TrimSpace(authResult.Name) != "" {
		metadata["name"] = authResult.Name
	}
	if strings.TrimSpace(authResult.UserID) != "" {
		metadata["user_id"] = authResult.UserID
	}

	fileName := zaiauth.CredentialFileName(provider, authResult.UserID, authResult.Email)

	label := strings.TrimSpace(authResult.Email)
	if label == "" {
		label = strings.TrimSpace(authResult.Name)
	}
	if label == "" {
		label = fmt.Sprintf("Z.AI (%s)", provider)
	}

	fmt.Println("\nZ.AI authentication successful!")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    label,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}
