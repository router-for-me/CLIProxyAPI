package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// GitHubCopilotAuthenticator implements the OAuth device flow login for GitHub Copilot.
type GitHubCopilotAuthenticator struct{}

// NewGitHubCopilotAuthenticator constructs a new GitHub Copilot authenticator.
func NewGitHubCopilotAuthenticator() Authenticator {
	return &GitHubCopilotAuthenticator{}
}

// Provider returns the provider key for github-copilot.
func (GitHubCopilotAuthenticator) Provider() string {
	return "github-copilot"
}

// RefreshLead returns nil since GitHub OAuth tokens don't expire in the traditional sense.
// The token remains valid until the user revokes it or the Copilot subscription expires.
func (GitHubCopilotAuthenticator) RefreshLead() *time.Duration {
	return nil
}

// Login initiates the GitHub device flow authentication for Copilot access.
func (a GitHubCopilotAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	authSvc := copilot.NewCopilotAuth(cfg)

	// Start the device flow
	fmt.Println("Starting GitHub Copilot authentication...")
	deviceCode, err := authSvc.StartDeviceFlow(ctx)
	if err != nil {
		return nil, fmt.Errorf("github-copilot: failed to start device flow: %w", err)
	}

	// Display the user code and verification URL
	fmt.Printf("\nTo authenticate, please visit: %s\n", deviceCode.VerificationURI)
	fmt.Printf("And enter the code: %s\n\n", deviceCode.UserCode)

	// Try to open the browser automatically
	if !opts.NoBrowser {
		if browser.IsAvailable() {
			if errOpen := browser.OpenURL(deviceCode.VerificationURI); errOpen != nil {
				log.Warnf("Failed to open browser automatically: %v", errOpen)
			}
		}
	}

	fmt.Println("Waiting for GitHub authorization...")
	fmt.Printf("(This will timeout in %d seconds if not authorized)\n", deviceCode.ExpiresIn)

	// Wait for user authorization
	authBundle, err := authSvc.WaitForAuthorization(ctx, deviceCode)
	if err != nil {
		errMsg := copilot.GetUserFriendlyMessage(err)
		return nil, fmt.Errorf("github-copilot: %s", errMsg)
	}

	// Verify the token can get a Copilot API token
	fmt.Println("Verifying Copilot access...")
	apiToken, err := authSvc.GetCopilotAPIToken(ctx, authBundle.TokenData.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("github-copilot: failed to verify Copilot access - you may not have an active Copilot subscription: %w", err)
	}

	// Create the token storage
	tokenStorage := authSvc.CreateTokenStorage(authBundle)

	// Build metadata with token information for the executor
	metadata := map[string]any{
		"type":         "github-copilot",
		"username":     authBundle.Username,
		"access_token": authBundle.TokenData.AccessToken,
		"token_type":   authBundle.TokenData.TokenType,
		"scope":        authBundle.TokenData.Scope,
		"timestamp":    time.Now().UnixMilli(),
	}

	if apiToken.ExpiresAt > 0 {
		metadata["api_token_expires_at"] = apiToken.ExpiresAt
	}

	fileName := fmt.Sprintf("github-copilot-%s.json", authBundle.Username)

	fmt.Printf("\nGitHub Copilot authentication successful for user: %s\n", authBundle.Username)

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    authBundle.Username,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}

// RefreshGitHubCopilotToken validates and returns the current token status.
// GitHub OAuth tokens don't need traditional refresh - we just validate they still work.
func RefreshGitHubCopilotToken(ctx context.Context, cfg *config.Config, storage *copilot.CopilotTokenStorage) error {
	if storage == nil || storage.AccessToken == "" {
		return fmt.Errorf("no token available")
	}

	authSvc := copilot.NewCopilotAuth(cfg)

	// Validate the token can still get a Copilot API token
	_, err := authSvc.GetCopilotAPIToken(ctx, storage.AccessToken)
	if err != nil {
		return fmt.Errorf("token validation failed: %w", err)
	}

	return nil
}
