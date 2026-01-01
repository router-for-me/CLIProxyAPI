package kiro

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultRegion is the default AWS region for Kiro.
	DefaultRegion = "us-east-1"

	// KiroRefreshURL is the Kiro-specific refresh endpoint for social auth (Google, etc.)
	KiroRefreshURL = "https://prod.%s.auth.desktop.kiro.dev/refreshToken"

	// OIDCRefreshURL is the AWS OIDC refresh endpoint for IAM/SSO auth
	OIDCRefreshURL = "https://oidc.%s.amazonaws.com/token"
)

// KiroRefreshResponse represents the response from Kiro refresh endpoint.
type KiroRefreshResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int    `json:"expiresIn"`
	ProfileArn   string `json:"profileArn"`
}

// RefreshTokens attempts to refresh the access token using the refresh token.
// For social auth (Google, etc.), uses Kiro's dedicated refresh endpoint.
// For IAM/SSO auth, uses AWS OIDC endpoint with client credentials.
func RefreshTokens(creds *KiroCredentials) (*KiroCredentials, error) {
	if creds.RefreshToken == "" {
		return nil, fmt.Errorf("no refresh token available")
	}

	region := creds.Region
	if region == "" {
		region = DefaultRegion
	}

	// Determine auth method - default to social for tokens from Kiro IDE
	authMethod := creds.AuthMethod
	if authMethod == "" {
		authMethod = "social"
	}

	var tokenURL string
	var reqBody []byte

	if authMethod == "social" {
		// Use Kiro's dedicated refresh endpoint for social auth
		tokenURL = fmt.Sprintf(KiroRefreshURL, region)
		payload := map[string]string{
			"refreshToken": creds.RefreshToken,
		}
		reqBody, _ = json.Marshal(payload)
	} else {
		// Use AWS OIDC endpoint for IAM/SSO auth
		tokenURL = fmt.Sprintf(OIDCRefreshURL, region)
		data := url.Values{}
		data.Set("grant_type", "refresh_token")
		data.Set("refresh_token", creds.RefreshToken)
		if creds.ClientID != "" {
			data.Set("client_id", creds.ClientID)
		}
		if creds.ClientSecret != "" {
			data.Set("client_secret", creds.ClientSecret)
		}
		reqBody = []byte(data.Encode())
	}

	req, err := http.NewRequest("POST", tokenURL, strings.NewReader(string(reqBody)))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if authMethod == "social" {
		req.Header.Set("Content-Type", "application/json")
	} else {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send refresh request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token refresh failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	// Create updated credentials
	newCreds := *creds
	newCreds.Type = "kiro" // Ensure type is set for watcher detection

	if authMethod == "social" {
		var kiroResp KiroRefreshResponse
		if err := json.NewDecoder(resp.Body).Decode(&kiroResp); err != nil {
			return nil, fmt.Errorf("failed to decode refresh response: %w", err)
		}
		newCreds.AccessToken = kiroResp.AccessToken
		if kiroResp.RefreshToken != "" {
			newCreds.RefreshToken = kiroResp.RefreshToken
		}
		newCreds.ExpiresIn = kiroResp.ExpiresIn
		newCreds.ExpiresAt = time.Now().Add(time.Duration(kiroResp.ExpiresIn) * time.Second)
		if kiroResp.ProfileArn != "" {
			newCreds.ProfileArn = kiroResp.ProfileArn
		}
	} else {
		var tokenResp TokenResponse
		if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
			return nil, fmt.Errorf("failed to decode token response: %w", err)
		}
		newCreds.AccessToken = tokenResp.AccessToken
		if tokenResp.RefreshToken != "" {
			newCreds.RefreshToken = tokenResp.RefreshToken
		}
		newCreds.ExpiresIn = tokenResp.ExpiresIn
		newCreds.ExpiresAt = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		newCreds.TokenType = tokenResp.TokenType
	}

	return &newCreds, nil
}
