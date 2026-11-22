package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

const (
	// DefaultRefreshURL is the endpoint used to refresh Kiro tokens.
	DefaultRefreshURL = "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken"
	// DefaultRuntimeURL is the streaming endpoint for Kiro inference.
	DefaultRuntimeURL = "https://runtime.us-east-1.kiro.dev/v1/responses"
)

// KiroAuthService handles communication with the Kiro authentication endpoints.
type KiroAuthService struct {
	client     *http.Client
	refreshURL string
	runtimeURL string
}

// NewKiroAuthService constructs a new Kiro auth service with the optional HTTP client.
func NewKiroAuthService(client *http.Client) *KiroAuthService {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &KiroAuthService{
		client:     client,
		refreshURL: DefaultRefreshURL,
		runtimeURL: DefaultRuntimeURL,
	}
}

// NewKiroAuthServiceWithProxyURL creates a service configured with proxy settings.
func NewKiroAuthServiceWithProxyURL(cfg *config.Config, proxyURL string) *KiroAuthService {
	effectiveProxyURL := strings.TrimSpace(proxyURL)
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effectiveProxyURL
	client := util.SetProxy(&sdkCfg, &http.Client{Timeout: 30 * time.Second})
	return NewKiroAuthService(client)
}

// RefreshTokens exchanges a refresh token for a fresh access token.
func (s *KiroAuthService) RefreshTokens(ctx context.Context, refreshToken string) (*KiroTokenBundle, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, fmt.Errorf("kiro auth: refresh token is required")
	}

	payload := map[string]string{
		"refreshToken": refreshToken,
	}
	bodyBytes, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("kiro auth: failed to marshal refresh request: %w", errMarshal)
	}

	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, s.refreshURL, bytes.NewReader(bodyBytes))
	if errReq != nil {
		return nil, fmt.Errorf("kiro auth: failed to create refresh request: %w", errReq)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, errDo := s.client.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("kiro auth: refresh request failed: %w", errDo)
	}
	defer resp.Body.Close()

	respBody, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return nil, fmt.Errorf("kiro auth: failed to read refresh response: %w", errRead)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kiro auth: refresh failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var refreshResp RefreshResponse
	if errUnmarshal := json.Unmarshal(respBody, &refreshResp); errUnmarshal != nil {
		return nil, fmt.Errorf("kiro auth: failed to unmarshal refresh response: %w", errUnmarshal)
	}

	if refreshResp.AccessToken == "" {
		return nil, fmt.Errorf("kiro auth: refresh response did not contain an access token")
	}

	expiresIn := refreshResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second).UTC().Format(time.RFC3339)

	nextRefreshToken := refreshResp.RefreshToken
	if nextRefreshToken == "" {
		nextRefreshToken = refreshToken
	}

	return &KiroTokenBundle{
		AccessToken:  refreshResp.AccessToken,
		RefreshToken: nextRefreshToken,
		ProfileArn:   refreshResp.ProfileArn,
		ExpiresAt:    expiresAt,
	}, nil
}
