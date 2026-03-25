package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codebuddy"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// CodeBuddyAuthenticator implements the browser OAuth polling flow for CodeBuddy.
type CodeBuddyAuthenticator struct{}

// NewCodeBuddyAuthenticator constructs a new CodeBuddy authenticator.
func NewCodeBuddyAuthenticator() Authenticator {
	return &CodeBuddyAuthenticator{}
}

// Provider returns the provider key for codebuddy.
func (CodeBuddyAuthenticator) Provider() string {
	return "codebuddy"
}

// codeBuddyRefreshLead is the duration before token expiry when a refresh should be attempted.
var codeBuddyRefreshLead = 24 * time.Hour

// RefreshLead returns how soon before expiry a refresh should be attempted.
// CodeBuddy tokens have a long validity period, so we refresh 24 hours before expiry.
func (CodeBuddyAuthenticator) RefreshLead() *time.Duration {
	return &codeBuddyRefreshLead
}

// Login initiates the browser OAuth flow for CodeBuddy.
func (a CodeBuddyAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("codebuddy: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	authSvc := codebuddy.NewCodeBuddyAuth(cfg)

	authState, err := authSvc.FetchAuthState(ctx)
	if err != nil {
		return nil, fmt.Errorf("codebuddy: failed to fetch auth state: %w", err)
	}

	fmt.Printf("\nPlease open the following URL in your browser to login:\n\n  %s\n\n", authState.AuthURL)
	fmt.Println("Waiting for authorization...")

	if !opts.NoBrowser {
		if browser.IsAvailable() {
			if errOpen := browser.OpenURL(authState.AuthURL); errOpen != nil {
				log.Debugf("codebuddy: failed to open browser: %v", errOpen)
			}
		}
	}

	storage, err := authSvc.PollForToken(ctx, authState.State)
	if err != nil {
		return nil, fmt.Errorf("codebuddy: %s: %w", codebuddy.GetUserFriendlyMessage(err), err)
	}

	fmt.Printf("\nSuccessfully logged in! (User ID: %s)\n", storage.UserID)

	authID := fmt.Sprintf("codebuddy-%s.json", storage.UserID)

	label := storage.UserID
	if label == "" {
		label = "codebuddy-user"
	}

	return &coreauth.Auth{
		ID:       authID,
		Provider: a.Provider(),
		FileName: authID,
		Label:    label,
		Storage:  storage,
		Metadata: map[string]any{
			"access_token":  storage.AccessToken,
			"refresh_token": storage.RefreshToken,
			"user_id":       storage.UserID,
			"domain":        storage.Domain,
			"expires_in":    storage.ExpiresIn,
		},
	}, nil
}
