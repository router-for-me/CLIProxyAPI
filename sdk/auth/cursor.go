package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// CursorAuthenticator implements the Cursor deep-link login flow.
//
// Cursor issues a long-lived access token and no refresh token, so there is no
// token refresh.
type CursorAuthenticator struct{}

// NewCursorAuthenticator constructs a new Cursor authenticator.
func NewCursorAuthenticator() Authenticator {
	return &CursorAuthenticator{}
}

// Provider returns the provider key for cursor.
func (CursorAuthenticator) Provider() string {
	return "cursor"
}

// RefreshLead returns nil because Cursor tokens do not refresh.
func (CursorAuthenticator) RefreshLead() *time.Duration {
	return nil
}

// Login initiates the Cursor deep-link authentication.
func (a CursorAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	authSvc := cursor.NewCursorAuth(cfg)

	fmt.Println("Starting Cursor authentication...")
	flow, err := authSvc.StartLoginFlow(ctx)
	if err != nil {
		return nil, fmt.Errorf("cursor: failed to start login flow: %w", err)
	}

	fmt.Printf("\nTo authenticate, please visit:\n%s\n\n", flow.LoginURL)
	if flow.ExpiresIn > 0 {
		fmt.Printf("(This will timeout in %d seconds if not authorized)\n", flow.ExpiresIn)
	}

	if !opts.NoBrowser {
		if browser.IsAvailable() {
			if errOpen := browser.OpenURL(flow.LoginURL); errOpen != nil {
				log.Warnf("Failed to open browser automatically: %v", errOpen)
			} else {
				fmt.Println("Browser opened automatically.")
			}
		}
	}

	fmt.Println("Waiting for authorization...")
	authBundle, err := authSvc.WaitForAuthorization(ctx, flow)
	if err != nil {
		return nil, fmt.Errorf("cursor: %w", err)
	}

	tokenStorage := authSvc.CreateTokenStorage(authBundle)
	label := authBundle.Label()

	metadata := map[string]any{
		"type":         "cursor",
		"access_token": authBundle.Login.AccessToken,
		"auth_id":      authBundle.Login.AuthID,
		"timestamp":    time.Now().UnixMilli(),
	}
	if authBundle.Login.Cookie != "" {
		metadata["cookie"] = authBundle.Login.Cookie
	}

	fileName := fmt.Sprintf("cursor-%d.json", time.Now().UnixMilli())

	fmt.Println("\nCursor authentication successful!")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Label:    label,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}
