package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	githubDeviceCodeEndpoint = "https://github.com/login/device/code"
	githubTokenEndpoint      = "https://github.com/login/oauth/access_token"
	githubUserEndpoint       = "https://api.github.com/user"
)

// DeviceCodeResponse represents the GitHub device code response payload.
type DeviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// TokenResponse represents the GitHub device token response payload.
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	ExpiresIn   int    `json:"expires_in"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

// UserResponse represents a minimal GitHub user profile.
type UserResponse struct {
	Login string `json:"login"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// StartDeviceFlow kicks off the GitHub OAuth device flow for Copilot scopes.
func StartDeviceFlow(ctx context.Context, clientID, scope string) (*DeviceCodeResponse, error) {
	values := url.Values{}
	values.Set("client_id", strings.TrimSpace(clientID))
	values.Set("scope", scope)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubDeviceCodeEndpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out DeviceCodeResponse
	if err = json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.DeviceCode == "" || out.UserCode == "" {
		return nil, fmt.Errorf("copilot device flow: missing device_code/user_code (status %d)", resp.StatusCode)
	}
	return &out, nil
}

// PollForToken polls GitHub for an access token using the device code.
func PollForToken(ctx context.Context, clientID, deviceCode string, interval time.Duration) (*TokenResponse, error) {
	if interval <= 0 {
		interval = 5 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	fmt.Printf("Waiting for authorization (polling every %s)\n", interval)

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			values := url.Values{}
			values.Set("client_id", strings.TrimSpace(clientID))
			values.Set("device_code", strings.TrimSpace(deviceCode))
			values.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

			req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubTokenEndpoint, strings.NewReader(values.Encode()))
			if err != nil {
				return nil, err
			}
			req.Header.Set("Accept", "application/json")
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return nil, err
			}
			bodyBytes, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()

			var token TokenResponse
			if err = json.Unmarshal(bodyBytes, &token); err != nil {
				fmt.Printf("[copilot] token poll decode error (status %d): %v body=%s\n", resp.StatusCode, err, string(bodyBytes))
				return nil, err
			}

			if token.Error != "" || token.AccessToken != "" {
				fmt.Printf("[copilot] poll status=%d error=%s access_token_present=%t\n", resp.StatusCode, token.Error, token.AccessToken != "")
			}

			switch token.Error {
			case "authorization_pending", "slow_down":
				// continue polling, print dot to show progress
				fmt.Print(".")
				if token.Error == "slow_down" {
					interval += 5 * time.Second // GitHub requires 5s increase on slow_down
					ticker.Reset(interval)
				}
				continue
			case "access_denied":
				return nil, errors.New("copilot device flow: access denied")
			case "expired_token":
				return nil, errors.New("copilot device flow: device code expired")
			}

			if token.AccessToken != "" {
				fmt.Println() // newline after dots
				fmt.Printf("[copilot] received access token\n")
				return &token, nil
			}

			if token.Error != "" {
				return nil, fmt.Errorf("copilot device flow: token error: %s", token.Error)
			}
		}
	}
}

// FetchUser fetches the GitHub user profile using the access token.
func FetchUser(ctx context.Context, accessToken string) (*UserResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubUserEndpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("copilot user lookup failed: status %d", resp.StatusCode)
	}

	var user UserResponse
	if err = json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}
	if user.Login == "" {
		return nil, fmt.Errorf("copilot user lookup: missing login")
	}
	return &user, nil
}
