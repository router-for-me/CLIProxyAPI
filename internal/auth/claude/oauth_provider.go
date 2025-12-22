package claude

import (
	"context"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/oauthflow"
)

// OAuthProvider adapts ClaudeAuth to the shared oauthflow.ProviderOAuth interface.
type OAuthProvider struct {
	auth *ClaudeAuth
}

func NewOAuthProvider(auth *ClaudeAuth) *OAuthProvider {
	return &OAuthProvider{auth: auth}
}

func (p *OAuthProvider) Provider() string {
	return "claude"
}

func (p *OAuthProvider) AuthorizeURL(session oauthflow.OAuthSession) (string, oauthflow.OAuthSession, error) {
	if p == nil || p.auth == nil {
		return "", session, fmt.Errorf("claude oauth provider: auth is nil")
	}
	pkce := &PKCECodes{
		CodeVerifier:  session.CodeVerifier,
		CodeChallenge: session.CodeChallenge,
	}
	authURL, returnedState, err := p.auth.GenerateAuthURLWithRedirectURI(session.State, pkce, session.RedirectURI)
	if err != nil {
		return "", session, err
	}
	session.State = returnedState
	return authURL, session, nil
}

func (p *OAuthProvider) ExchangeCode(ctx context.Context, session oauthflow.OAuthSession, code string) (*oauthflow.TokenResult, error) {
	if p == nil || p.auth == nil {
		return nil, fmt.Errorf("claude oauth provider: auth is nil")
	}
	pkce := &PKCECodes{
		CodeVerifier:  session.CodeVerifier,
		CodeChallenge: session.CodeChallenge,
	}
	bundle, err := p.auth.ExchangeCodeForTokensWithRedirectURI(ctx, code, session.State, pkce, session.RedirectURI)
	if err != nil {
		return nil, err
	}
	if bundle == nil {
		return nil, fmt.Errorf("claude oauth provider: token bundle is nil")
	}

	meta := map[string]any{}
	if email := strings.TrimSpace(bundle.TokenData.Email); email != "" {
		meta["email"] = email
	}

	return &oauthflow.TokenResult{
		AccessToken:  strings.TrimSpace(bundle.TokenData.AccessToken),
		RefreshToken: strings.TrimSpace(bundle.TokenData.RefreshToken),
		ExpiresAt:    strings.TrimSpace(bundle.TokenData.Expire),
		TokenType:    "Bearer",
		Metadata:     meta,
	}, nil
}

func (p *OAuthProvider) Refresh(ctx context.Context, refreshToken string) (*oauthflow.TokenResult, error) {
	if p == nil || p.auth == nil {
		return nil, fmt.Errorf("claude oauth provider: auth is nil")
	}
	data, err := p.auth.RefreshTokens(ctx, refreshToken)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, fmt.Errorf("claude oauth provider: refresh result is nil")
	}

	meta := map[string]any{}
	if email := strings.TrimSpace(data.Email); email != "" {
		meta["email"] = email
	}

	return &oauthflow.TokenResult{
		AccessToken:  strings.TrimSpace(data.AccessToken),
		RefreshToken: strings.TrimSpace(data.RefreshToken),
		ExpiresAt:    strings.TrimSpace(data.Expire),
		TokenType:    "Bearer",
		Metadata:     meta,
	}, nil
}

// Revoke invalidates the given token at Anthropic.
// Note: Anthropic does not currently provide a public token revocation endpoint,
// so this method returns ErrRevokeNotSupported.
func (p *OAuthProvider) Revoke(ctx context.Context, token string) error {
	// Anthropic does not currently support OAuth token revocation via public API.
	// Users should revoke tokens through the Anthropic console.
	return oauthflow.ErrRevokeNotSupported
}
