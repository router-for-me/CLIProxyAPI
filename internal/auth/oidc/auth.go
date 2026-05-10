package oidc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

const (
	DefaultCallbackPath = "/auth/callback"
	DefaultScope        = "openid profile email"
	DefaultCallbackPort = 38965
)

const (
	MetadataNameKey = "name"
)

type Auth struct {
	httpClient *http.Client
	Config     config.OIDCConfig
}

type TokenData struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Email        string `json:"email"`
	Username     string `json:"username"`
	Name         string `json:"name"`
	Expired      string `json:"expired"`
	Subject      string `json:"subject"`
}

func NewAuth(cfg *config.Config, config config.OIDCConfig) *Auth {
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg == nil {
		return &Auth{httpClient: client, Config: config}
	}
	return &Auth{
		httpClient: util.SetProxy(&cfg.SDKConfig, client),
		Config:     config,
	}
}

func (a *Auth) AuthorizationURL(state, redirectURI string, pkce *PKCECodes) (string, error) {
	if a == nil {
		return "", fmt.Errorf("oidc auth is nil")
	}
	if pkce == nil {
		return "", fmt.Errorf("pkce codes are required")
	}
	redirectURI = strings.TrimSpace(redirectURI)
	if redirectURI == "" {
		return "", fmt.Errorf("redirect uri is required")
	}
	values := url.Values{}
	values.Set("response_type", "code")
	values.Set("client_id", a.Config.ClientID)
	values.Set("redirect_uri", redirectURI)
	values.Set("scope", a.Config.Scope)
	values.Set("state", state)
	values.Set("code_challenge", pkce.CodeChallenge)
	values.Set("code_challenge_method", "S256")
	return joinURL(a.Config.Domain, a.Config.AuthorizePath) + "?" + values.Encode(), nil
}

func (a *Auth) ExchangeCodeForTokens(ctx context.Context, code, redirectURI, codeVerifier string) (*TokenData, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", a.Config.ClientID)
	form.Set("code", strings.TrimSpace(code))
	form.Set("redirect_uri", strings.TrimSpace(redirectURI))
	form.Set("code_verifier", strings.TrimSpace(codeVerifier))
	return a.tokenRequest(ctx, form)
}

func (a *Auth) RefreshTokens(ctx context.Context, refreshToken string) (*TokenData, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", a.Config.ClientID)
	form.Set("refresh_token", strings.TrimSpace(refreshToken))
	return a.tokenRequest(ctx, form)
}

func (a *Auth) CreateTokenStorage(data *TokenData, redirectURI string) *TokenStorage {
	if data == nil {
		return nil
	}
	return &TokenStorage{
		TokenData: data,
	}
}

func (a *Auth) tokenRequest(ctx context.Context, form url.Values) (*TokenData, error) {
	if a == nil {
		return nil, fmt.Errorf("oidc auth is nil")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL(a.Config.Domain, a.Config.TokenPath), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("oidc token: create request failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc token: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("oidc token: read response failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oidc token: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        string `json:"scope"`
		Subject      string `json:"sub"`
	}
	if err = json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("oidc token: parse response failed: %w", err)
	}
	data := &TokenData{
		IDToken:      tokenResp.IDToken,
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
	}
	if tokenResp.ExpiresIn > 0 {
		data.Expired = time.Now().UTC().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339)
	}
	claims, err := parseIDTokenClaims(tokenResp.IDToken)
	if err == nil && claims != nil {
		data.Email = claims.Email
		data.Username = claims.Username
		data.Subject = claims.Subject
		data.Name = claims.Name
		if claims.Expired != "" {
			if data.Expired == "" {
				data.Expired = claims.Expired
			} else if currentExpiry, errCurrent := time.Parse(time.RFC3339, data.Expired); errCurrent == nil {
				if claimExpiry, errClaim := time.Parse(time.RFC3339, claims.Expired); errClaim == nil {
					if claimExpiry.Before(currentExpiry) {
						data.Expired = claims.Expired
					}
				}
			}
		}
	}
	return data, nil
}

type idTokenClaims struct {
	Raw      map[string]any
	Email    string
	Subject  string
	Username string
	Name     string
	Issuer   string
	Expired  string
}

func parseIDTokenClaims(token string) (*idTokenClaims, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, nil
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid id_token format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(addBase64Padding(parts[1]))
		if err != nil {
			return nil, fmt.Errorf("decode id_token payload failed: %w", err)
		}
	}
	raw := make(map[string]any)
	if err = json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("parse id_token payload failed: %w", err)
	}
	return &idTokenClaims{
		Raw:      raw,
		Email:    firstString(raw, "email", "upn"),
		Subject:  firstString(raw, "sub"),
		Username: firstString(raw, "preferred_username", "username", "login"),
		Name:     firstString(raw, "name", "given_name"),
		Issuer:   firstString(raw, "iss"),
		Expired:  firstExpiry(raw, "exp", "expires_at", "expire"),
	}, nil
}

func stringMetadata(metadata map[string]string, key string) string {
	if metadata == nil {
		return ""
	}
	return strings.TrimSpace(metadata[key])
}

func normalizeDomain(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("oidc domain is empty")
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid oidc domain: %w", err)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("invalid oidc domain")
	}
	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func normalizeURLPath(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "/"
	}
	if !strings.HasPrefix(trimmed, "/") {
		trimmed = "/" + trimmed
	}
	return trimmed
}

func joinURL(domain, path string) string {
	return strings.TrimRight(domain, "/") + normalizeURLPath(path)
}

func firstString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		text, ok := value.(string)
		if ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func firstExpiry(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case float64:
			return time.Unix(int64(v), 0).UTC().Format(time.RFC3339)
		case int64:
			return time.Unix(v, 0).UTC().Format(time.RFC3339)
		case int:
			return time.Unix(int64(v), 0).UTC().Format(time.RFC3339)
		case json.Number:
			if n, err := v.Int64(); err == nil {
				return time.Unix(n, 0).UTC().Format(time.RFC3339)
			}
		case string:
			trimmed := strings.TrimSpace(v)
			if trimmed == "" {
				continue
			}
			if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
				return parsed.UTC().Format(time.RFC3339)
			}
			if n, err := json.Number(trimmed).Int64(); err == nil {
				return time.Unix(n, 0).UTC().Format(time.RFC3339)
			}
		}
	}
	return ""
}

func addBase64Padding(value string) string {
	switch len(value) % 4 {
	case 2:
		return value + "=="
	case 3:
		return value + "="
	default:
		return value
	}
}

func cloneMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneModels(input []config.OpenAICompatibilityModel) []config.OpenAICompatibilityModel {
	if len(input) == 0 {
		return nil
	}
	output := make([]config.OpenAICompatibilityModel, len(input))
	copy(output, input)
	return output
}

func IsLoopbackHost(raw string) bool {
	host := strings.ToLower(strings.TrimSpace(raw))
	if host == "localhost" || host == "127.0.0.1" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
