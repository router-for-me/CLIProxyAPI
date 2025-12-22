package iflow

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/oauthflow"
)

// OAuthProvider adapts IFlowAuth to the shared oauthflow.ProviderOAuth interface.
type OAuthProvider struct {
	auth *IFlowAuth
}

func NewOAuthProvider(auth *IFlowAuth) *OAuthProvider {
	return &OAuthProvider{auth: auth}
}

func (p *OAuthProvider) Provider() string {
	return "iflow"
}

func (p *OAuthProvider) AuthorizeURL(session oauthflow.OAuthSession) (string, oauthflow.OAuthSession, error) {
	if p == nil || p.auth == nil {
		return "", session, fmt.Errorf("iflow oauth provider: auth is nil")
	}
	redirectURI := strings.TrimSpace(session.RedirectURI)
	if redirectURI == "" {
		return "", session, fmt.Errorf("iflow oauth provider: redirect URI is empty")
	}
	parsed, err := url.Parse(redirectURI)
	if err != nil {
		return "", session, fmt.Errorf("iflow oauth provider: parse redirect URI: %w", err)
	}
	portStr := parsed.Port()
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		return "", session, fmt.Errorf("iflow oauth provider: invalid redirect URI port: %q", portStr)
	}

	authURL, resolvedRedirectURI := p.auth.AuthorizationURL(session.State, port)
	session.RedirectURI = resolvedRedirectURI
	return authURL, session, nil
}

func (p *OAuthProvider) ExchangeCode(ctx context.Context, session oauthflow.OAuthSession, code string) (*oauthflow.TokenResult, error) {
	if p == nil || p.auth == nil {
		return nil, fmt.Errorf("iflow oauth provider: auth is nil")
	}
	data, err := p.auth.ExchangeCodeForTokens(ctx, code, session.RedirectURI)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, fmt.Errorf("iflow oauth provider: token result is nil")
	}

	meta := map[string]any{}
	if email := strings.TrimSpace(data.Email); email != "" {
		meta["email"] = email
	}
	if apiKey := strings.TrimSpace(data.APIKey); apiKey != "" {
		meta["api_key"] = apiKey
	}
	if scope := strings.TrimSpace(data.Scope); scope != "" {
		meta["scope"] = scope
	}

	tokenType := strings.TrimSpace(data.TokenType)
	if tokenType == "" {
		tokenType = "Bearer"
	}

	return &oauthflow.TokenResult{
		AccessToken:  strings.TrimSpace(data.AccessToken),
		RefreshToken: strings.TrimSpace(data.RefreshToken),
		ExpiresAt:    strings.TrimSpace(data.Expire),
		TokenType:    tokenType,
		Metadata:     meta,
	}, nil
}

func (p *OAuthProvider) Refresh(ctx context.Context, refreshToken string) (*oauthflow.TokenResult, error) {
	if p == nil || p.auth == nil {
		return nil, fmt.Errorf("iflow oauth provider: auth is nil")
	}
	data, err := p.auth.RefreshTokens(ctx, refreshToken)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, fmt.Errorf("iflow oauth provider: refresh result is nil")
	}

	meta := map[string]any{}
	if email := strings.TrimSpace(data.Email); email != "" {
		meta["email"] = email
	}
	if apiKey := strings.TrimSpace(data.APIKey); apiKey != "" {
		meta["api_key"] = apiKey
	}
	if scope := strings.TrimSpace(data.Scope); scope != "" {
		meta["scope"] = scope
	}

	tokenType := strings.TrimSpace(data.TokenType)
	if tokenType == "" {
		tokenType = "Bearer"
	}

	return &oauthflow.TokenResult{
		AccessToken:  strings.TrimSpace(data.AccessToken),
		RefreshToken: strings.TrimSpace(data.RefreshToken),
		ExpiresAt:    strings.TrimSpace(data.Expire),
		TokenType:    tokenType,
		Metadata:     meta,
	}, nil
}

// Revoke invalidates the given token at the iFlow provider.
// Note: iFlow does not currently provide a public token revocation endpoint,
// so this method returns ErrRevokeNotSupported.
func (p *OAuthProvider) Revoke(ctx context.Context, token string) error {
	return oauthflow.ErrRevokeNotSupported
}
