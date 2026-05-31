package grok

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/oauthflight"
	log "github.com/sirupsen/logrus"
)

// RefreshAccessToken refreshes the Grok access token using the refresh_token grant.
//
// All concurrent calls for the same authID collapse into one HTTP call via
// oauthflight.Do so a single rotating refresh_token is never replayed by N
// goroutines (which would burn it and poison the credential).
func (g *GrokAuth) RefreshAccessToken(ctx context.Context, authID, refreshToken string) (*TokenResponse, error) {
	return oauthflight.Do[*TokenResponse](authID, func() (*TokenResponse, error) {
		return g.refreshAccessTokenRaw(ctx, refreshToken)
	})
}

// refreshAccessTokenRaw is the un-collapsed refresh path. It POSTs to TokenURL
// with grant_type=refresh_token + refresh_token + client_id. Callers should
// prefer RefreshAccessToken (single-flight); raw is exposed for tests.
func (g *GrokAuth) refreshAccessTokenRaw(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, fmt.Errorf("refresh token is required")
	}

	data := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {ClientID},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token refresh request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read refresh response: %w", err)
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
		return nil, fmt.Errorf("failed to parse refresh response: %w", err)
	}

	log.Debugf("xAI token refresh succeeded (token_type=%s)", tokenResp.TokenType)
	return &tokenResp, nil
}
