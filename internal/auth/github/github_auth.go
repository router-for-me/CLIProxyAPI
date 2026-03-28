package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

// GithubAuth handles the GitHub device authorization flow and Copilot token exchange.
type GithubAuth struct {
	httpClient *http.Client
}

// NewGithubAuth creates a new GithubAuth service instance with optional proxy configuration.
func NewGithubAuth(cfg *config.Config) *GithubAuth {
	if cfg == nil {
		cfg = &config.Config{}
	}
	return &GithubAuth{
		httpClient: util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second}),
	}
}

// RequestDeviceCode initiates the GitHub device authorization flow.
// Returns the device code response containing user_code and verification_uri.
func (a *GithubAuth) RequestDeviceCode(ctx context.Context) (*DeviceCodeResponse, error) {
	data := url.Values{}
	data.Set("client_id", ClientID)
	data.Set("scope", Scope)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, DeviceCodeURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("github: failed to create device code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: device code request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("github: close device code response body: %v", errClose)
		}
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil {
		return nil, fmt.Errorf("github: failed to read device code response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github: device code request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var deviceCode DeviceCodeResponse
	if err = json.Unmarshal(body, &deviceCode); err != nil {
		return nil, fmt.Errorf("github: failed to parse device code response: %w", err)
	}

	return &deviceCode, nil
}

// PollForGithubToken polls the access token endpoint until the user authorizes or the code expires.
// Returns the GitHub user access token (ghu_...) on success.
// Per RFC 8628, the interval is increased by 5 seconds on each slow_down response.
func (a *GithubAuth) PollForGithubToken(ctx context.Context, deviceCode *DeviceCodeResponse) (string, error) {
	if deviceCode == nil {
		return "", fmt.Errorf("github: device code is nil")
	}

	interval := time.Duration(deviceCode.Interval) * time.Second
	if interval < defaultPollInterval {
		interval = defaultPollInterval
	}

	deadline := time.Now().Add(maxPollDuration)
	if deviceCode.ExpiresIn > 0 {
		codeDeadline := time.Now().Add(time.Duration(deviceCode.ExpiresIn) * time.Second)
		if codeDeadline.Before(deadline) {
			deadline = codeDeadline
		}
	}

	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("github: context cancelled: %w", ctx.Err())
		case <-time.After(interval):
		}

		if time.Now().After(deadline) {
			return "", fmt.Errorf("github: device code expired")
		}

		token, pollErr, shouldContinue, slowDown := a.exchangeDeviceCode(ctx, deviceCode.DeviceCode)
		if token != "" {
			return token, nil
		}
		if slowDown {
			interval += 5 * time.Second
		}
		if !shouldContinue {
			return "", pollErr
		}
	}
}

// exchangeDeviceCode attempts to exchange the device code for a GitHub user access token.
// Returns (token, error, shouldContinue, slowDown). slowDown is true when GitHub requests
// a slower poll rate; the caller must increase the interval by 5 seconds per RFC 8628.
func (a *GithubAuth) exchangeDeviceCode(ctx context.Context, deviceCode string) (string, error, bool, bool) {
	data := url.Values{}
	data.Set("client_id", ClientID)
	data.Set("device_code", deviceCode)
	data.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, AccessTokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("github: failed to create token request: %w", err), false, false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("github: token request failed: %w", err), false, false
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("github: close token response body: %v", errClose)
		}
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil {
		return "", fmt.Errorf("github: failed to read token response: %w", err), false, false
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err = json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("github: failed to parse token response: %w", err), false, false
	}

	if tokenResp.Error != "" {
		switch tokenResp.Error {
		case "authorization_pending":
			return "", nil, true, false
		case "slow_down":
			return "", nil, true, true // caller must increase interval by 5s
		case "expired_token":
			return "", fmt.Errorf("github: device code expired"), false, false
		case "access_denied":
			return "", fmt.Errorf("github: access denied by user"), false, false
		default:
			return "", fmt.Errorf("github: OAuth error: %s - %s", tokenResp.Error, tokenResp.ErrorDesc), false, false
		}
	}

	if tokenResp.AccessToken == "" {
		return "", fmt.Errorf("github: empty access token in response"), false, false
	}

	return tokenResp.AccessToken, nil, false, false
}

// FetchUserInfo retrieves the GitHub username and email for an authenticated user.
func (a *GithubAuth) FetchUserInfo(ctx context.Context, githubToken string) (login, email string, err error) {
	githubToken = strings.TrimSpace(githubToken)
	if githubToken == "" {
		return "", "", fmt.Errorf("github: missing GitHub token")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, UserInfoURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("github: failed to create user info request: %w", err)
	}
	req.Header.Set("Authorization", "token "+githubToken)
	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("github: user info request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("github: close user info response body: %v", errClose)
		}
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil {
		return "", "", fmt.Errorf("github: failed to read user info response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("github: user info request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var userInfo struct {
		Login string `json:"login"`
		Email string `json:"email"`
	}
	if err = json.Unmarshal(body, &userInfo); err != nil {
		return "", "", fmt.Errorf("github: failed to parse user info response: %w", err)
	}

	login = strings.TrimSpace(userInfo.Login)
	email = strings.TrimSpace(userInfo.Email)

	if login == "" {
		return "", "", fmt.Errorf("github: empty login in user info response")
	}

	return login, email, nil
}

// FetchCopilotToken exchanges a GitHub user token for a short-lived Copilot API token.
// The returned token is used as the Bearer token for Copilot API requests.
func (a *GithubAuth) FetchCopilotToken(ctx context.Context, githubToken string) (*CopilotToken, error) {
	githubToken = strings.TrimSpace(githubToken)
	if githubToken == "" {
		return nil, fmt.Errorf("github: missing GitHub token for Copilot token exchange")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return nil, fmt.Errorf("github: failed to create Copilot token request: %w", err)
	}
	req.Header.Set("Authorization", "token "+githubToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Editor-Version", CopilotEditorVersion)
	req.Header.Set("Editor-Plugin-Version", CopilotEditorPluginVersion)
	req.Header.Set("Copilot-Integration-Id", CopilotIntegrationID)
	req.Header.Set("User-Agent", CopilotUserAgent)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: Copilot token request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("github: close Copilot token response body: %v", errClose)
		}
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil {
		return nil, fmt.Errorf("github: failed to read Copilot token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github: Copilot token request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var copilotToken CopilotToken
	if err = json.Unmarshal(body, &copilotToken); err != nil {
		return nil, fmt.Errorf("github: failed to parse Copilot token response: %w", err)
	}

	copilotToken.Token = strings.TrimSpace(copilotToken.Token)
	if copilotToken.Token == "" {
		return nil, fmt.Errorf("github: empty Copilot token in response")
	}

	return &copilotToken, nil
}
