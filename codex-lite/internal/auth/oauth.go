package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	AuthURL     = "https://auth.openai.com/oauth/authorize"
	TokenURL    = "https://auth.openai.com/oauth/token"
	ClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
	RedirectURI = "http://localhost:1455/auth/callback"
)

type CodexAuth struct {
	client *http.Client
}

func NewCodexAuth() *CodexAuth {
	return &CodexAuth{
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (a *CodexAuth) GenerateAuthURL(state string, pkce *PKCECodes) string {
	params := url.Values{
		"client_id":             {ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {RedirectURI},
		"scope":                 {"openid email profile offline_access"},
		"state":                 {state},
		"code_challenge":        {pkce.CodeChallenge},
		"code_challenge_method": {"S256"},
		"prompt":                {"login"},
	}
	return fmt.Sprintf("%s?%s", AuthURL, params.Encode())
}

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func (a *CodexAuth) ExchangeCode(ctx context.Context, code string, pkce *PKCECodes) (*TokenResponse, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {ClientID},
		"code":          {code},
		"redirect_uri":  {RedirectURI},
		"code_verifier": {pkce.CodeVerifier},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed: %s", string(body))
	}

	var tokenResp TokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, err
	}
	return &tokenResp, nil
}

func (a *CodexAuth) RefreshToken(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	data := url.Values{
		"client_id":     {ClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token refresh failed: %s", string(body))
	}

	var tokenResp TokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, err
	}
	return &tokenResp, nil
}
