package codex

import (
	"context"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/oauthflow"
)

// OAuthProvider adapts CodexAuth to the shared oauthflow.ProviderOAuth interface.
type OAuthProvider struct {
	auth *CodexAuth
}

func NewOAuthProvider(auth *CodexAuth) *OAuthProvider {
	return &OAuthProvider{auth: auth}
}

func (p *OAuthProvider) Provider() string {
	return "codex"
}

func (p *OAuthProvider) AuthorizeURL(session oauthflow.OAuthSession) (string, oauthflow.OAuthSession, error) {
	if p == nil || p.auth == nil {
		return "", session, fmt.Errorf("codex oauth provider: auth is nil")
	}
	pkce := &PKCECodes{
		CodeVerifier:  session.CodeVerifier,
		CodeChallenge: session.CodeChallenge,
	}
	authURL, err := p.auth.GenerateAuthURLWithRedirectURI(session.State, pkce, session.RedirectURI)
	if err != nil {
		return "", session, err
	}
	return authURL, session, nil
}

func (p *OAuthProvider) ExchangeCode(ctx context.Context, session oauthflow.OAuthSession, code string) (*oauthflow.TokenResult, error) {
	if p == nil || p.auth == nil {
		return nil, fmt.Errorf("codex oauth provider: auth is nil")
	}
	pkce := &PKCECodes{
		CodeVerifier:  session.CodeVerifier,
		CodeChallenge: session.CodeChallenge,
	}
	bundle, err := p.auth.ExchangeCodeForTokensWithRedirectURI(ctx, code, pkce, session.RedirectURI)
	if err != nil {
		return nil, err
	}
	if bundle == nil {
		return nil, fmt.Errorf("codex oauth provider: token bundle is nil")
	}

	meta := map[string]any{}
	if email := strings.TrimSpace(bundle.TokenData.Email); email != "" {
		meta["email"] = email
	}
	if accountID := strings.TrimSpace(bundle.TokenData.AccountID); accountID != "" {
		meta["account_id"] = accountID
	}

	return &oauthflow.TokenResult{
		AccessToken:  strings.TrimSpace(bundle.TokenData.AccessToken),
		RefreshToken: strings.TrimSpace(bundle.TokenData.RefreshToken),
		ExpiresAt:    strings.TrimSpace(bundle.TokenData.Expire),
		TokenType:    "Bearer",
		IDToken:      strings.TrimSpace(bundle.TokenData.IDToken),
		Metadata:     meta,
	}, nil
}

func (p *OAuthProvider) Refresh(ctx context.Context, refreshToken string) (*oauthflow.TokenResult, error) {
	if p == nil || p.auth == nil {
		return nil, fmt.Errorf("codex oauth provider: auth is nil")
	}
	data, err := p.auth.RefreshTokens(ctx, refreshToken)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, fmt.Errorf("codex oauth provider: refresh result is nil")
	}

	meta := map[string]any{}
	if email := strings.TrimSpace(data.Email); email != "" {
		meta["email"] = email
	}
	if accountID := strings.TrimSpace(data.AccountID); accountID != "" {
		meta["account_id"] = accountID
	}

	return &oauthflow.TokenResult{
		AccessToken:  strings.TrimSpace(data.AccessToken),
		RefreshToken: strings.TrimSpace(data.RefreshToken),
		ExpiresAt:    strings.TrimSpace(data.Expire),
		TokenType:    "Bearer",
		IDToken:      strings.TrimSpace(data.IDToken),
		Metadata:     meta,
	}, nil
}

// Revoke invalidates the given token at OpenAI.
// Note: OpenAI does not currently provide a public token revocation endpoint,
// so this method returns ErrRevokeNotSupported.
func (p *OAuthProvider) Revoke(ctx context.Context, token string) error {
	// OpenAI does not currently support OAuth token revocation via public API.
	// Users should revoke tokens through the OpenAI platform dashboard.
	return oauthflow.ErrRevokeNotSupported
}
