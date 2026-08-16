package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	cursorauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

var cursorRefreshLead = 5 * time.Minute

// CursorAuthenticator implements Cursor's browser PKCE polling flow.
type CursorAuthenticator struct{}

func NewCursorAuthenticator() Authenticator { return &CursorAuthenticator{} }

func (CursorAuthenticator) Provider() string { return cursorauth.Provider }

func (CursorAuthenticator) RefreshLead() *time.Duration { return &cursorRefreshLead }

func (CursorAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	params, errParams := cursorauth.GenerateAuthParams()
	if errParams != nil {
		return nil, errParams
	}
	fmt.Printf("Open this URL to authenticate with Cursor:\n%s\n", params.LoginURL)
	if !opts.NoBrowser && browser.IsAvailable() {
		if errOpen := browser.OpenURL(params.LoginURL); errOpen != nil {
			log.WithError(errOpen).Warn("cursor oauth: failed to open browser")
		}
	}

	loginCtx, cancelLogin := context.WithTimeout(ctx, time.Duration(cursorauth.LoginTimeout)*time.Second)
	defer cancelLogin()
	client := cursorauth.NewClient(cfg, "")
	tokens, errPoll := client.Poll(loginCtx, params.UUID, params.Verifier)
	if errPoll != nil {
		return nil, errPoll
	}
	models, errModels := client.DiscoverModels(loginCtx, tokens.AccessToken)
	if errModels != nil {
		return nil, fmt.Errorf("cursor login: initial model discovery failed: %w", errModels)
	}

	now := time.Now().UTC()
	expired := tokens.ExpiresAt.UTC().Format(time.RFC3339)
	metadata := map[string]any{
		"type":                   cursorauth.Provider,
		"auth_kind":              cursorauth.OAuthKind,
		"access_token":           tokens.AccessToken,
		"refresh_token":          tokens.RefreshToken,
		"expired":                expired,
		"last_refresh":           now.Format(time.RFC3339),
		cursorauth.ModelCacheKey: models,
	}
	email := cursorauth.JWTEmail(tokens.AccessToken)
	if email != "" {
		metadata["email"] = email
	}
	fileName := cursorauth.CredentialFileName(tokens.AccessToken)
	label := strings.TrimSpace(email)
	if label == "" {
		label = "Cursor User"
	}
	storage := &cursorauth.TokenStorage{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		Expired:      expired,
		LastRefresh:  now.Format(time.RFC3339),
		AuthKind:     cursorauth.OAuthKind,
		Type:         cursorauth.Provider,
		Models:       models,
	}
	fmt.Printf("Cursor authentication successful; discovered %d models.\n", len(models))
	return &coreauth.Auth{
		ID:         fileName,
		Provider:   cursorauth.Provider,
		FileName:   fileName,
		Label:      label,
		Storage:    storage,
		Metadata:   metadata,
		Attributes: map[string]string{"auth_kind": cursorauth.OAuthKind},
	}, nil
}
