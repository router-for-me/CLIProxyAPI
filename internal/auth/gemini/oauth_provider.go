package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/oauthflow"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/oauthhttp"
)

// OAuthProvider adapts Gemini OAuth to the shared oauthflow.ProviderOAuth interface.
type OAuthProvider struct {
	httpClient *http.Client
}

func NewOAuthProvider(httpClient *http.Client) *OAuthProvider {
	return &OAuthProvider{httpClient: httpClient}
}

func (p *OAuthProvider) Provider() string {
	return "gemini"
}

func (p *OAuthProvider) AuthorizeURL(session oauthflow.OAuthSession) (string, oauthflow.OAuthSession, error) {
	if p == nil {
		return "", session, fmt.Errorf("gemini oauth provider: provider is nil")
	}
	redirectURI := strings.TrimSpace(session.RedirectURI)
	if redirectURI == "" {
		return "", session, fmt.Errorf("gemini oauth provider: redirect URI is empty")
	}

	params := url.Values{}
	params.Set("access_type", "offline")
	params.Set("client_id", geminiOauthClientID)
	params.Set("prompt", "consent")
	params.Set("redirect_uri", redirectURI)
	params.Set("response_type", "code")
	params.Set("scope", strings.Join(geminiOauthScopes, " "))
	params.Set("state", session.State)
	if strings.TrimSpace(session.CodeChallenge) != "" {
		params.Set("code_challenge", strings.TrimSpace(session.CodeChallenge))
		params.Set("code_challenge_method", "S256")
	}
	return "https://accounts.google.com/o/oauth2/v2/auth?" + params.Encode(), session, nil
}

type googleTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	IDToken      string `json:"id_token"`
}

func (p *OAuthProvider) ExchangeCode(ctx context.Context, session oauthflow.OAuthSession, code string) (*oauthflow.TokenResult, error) {
	if p == nil || p.httpClient == nil {
		return nil, fmt.Errorf("gemini oauth provider: http client is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, fmt.Errorf("gemini oauth provider: authorization code is empty")
	}

	data := url.Values{}
	data.Set("code", code)
	data.Set("client_id", geminiOauthClientID)
	data.Set("client_secret", geminiOauthClientSecret)
	data.Set("redirect_uri", strings.TrimSpace(session.RedirectURI))
	data.Set("grant_type", "authorization_code")
	if strings.TrimSpace(session.CodeVerifier) != "" {
		data.Set("code_verifier", strings.TrimSpace(session.CodeVerifier))
	}

	encoded := data.Encode()
	status, _, body, err := oauthhttp.Do(
		ctx,
		p.httpClient,
		func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(encoded))
			if err != nil {
				return nil, err
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Accept", "application/json")
			return req, nil
		},
		oauthhttp.DefaultRetryConfig(),
	)
	if err != nil && status == 0 {
		return nil, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		msg := strings.TrimSpace(string(body))
		if err != nil {
			return nil, fmt.Errorf("gemini oauth token exchange failed: status %d: %s: %w", status, msg, err)
		}
		return nil, fmt.Errorf("gemini oauth token exchange failed: status %d: %s", status, msg)
	}
	if err != nil {
		return nil, err
	}

	var token googleTokenResponse
	if errDecode := json.Unmarshal(body, &token); errDecode != nil {
		return nil, errDecode
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return nil, fmt.Errorf("gemini oauth token exchange failed: empty access token")
	}

	tokenType := strings.TrimSpace(token.TokenType)
	if tokenType == "" {
		tokenType = "Bearer"
	}
	expiresAt := ""
	if token.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).Format(time.RFC3339)
	}

	meta := map[string]any{
		"expires_in": token.ExpiresIn,
	}
	if strings.TrimSpace(token.Scope) != "" {
		meta["scope"] = strings.TrimSpace(token.Scope)
	}

	return &oauthflow.TokenResult{
		AccessToken:  strings.TrimSpace(token.AccessToken),
		RefreshToken: strings.TrimSpace(token.RefreshToken),
		ExpiresAt:    expiresAt,
		TokenType:    tokenType,
		IDToken:      strings.TrimSpace(token.IDToken),
		Metadata:     meta,
	}, nil
}

func (p *OAuthProvider) Refresh(ctx context.Context, refreshToken string) (*oauthflow.TokenResult, error) {
	if p == nil || p.httpClient == nil {
		return nil, fmt.Errorf("gemini oauth provider: http client is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, fmt.Errorf("gemini oauth provider: refresh token is empty")
	}

	data := url.Values{}
	data.Set("refresh_token", refreshToken)
	data.Set("client_id", geminiOauthClientID)
	data.Set("client_secret", geminiOauthClientSecret)
	data.Set("grant_type", "refresh_token")

	encoded := data.Encode()
	status, _, body, err := oauthhttp.Do(
		ctx,
		p.httpClient,
		func() (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(encoded))
			if err != nil {
				return nil, err
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Accept", "application/json")
			return req, nil
		},
		oauthhttp.DefaultRetryConfig(),
	)
	if err != nil && status == 0 {
		return nil, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		msg := strings.TrimSpace(string(body))
		if err != nil {
			return nil, fmt.Errorf("gemini oauth token refresh failed: status %d: %s: %w", status, msg, err)
		}
		return nil, fmt.Errorf("gemini oauth token refresh failed: status %d: %s", status, msg)
	}
	if err != nil {
		return nil, err
	}

	var token googleTokenResponse
	if errDecode := json.Unmarshal(body, &token); errDecode != nil {
		return nil, errDecode
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return nil, fmt.Errorf("gemini oauth token refresh failed: empty access token")
	}

	tokenType := strings.TrimSpace(token.TokenType)
	if tokenType == "" {
		tokenType = "Bearer"
	}
	expiresAt := ""
	if token.ExpiresIn > 0 {
		expiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).Format(time.RFC3339)
	}

	meta := map[string]any{
		"expires_in": token.ExpiresIn,
	}
	if strings.TrimSpace(token.Scope) != "" {
		meta["scope"] = strings.TrimSpace(token.Scope)
	}

	return &oauthflow.TokenResult{
		AccessToken:  strings.TrimSpace(token.AccessToken),
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
		TokenType:    tokenType,
		Metadata:     meta,
	}, nil
}

func (p *OAuthProvider) Revoke(ctx context.Context, token string) error {
	return oauthflow.ErrRevokeNotSupported
}
