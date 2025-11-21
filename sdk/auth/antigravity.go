package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// AntigravityAuthenticator implements the login flow for Google Antigravity accounts.
type AntigravityAuthenticator struct{}

// NewAntigravityAuthenticator constructs a new Antigravity authenticator.
func NewAntigravityAuthenticator() *AntigravityAuthenticator {
	return &AntigravityAuthenticator{}
}

func (a *AntigravityAuthenticator) Provider() string {
	return "antigravity"
}

func (a *AntigravityAuthenticator) RefreshLead() *time.Duration {
	// Return 5 minutes refresh lead time for antigravity tokens
	lead := 5 * time.Minute
	return &lead
}

func (a *AntigravityAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	antigravityAuth := antigravity.NewAntigravityAuth(cfg)

	tokenData, err := antigravityAuth.StartOAuthFlow(ctx, opts.NoBrowser)
	if err != nil {
		return nil, fmt.Errorf("antigravity authentication failed: %w", err)
	}

	tokenStorage := antigravityAuth.CreateTokenStorage(tokenData)

	fileName := fmt.Sprintf("antigravity-%s.json", tokenStorage.Email)

	metadata := map[string]any{
		"email":              tokenStorage.Email,
		"is_google_internal": tokenStorage.IsGoogleInternal,
		"last_refresh":       tokenStorage.LastRefresh,
		"expire":             tokenStorage.Expire,
	}

	fmt.Printf("Google Antigravity authentication successful! User: %s\n", tokenStorage.Email)
	if tokenStorage.IsGoogleInternal {
		fmt.Println("Google internal user detected - special features enabled")
	}

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}
