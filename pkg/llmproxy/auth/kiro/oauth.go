// Package kiro provides OAuth2 authentication for Kiro using native Google login.
package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// Kiro auth endpoint
	kiroAuthEndpoint = "https://prod.us-east-1.auth.desktop.kiro.dev"
)

// KiroTokenResponse represents the response from Kiro token endpoint.
type KiroTokenResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ProfileArn   string `json:"profileArn"`
	ExpiresIn    int    `json:"expiresIn"`
}

// KiroOAuth handles the OAuth flow for Kiro authentication.
type KiroOAuth struct {
	httpClient *http.Client
	cfg        *config.Config
}

// NewKiroOAuth creates a new Kiro OAuth handler.
func NewKiroOAuth(cfg *config.Config) *KiroOAuth {
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		client = util.SetProxy(&cfg.SDKConfig, client)
	}
	return &KiroOAuth{
		httpClient: client,
		cfg:        cfg,
	}
}

// LoginWithBuilderID performs OAuth login with AWS Builder ID using device code flow.
func (o *KiroOAuth) LoginWithBuilderID(ctx context.Context) (*KiroTokenData, error) {
	ssoClient := NewSSOOIDCClient(o.cfg)
	return ssoClient.LoginWithBuilderID(ctx)
}

// LoginWithBuilderIDAuthCode performs OAuth login with AWS Builder ID using authorization code flow.
// This provides a better UX than device code flow as it uses automatic browser callback.
func (o *KiroOAuth) LoginWithBuilderIDAuthCode(ctx context.Context) (*KiroTokenData, error) {
	ssoClient := NewSSOOIDCClient(o.cfg)
	return ssoClient.LoginWithBuilderIDAuthCode(ctx)
}

// RefreshToken refreshes an expired access token.
// Uses KiroIDE-style User-Agent to match official Kiro IDE behavior.
func (o *KiroOAuth) RefreshToken(ctx context.Context, refreshToken string) (*KiroTokenData, error) {
	return o.RefreshTokenWithFingerprint(ctx, refreshToken, "")
}

// RefreshTokenWithFingerprint refreshes an expired access token with a specific fingerprint.
// tokenKey is used to generate a consistent fingerprint for the token.
func (o *KiroOAuth) RefreshTokenWithFingerprint(ctx context.Context, refreshToken, tokenKey string) (*KiroTokenData, error) {
	payload := map[string]string{
		"refreshToken": refreshToken,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	refreshURL := kiroAuthEndpoint + "/refreshToken"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, refreshURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Use KiroIDE-style User-Agent to match official Kiro IDE behavior
	// This helps avoid 403 errors from server-side User-Agent validation
	userAgent := buildKiroUserAgent(tokenKey)
	req.Header.Set("User-Agent", userAgent)

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("token refresh failed (status %d): %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("token refresh failed (status %d): %s", resp.StatusCode, string(respBody))
	}

	var tokenResp KiroTokenResponse
	if err := json.Unmarshal(respBody, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	// Validate ExpiresIn - use default 1 hour if invalid
	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)

	return &KiroTokenData{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ProfileArn:   tokenResp.ProfileArn,
		ExpiresAt:    expiresAt.Format(time.RFC3339),
		AuthMethod:   "social",
		Provider:     "", // Caller should preserve original provider
		Region:       "us-east-1",
	}, nil
}

// buildKiroUserAgent builds a KiroIDE-style User-Agent string.
// If tokenKey is provided, uses fingerprint manager for consistent fingerprint.
// Otherwise generates a simple KiroIDE User-Agent.
func buildKiroUserAgent(tokenKey string) string {
	if tokenKey != "" {
		fm := NewFingerprintManager()
		fp := fm.GetFingerprint(tokenKey)
		return fmt.Sprintf("KiroIDE-%s-%s", fp.KiroVersion, fp.KiroHash[:16])
	}
	// Default KiroIDE User-Agent matching kiro-openai-gateway format
	return "KiroIDE-0.7.45-cli-proxy-api"
}

// LoginWithGoogle performs OAuth login with Google using Kiro's social auth.
// This uses a custom protocol handler (kiro://) to receive the callback.
func (o *KiroOAuth) LoginWithGoogle(ctx context.Context) (*KiroTokenData, error) {
	socialClient := NewSocialAuthClient(o.cfg)
	return socialClient.LoginWithGoogle(ctx)
}

// LoginWithGitHub performs OAuth login with GitHub using Kiro's social auth.
// This uses a custom protocol handler (kiro://) to receive the callback.
func (o *KiroOAuth) LoginWithGitHub(ctx context.Context) (*KiroTokenData, error) {
	socialClient := NewSocialAuthClient(o.cfg)
	return socialClient.LoginWithGitHub(ctx)
}
