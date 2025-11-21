package antigravity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// AntigravityTokenStorage persists Google Antigravity OAuth credentials.
type AntigravityTokenStorage struct {
	AccessToken      string `json:"access_token"`
	RefreshTokenStr  string `json:"refresh_token"`
	LastRefresh      string `json:"last_refresh"`
	Expire           string `json:"expired"`
	Email            string `json:"email"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
	IsGoogleInternal bool   `json:"is_google_internal"`
}

// SaveTokenToFile serialises the token storage to disk.
func (ts *AntigravityTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	if err := os.MkdirAll(filepath.Dir(authFilePath), 0o700); err != nil {
		return fmt.Errorf("antigravity token: create directory failed: %w", err)
	}

	f, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("antigravity token: create file failed: %w", err)
	}
	defer func() { _ = f.Close() }()

	if err = json.NewEncoder(f).Encode(ts); err != nil {
		return fmt.Errorf("antigravity token: encode token failed: %w", err)
	}
	return nil
}

// GetToken returns a valid access token, refreshing if necessary.
func (ts *AntigravityTokenStorage) GetToken(ctx context.Context, cfg *config.Config) (string, error) {
	if ts == nil {
		return "", fmt.Errorf("antigravity token: storage is nil")
	}

	if ts.ShouldRefresh() {
		if err := ts.RefreshAccessToken(ctx, cfg); err != nil {
			return "", fmt.Errorf("antigravity token: refresh failed: %w", err)
		}
	}

	if ts.AccessToken == "" {
		return "", fmt.Errorf("antigravity token: access token is empty")
	}

	return ts.AccessToken, nil
}

// ShouldRefresh checks if the token needs to be refreshed.
func (ts *AntigravityTokenStorage) ShouldRefresh() bool {
	if ts == nil || ts.Expire == "" {
		return true
	}

	expireTime, err := time.Parse(time.RFC3339, ts.Expire)
	if err != nil {
		log.Warnf("antigravity token: failed to parse expire time: %v", err)
		return true
	}

	return time.Until(expireTime) < 300*time.Second
}

// RefreshAccessToken refreshes the access token using the refresh token.
func (ts *AntigravityTokenStorage) RefreshAccessToken(ctx context.Context, cfg *config.Config) error {
	if ts == nil {
		return fmt.Errorf("antigravity token: storage is nil")
	}

	if ts.RefreshTokenStr == "" {
		return fmt.Errorf("antigravity token: refresh token is empty")
	}

	clientSecret := antigravityOAuthClientSecret

	client := &http.Client{Timeout: 30 * time.Second}
	client = util.SetProxy(&cfg.SDKConfig, client)

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", ts.RefreshTokenStr)
	form.Set("client_id", antigravityOAuthClientID)
	form.Set("client_secret", clientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, antigravityOAuthTokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("antigravity token: create refresh request failed: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("antigravity token: refresh request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("antigravity token: read refresh response failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("antigravity token refresh failed: status=%d body=%s", resp.StatusCode, string(body))

		if resp.StatusCode == http.StatusBadRequest {
			errorResult := gjson.GetBytes(body, "error")
			if errorResult.Exists() {
				errorCode := errorResult.String()
				if errorCode == "invalid_grant" || errorCode == "unauthorized_client" {
					log.Warnf("antigravity token: special error detected (%s), triggering logout", errorCode)
					return fmt.Errorf("antigravity token: %s - re-authentication required", errorCode)
				}
			}
		}

		return fmt.Errorf("antigravity token: refresh failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tokenResp AntigravityTokenResponse
	if err = json.Unmarshal(body, &tokenResp); err != nil {
		return fmt.Errorf("antigravity token: decode refresh response failed: %w", err)
	}

	if tokenResp.AccessToken == "" {
		return fmt.Errorf("antigravity token: missing access token in refresh response")
	}

	ts.AccessToken = tokenResp.AccessToken
	ts.LastRefresh = time.Now().Format(time.RFC3339)
	ts.Expire = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339)

	if tokenResp.RefreshToken != "" {
		ts.RefreshTokenStr = tokenResp.RefreshToken
	}

	if tokenResp.TokenType != "" {
		ts.TokenType = tokenResp.TokenType
	}

	if tokenResp.Scope != "" {
		ts.Scope = tokenResp.Scope
	}

	log.Debugf("antigravity token: successfully refreshed token for user %s", ts.Email)
	return nil
}

// IsValid checks if the current access token is valid and not expired.
func (ts *AntigravityTokenStorage) IsValid() bool {
	if ts == nil || ts.AccessToken == "" || ts.Expire == "" {
		return false
	}

	expireTime, err := time.Parse(time.RFC3339, ts.Expire)
	if err != nil {
		log.Warnf("antigravity token: failed to parse expire time: %v", err)
		return false
	}

	return time.Now().Before(expireTime)
}

// GetUserInfo returns user information from the token storage.
func (ts *AntigravityTokenStorage) GetUserInfo() map[string]interface{} {
	if ts == nil {
		return nil
	}

	return map[string]interface{}{
		"email":              ts.Email,
		"is_google_internal": ts.IsGoogleInternal,
		"last_refresh":       ts.LastRefresh,
		"expire":             ts.Expire,
	}
}
