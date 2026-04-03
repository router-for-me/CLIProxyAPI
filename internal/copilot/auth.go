package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	GitHubCopilotClientID = "Iv1.b507a08c87ecfe98"
	GitHubScope           = "read:email"
	GitHubDeviceCodeURL   = "https://github.com/login/device/code"
	GitHubAccessTokenURL  = "https://github.com/login/oauth/access_token"
	GitHubUserURL         = "https://api.github.com/user"
	DeviceCodeTimeout     = 15 * time.Minute
	DefaultPollInterval   = 5 * time.Second
)

type CopilotAuth struct {
	httpClient *http.Client
}

func NewCopilotAuth(httpClient *http.Client) *CopilotAuth {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &CopilotAuth{httpClient: httpClient}
}

func (a *CopilotAuth) RequestDeviceCode(ctx context.Context) (*DeviceCodeResponse, error) {
	form := url.Values{}
	form.Set("client_id", GitHubCopilotClientID)
	form.Set("scope", GitHubScope)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, GitHubDeviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var deviceCodeResp DeviceCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&deviceCodeResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &deviceCodeResp, nil
}

func (a *CopilotAuth) PollForToken(ctx context.Context, deviceCode string, interval time.Duration) (string, error) {
	deadline := time.Now().Add(DeviceCodeTimeout)

	for {
		if time.Now().After(deadline) {
			return "", errors.New("timeout waiting for device code authorization")
		}

		form := url.Values{}
		form.Set("client_id", GitHubCopilotClientID)
		form.Set("device_code", deviceCode)
		form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, GitHubAccessTokenURL, strings.NewReader(form.Encode()))
		if err != nil {
			return "", fmt.Errorf("failed to create request: %w", err)
		}

		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")

		resp, err := a.httpClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("failed to do request: %w", err)
		}

		var tokenResp AccessTokenResponse
		if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
			resp.Body.Close()
			return "", fmt.Errorf("failed to decode response: %w", err)
		}
		resp.Body.Close()

		if tokenResp.AccessToken != "" {
			return tokenResp.AccessToken, nil
		}

		switch tokenResp.Error {
		case "authorization_pending":
			// User hasn't authorized yet, wait and try again
		case "slow_down":
			// Too many requests, increase interval
			interval += 5 * time.Second
		case "expired_token":
			return "", errors.New("device code has expired")
		case "access_denied":
			return "", errors.New("access denied by user")
		default:
			if tokenResp.Error != "" {
				return "", fmt.Errorf("authorization error: %s - %s", tokenResp.Error, tokenResp.ErrorDescription)
			}
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(interval):
			// Proceed to next iteration
		}
	}
}

// TryExchangeToken makes a single attempt to exchange a device code for an access token.
// It returns the token if authorization is complete, or an error describing the current state
// (e.g., "authorization_pending", "slow_down", "expired_token", "access_denied").
// Unlike PollForToken, this method does NOT loop or sleep — it returns immediately.
func (a *CopilotAuth) TryExchangeToken(ctx context.Context, deviceCode string) (string, error) {
	form := url.Values{}
	form.Set("client_id", GitHubCopilotClientID)
	form.Set("device_code", deviceCode)
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, GitHubAccessTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to do request: %w", err)
	}
	defer resp.Body.Close()

	var tokenResp AccessTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if tokenResp.AccessToken != "" {
		return tokenResp.AccessToken, nil
	}

	if tokenResp.Error != "" {
		return "", errors.New(tokenResp.Error)
	}

	return "", errors.New("no token and no error in response")
}

func (a *CopilotAuth) FetchUserEmail(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, GitHubUserURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var user map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if login, ok := user["login"].(string); ok && login != "" {
		return login, nil
	}

	if email, ok := user["email"].(string); ok && email != "" {
		return email, nil
	}

	return "", errors.New("no login or email found in user profile")
}
