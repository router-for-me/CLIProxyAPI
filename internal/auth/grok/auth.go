package grok

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

// GrokAuth handles the xAI OAuth2 authentication flow (authorization-code + PKCE).
type GrokAuth struct {
	httpClient *http.Client
}

// NewGrokAuth creates a new GrokAuth service using proxy settings from cfg.
func NewGrokAuth(cfg *config.Config) *GrokAuth {
	return NewGrokAuthWithProxyURL(cfg, "")
}

// NewGrokAuthWithClient creates a GrokAuth that uses the supplied http.Client
// for all token-endpoint requests. Intended for tests that need to redirect
// token-refresh calls to a fake server without network I/O.
func NewGrokAuthWithClient(client *http.Client) *GrokAuth {
	if client == nil {
		client = &http.Client{}
	}
	return &GrokAuth{httpClient: client}
}

// NewGrokAuthWithProxyURL creates a new GrokAuth service.
// proxyURL takes precedence over cfg.ProxyURL when non-empty.
func NewGrokAuthWithProxyURL(cfg *config.Config, proxyURL string) *GrokAuth {
	effectiveProxyURL := strings.TrimSpace(proxyURL)
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effectiveProxyURL
	return &GrokAuth{
		httpClient: util.SetProxy(&sdkCfg, &http.Client{}),
	}
}

// GenerateAuthURL builds the xAI authorize URL with all required parameters.
// Both plan=generic and referrer=cliproxyapi are mandatory: without plan=generic
// accounts.x.ai rejects loopback OAuth from non-allowlisted clients.
func (g *GrokAuth) GenerateAuthURL(state, nonce string, pkce *PKCECodes) (string, error) {
	if pkce == nil {
		return "", fmt.Errorf("PKCE codes are required")
	}

	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {ClientID},
		"redirect_uri":          {RedirectURI},
		"scope":                 {Scope},
		"code_challenge":        {pkce.CodeChallenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
		"nonce":                 {nonce},
		"plan":                  {AuthorizePlan},
		"referrer":              {AuthorizeReferrer},
	}

	authURL := fmt.Sprintf("%s?%s", AuthorizeURL, params.Encode())
	log.Debugf("Generated xAI authorize URL (state=%s)", state)
	return authURL, nil
}

// ExchangeCodeForTokens exchanges an authorization code for access + refresh tokens.
// It POSTs to TokenURL with an application/x-www-form-urlencoded body.
func (g *GrokAuth) ExchangeCodeForTokens(ctx context.Context, code string, pkce *PKCECodes) (*TokenResponse, error) {
	if pkce == nil {
		return nil, fmt.Errorf("PKCE codes are required for token exchange")
	}
	if strings.TrimSpace(code) == "" {
		return nil, fmt.Errorf("authorization code is required")
	}

	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {RedirectURI},
		"client_id":     {ClientID},
		"code_verifier": {pkce.CodeVerifier},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		excerpt := string(body)
		if len(excerpt) > 256 {
			excerpt = excerpt[:256]
		}
		return nil, fmt.Errorf("%w: status %d: %s", ErrCodeExchangeFailed, resp.StatusCode, excerpt)
	}

	var tokenResp TokenResponse
	if err = json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	log.Debugf("xAI token exchange succeeded (token_type=%s)", tokenResp.TokenType)
	return &tokenResp, nil
}
