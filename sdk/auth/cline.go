/**
 * @file SDK authenticator for Cline provider
 * @description Implements the Authenticator interface for Cline authentication. Unlike traditional
 * OAuth flows with browser-based authorization, Cline uses a simpler approach where users export
 * a refresh token from the VSCode extension and provide it directly. This authenticator handles
 * the token exchange and storage process.
 */

package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/cline"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// ClineAuthenticator implements the authentication flow for Cline accounts.
// It uses a refresh token obtained from the Cline VSCode extension to generate
// JWT access tokens for API authentication.
type ClineAuthenticator struct{}

// NewClineAuthenticator constructs a Cline authenticator.
func NewClineAuthenticator() *ClineAuthenticator {
	return &ClineAuthenticator{}
}

// Provider returns the provider identifier for this authenticator.
func (a *ClineAuthenticator) Provider() string {
	return "cline"
}

// RefreshLead returns the recommended time before token expiration to trigger a refresh.
// Cline tokens typically expire in 10 minutes, so we refresh 2 minutes before expiration.
func (a *ClineAuthenticator) RefreshLead() *time.Duration {
	d := 2 * time.Minute
	return &d
}

// Login performs the Cline authentication flow.
// This method prompts the user for a refresh token (obtained from VSCode extension),
// exchanges it for an access token, and stores the credentials.
//
// Parameters:
//   - ctx: The context for the operation
//   - cfg: The application configuration
//   - opts: Login options including metadata and prompt function
//
// Returns:
//   - *coreauth.Auth: The authentication record with token storage
//   - error: An error if authentication fails
func (a *ClineAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	// Get refresh token from metadata or prompt
	refreshToken := ""
	if opts.Metadata != nil {
		refreshToken = opts.Metadata["refresh_token"]
	}

	if refreshToken == "" && opts.Prompt != nil {
		fmt.Println("\nTo authenticate with Cline:")
		fmt.Println("1. Open VS Code with Cline extension installed")
		fmt.Println("2. Press Ctrl+Shift+P (Cmd+Shift+P on Mac)")
		fmt.Println("3. Run command: 'Cline: Export Auth Token'")
		fmt.Println("4. The refresh token will be copied to your clipboard")
		fmt.Println()

		var err error
		refreshToken, err = opts.Prompt("Please paste your Cline refresh token:")
		if err != nil {
			return nil, err
		}
	}

	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is required for Cline authentication")
	}

	// Exchange refresh token for access token
	authSvc := cline.NewClineAuth(cfg)
	tokenData, err := authSvc.RefreshTokens(ctx, refreshToken)
	if err != nil {
		return nil, fmt.Errorf("cline token exchange failed: %w", err)
	}

	// Create token storage
	tokenStorage := authSvc.CreateTokenStorage(tokenData)

	// Get email from metadata or prompt
	email := tokenData.Email
	if email == "" && opts.Metadata != nil {
		email = opts.Metadata["email"]
		if email == "" {
			email = opts.Metadata["alias"]
		}
	}

	if email == "" && opts.Prompt != nil {
		email, err = opts.Prompt("Please input your email address or alias for Cline:")
		if err != nil {
			return nil, err
		}
	}

	email = strings.TrimSpace(email)
	if email == "" {
		return nil, &EmailRequiredError{Prompt: "Please provide an email address or alias for Cline."}
	}

	tokenStorage.Email = email

	fileName := fmt.Sprintf("cline-%s.json", tokenStorage.Email)
	metadata := map[string]any{
		"email": tokenStorage.Email,
	}

	fmt.Println("Cline authentication successful")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}
