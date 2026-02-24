// Package github provides authentication and token management for GitHub Copilot API.
// It handles the RFC 8628 OAuth2 Device Authorization Grant flow for secure authentication.
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

const (
	// githubClientID is the GitHub CLI OAuth app client ID (public).
	githubClientID = "178c6fc778ccc68e1d6a"
	// githubClientSecret is the GitHub CLI OAuth app client secret (public).
	githubClientSecret = "34ddeff2b558a23d38fba8a6de74f086ede1cc0b"
	// githubDeviceCodeURL is the endpoint for requesting device codes.
	githubDeviceCodeURL = "https://github.com/login/device/code"
	// githubTokenURL is the endpoint for exchanging device codes for tokens.
	githubTokenURL = "https://github.com/login/oauth/access_token"
	// githubScope is the OAuth scope required for GitHub Copilot access.
	githubScope = "repo read:org gist"
	// githubUserAPIURL is the endpoint for basic authenticated user profile.
	githubUserAPIURL = "https://api.github.com/user"
	// defaultPollInterval is the default interval for polling token endpoint.
	defaultPollInterval = 5 * time.Second
	// maxPollDuration is the maximum time to wait for user authorization.
	maxPollDuration = 15 * time.Minute
)

// GithubUserInfo contains normalized account identity fields from GitHub.
type GithubUserInfo struct {
	Login string
	Email string
	Name  string
}

// GithubAuth handles GitHub Copilot authentication flow.
type GithubAuth struct {
	httpClient *http.Client
	cfg        *config.Config
}

// NewGithubAuth creates a new GithubAuth service instance.
func NewGithubAuth(cfg *config.Config) *GithubAuth {
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		client = util.SetProxy(&cfg.SDKConfig, client)
	}
	return &GithubAuth{
		httpClient: client,
		cfg:        cfg,
	}
}

// StartDeviceFlow initiates the GitHub Device Flow authentication.
func (g *GithubAuth) StartDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {
	data := url.Values{}
	data.Set("client_id", githubClientID)
	data.Set("scope", githubScope)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubDeviceCodeURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("github: failed to create device code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: device code request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("github device code: close body error: %v", errClose)
		}
	}()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("github: failed to read device code response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github: device code request failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var deviceCode DeviceCodeResponse
	if err = json.Unmarshal(bodyBytes, &deviceCode); err != nil {
		return nil, fmt.Errorf("github: failed to parse device code response: %w", err)
	}

	if deviceCode.DeviceCode == "" {
		return nil, fmt.Errorf("github: empty device code in response")
	}

	return &deviceCode, nil
}

// WaitForAuthorization polls for user authorization and returns the token data.
func (g *GithubAuth) WaitForAuthorization(ctx context.Context, deviceCode *DeviceCodeResponse) (*GithubTokenData, error) {
	if deviceCode == nil {
		return nil, fmt.Errorf("github: device code is nil")
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

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("github: context cancelled: %w", ctx.Err())
		case <-ticker.C:
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("github: device code expired")
			}

			token, pollErr, shouldContinue := g.exchangeDeviceCode(ctx, deviceCode.DeviceCode, &interval, ticker)
			if token != nil {
				return token, nil
			}
			if !shouldContinue {
				return nil, pollErr
			}
			// Continue polling
		}
	}
}

// exchangeDeviceCode attempts to exchange the device code for an access token.
// Returns (token, error, shouldContinue).
func (g *GithubAuth) exchangeDeviceCode(ctx context.Context, deviceCode string, interval *time.Duration, ticker *time.Ticker) (*GithubTokenData, error, bool) {
	data := url.Values{}
	data.Set("client_id", githubClientID)
	data.Set("client_secret", githubClientSecret)
	data.Set("device_code", deviceCode)
	data.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubTokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("github: failed to create token request: %w", err), false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: token request failed: %w", err), false
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("github token exchange: close body error: %v", errClose)
		}
	}()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("github: failed to read token response: %w", err), false
	}

	var oauthResp struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		AccessToken      string `json:"access_token"`
		TokenType        string `json:"token_type"`
		Scope            string `json:"scope"`
	}

	if err = json.Unmarshal(bodyBytes, &oauthResp); err != nil {
		return nil, fmt.Errorf("github: failed to parse token response: %w", err), false
	}

	if oauthResp.Error != "" {
		switch oauthResp.Error {
		case "authorization_pending":
			return nil, nil, true // Continue polling
		case "slow_down":
			// GitHub asks us to slow down — increase interval by 5 seconds
			*interval += 5 * time.Second
			ticker.Reset(*interval)
			return nil, nil, true
		case "expired_token":
			return nil, fmt.Errorf("github: device code expired"), false
		case "access_denied":
			return nil, fmt.Errorf("github: access denied by user"), false
		default:
			return nil, fmt.Errorf("github: OAuth error: %s - %s", oauthResp.Error, oauthResp.ErrorDescription), false
		}
	}

	if oauthResp.AccessToken == "" {
		return nil, fmt.Errorf("github: empty access token in response"), false
	}

	return &GithubTokenData{
		AccessToken: oauthResp.AccessToken,
		TokenType:   oauthResp.TokenType,
		Scope:       oauthResp.Scope,
	}, nil, false
}

// CreateTokenStorage creates a new GithubTokenStorage from token data.
func (g *GithubAuth) CreateTokenStorage(tokenData *GithubTokenData) *GithubTokenStorage {
	return &GithubTokenStorage{
		AccessToken: tokenData.AccessToken,
		TokenType:   tokenData.TokenType,
		Scope:       tokenData.Scope,
		Type:        "github",
	}
}

// FetchUserInfo loads GitHub account identity using an OAuth access token.
func (g *GithubAuth) FetchUserInfo(ctx context.Context, accessToken string) (*GithubUserInfo, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("github: empty access token")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubUserAPIURL, nil)
	if err != nil {
		return nil, fmt.Errorf("github: failed to create user info request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "CLIProxyAPI-GitHubAuth/1.0")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: user info request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("github user info: close body error: %v", errClose)
		}
	}()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("github: failed to read user info response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github: user info request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var profile struct {
		Login             string `json:"login"`
		Email             string `json:"email"`
		NotificationEmail string `json:"notification_email"`
		Name              string `json:"name"`
	}
	if err = json.Unmarshal(bodyBytes, &profile); err != nil {
		return nil, fmt.Errorf("github: failed to parse user info response: %w", err)
	}

	email := strings.TrimSpace(profile.Email)
	if email == "" {
		email = strings.TrimSpace(profile.NotificationEmail)
	}

	return &GithubUserInfo{
		Login: strings.TrimSpace(profile.Login),
		Email: email,
		Name:  strings.TrimSpace(profile.Name),
	}, nil
}
