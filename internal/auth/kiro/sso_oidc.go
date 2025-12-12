// Package kiro provides AWS SSO OIDC authentication for Kiro.
package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// AWS SSO OIDC endpoints
	ssoOIDCEndpoint = "https://oidc.us-east-1.amazonaws.com"
	
	// Kiro's start URL for Builder ID
	builderIDStartURL = "https://view.awsapps.com/start"
	
	// Polling interval
	pollInterval = 5 * time.Second
)

// SSOOIDCClient handles AWS SSO OIDC authentication.
type SSOOIDCClient struct {
	httpClient *http.Client
	cfg        *config.Config
}

// NewSSOOIDCClient creates a new SSO OIDC client.
func NewSSOOIDCClient(cfg *config.Config) *SSOOIDCClient {
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		client = util.SetProxy(&cfg.SDKConfig, client)
	}
	return &SSOOIDCClient{
		httpClient: client,
		cfg:        cfg,
	}
}

// RegisterClientResponse from AWS SSO OIDC.
type RegisterClientResponse struct {
	ClientID                string `json:"clientId"`
	ClientSecret            string `json:"clientSecret"`
	ClientIDIssuedAt        int64  `json:"clientIdIssuedAt"`
	ClientSecretExpiresAt   int64  `json:"clientSecretExpiresAt"`
}

// StartDeviceAuthResponse from AWS SSO OIDC.
type StartDeviceAuthResponse struct {
	DeviceCode              string `json:"deviceCode"`
	UserCode                string `json:"userCode"`
	VerificationURI         string `json:"verificationUri"`
	VerificationURIComplete string `json:"verificationUriComplete"`
	ExpiresIn               int    `json:"expiresIn"`
	Interval                int    `json:"interval"`
}

// CreateTokenResponse from AWS SSO OIDC.
type CreateTokenResponse struct {
	AccessToken  string `json:"accessToken"`
	TokenType    string `json:"tokenType"`
	ExpiresIn    int    `json:"expiresIn"`
	RefreshToken string `json:"refreshToken"`
}

// RegisterClient registers a new OIDC client with AWS.
func (c *SSOOIDCClient) RegisterClient(ctx context.Context) (*RegisterClientResponse, error) {
	// Generate unique client name for each registration to support multiple accounts
	clientName := fmt.Sprintf("CLI-Proxy-API-%d", time.Now().UnixNano())
	
	payload := map[string]interface{}{
		"clientName": clientName,
		"clientType": "public",
		"scopes":     []string{"codewhisperer:completions", "codewhisperer:analysis", "codewhisperer:conversations"},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ssoOIDCEndpoint+"/client/register", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("register client failed (status %d): %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("register client failed (status %d)", resp.StatusCode)
	}

	var result RegisterClientResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// StartDeviceAuthorization starts the device authorization flow.
func (c *SSOOIDCClient) StartDeviceAuthorization(ctx context.Context, clientID, clientSecret string) (*StartDeviceAuthResponse, error) {
	payload := map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"startUrl":     builderIDStartURL,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ssoOIDCEndpoint+"/device_authorization", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("start device auth failed (status %d): %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("start device auth failed (status %d)", resp.StatusCode)
	}

	var result StartDeviceAuthResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// CreateToken polls for the access token after user authorization.
func (c *SSOOIDCClient) CreateToken(ctx context.Context, clientID, clientSecret, deviceCode string) (*CreateTokenResponse, error) {
	payload := map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"deviceCode":   deviceCode,
		"grantType":    "urn:ietf:params:oauth:grant-type:device_code",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ssoOIDCEndpoint+"/token", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// Check for pending authorization
	if resp.StatusCode == http.StatusBadRequest {
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(respBody, &errResp) == nil {
			if errResp.Error == "authorization_pending" {
				return nil, fmt.Errorf("authorization_pending")
			}
			if errResp.Error == "slow_down" {
				return nil, fmt.Errorf("slow_down")
			}
		}
		log.Debugf("create token failed: %s", string(respBody))
		return nil, fmt.Errorf("create token failed")
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("create token failed (status %d): %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("create token failed (status %d)", resp.StatusCode)
	}

	var result CreateTokenResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// RefreshToken refreshes an access token using the refresh token.
func (c *SSOOIDCClient) RefreshToken(ctx context.Context, clientID, clientSecret, refreshToken string) (*KiroTokenData, error) {
	payload := map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
		"refreshToken": refreshToken,
		"grantType":    "refresh_token",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ssoOIDCEndpoint+"/token", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		log.Debugf("token refresh failed (status %d): %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("token refresh failed (status %d)", resp.StatusCode)
	}

	var result CreateTokenResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}

	expiresAt := time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)

	return &KiroTokenData{
		AccessToken:  result.AccessToken,
		RefreshToken: result.RefreshToken,
		ExpiresAt:    expiresAt.Format(time.RFC3339),
		AuthMethod:   "builder-id",
		Provider:     "AWS",
		ClientID:     clientID,
		ClientSecret: clientSecret,
	}, nil
}

// LoginWithBuilderID performs the full device code flow for AWS Builder ID.
func (c *SSOOIDCClient) LoginWithBuilderID(ctx context.Context) (*KiroTokenData, error) {
	fmt.Println("\n╔══════════════════════════════════════════════════════════╗")
	fmt.Println("║         Kiro Authentication (AWS Builder ID)              ║")
	fmt.Println("╚══════════════════════════════════════════════════════════╝")

	// Step 1: Register client
	fmt.Println("\nRegistering client...")
	regResp, err := c.RegisterClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to register client: %w", err)
	}
	log.Debugf("Client registered: %s", regResp.ClientID)

	// Step 2: Start device authorization
	fmt.Println("Starting device authorization...")
	authResp, err := c.StartDeviceAuthorization(ctx, regResp.ClientID, regResp.ClientSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to start device auth: %w", err)
	}

	// Step 3: Show user the verification URL
	fmt.Printf("\n")
	fmt.Println("════════════════════════════════════════════════════════════")
	fmt.Printf("  Open this URL in your browser:\n")
	fmt.Printf("  %s\n", authResp.VerificationURIComplete)
	fmt.Println("════════════════════════════════════════════════════════════")
	fmt.Printf("\n  Or go to: %s\n", authResp.VerificationURI)
	fmt.Printf("  And enter code: %s\n\n", authResp.UserCode)

	// Set incognito mode based on config (defaults to true for Kiro, can be overridden with --no-incognito)
	// Incognito mode enables multi-account support by bypassing cached sessions
	if c.cfg != nil {
		browser.SetIncognitoMode(c.cfg.IncognitoBrowser)
		if !c.cfg.IncognitoBrowser {
			log.Info("kiro: using normal browser mode (--no-incognito). Note: You may not be able to select a different account.")
		} else {
			log.Debug("kiro: using incognito mode for multi-account support")
		}
	} else {
		browser.SetIncognitoMode(true) // Default to incognito if no config
		log.Debug("kiro: using incognito mode for multi-account support (default)")
	}

	// Open browser using cross-platform browser package
	if err := browser.OpenURL(authResp.VerificationURIComplete); err != nil {
		log.Warnf("Could not open browser automatically: %v", err)
		fmt.Println("  Please open the URL manually in your browser.")
	} else {
		fmt.Println("  (Browser opened automatically)")
	}

	// Step 4: Poll for token
	fmt.Println("Waiting for authorization...")

	interval := pollInterval
	if authResp.Interval > 0 {
		interval = time.Duration(authResp.Interval) * time.Second
	}

	deadline := time.Now().Add(time.Duration(authResp.ExpiresIn) * time.Second)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			browser.CloseBrowser() // Cleanup on cancel
			return nil, ctx.Err()
		case <-time.After(interval):
			tokenResp, err := c.CreateToken(ctx, regResp.ClientID, regResp.ClientSecret, authResp.DeviceCode)
			if err != nil {
				errStr := err.Error()
				if strings.Contains(errStr, "authorization_pending") {
					fmt.Print(".")
					continue
				}
				if strings.Contains(errStr, "slow_down") {
					interval += 5 * time.Second
					continue
				}
				// Close browser on error before returning
				browser.CloseBrowser()
				return nil, fmt.Errorf("token creation failed: %w", err)
			}

			fmt.Println("\n\n✓ Authorization successful!")

			// Close the browser window
			if err := browser.CloseBrowser(); err != nil {
				log.Debugf("Failed to close browser: %v", err)
			}

			// Step 5: Get profile ARN from CodeWhisperer API
			fmt.Println("Fetching profile information...")
			profileArn := c.fetchProfileArn(ctx, tokenResp.AccessToken)

			// Extract email from JWT access token
			email := ExtractEmailFromJWT(tokenResp.AccessToken)
			if email != "" {
				fmt.Printf("  Logged in as: %s\n", email)
			}

			expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

			return &KiroTokenData{
				AccessToken:  tokenResp.AccessToken,
				RefreshToken: tokenResp.RefreshToken,
				ProfileArn:   profileArn,
				ExpiresAt:    expiresAt.Format(time.RFC3339),
				AuthMethod:   "builder-id",
				Provider:     "AWS",
				ClientID:     regResp.ClientID,
				ClientSecret: regResp.ClientSecret,
				Email:        email,
			}, nil
		}
	}

	// Close browser on timeout for better UX
	if err := browser.CloseBrowser(); err != nil {
		log.Debugf("Failed to close browser on timeout: %v", err)
	}
	return nil, fmt.Errorf("authorization timed out")
}

// fetchProfileArn retrieves the profile ARN from CodeWhisperer API.
// This is needed for file naming since AWS SSO OIDC doesn't return profile info.
func (c *SSOOIDCClient) fetchProfileArn(ctx context.Context, accessToken string) string {
	// Try ListProfiles API first
	profileArn := c.tryListProfiles(ctx, accessToken)
	if profileArn != "" {
		return profileArn
	}

	// Fallback: Try ListAvailableCustomizations
	return c.tryListCustomizations(ctx, accessToken)
}

func (c *SSOOIDCClient) tryListProfiles(ctx context.Context, accessToken string) string {
	payload := map[string]interface{}{
		"origin": "AI_EDITOR",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return ""
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://codewhisperer.us-east-1.amazonaws.com", strings.NewReader(string(body)))
	if err != nil {
		return ""
	}

	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("x-amz-target", "AmazonCodeWhispererService.ListProfiles")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		log.Debugf("ListProfiles failed (status %d): %s", resp.StatusCode, string(respBody))
		return ""
	}

	log.Debugf("ListProfiles response: %s", string(respBody))

	var result struct {
		Profiles []struct {
			Arn string `json:"arn"`
		} `json:"profiles"`
		ProfileArn string `json:"profileArn"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return ""
	}

	if result.ProfileArn != "" {
		return result.ProfileArn
	}

	if len(result.Profiles) > 0 {
		return result.Profiles[0].Arn
	}

	return ""
}

func (c *SSOOIDCClient) tryListCustomizations(ctx context.Context, accessToken string) string {
	payload := map[string]interface{}{
		"origin": "AI_EDITOR",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return ""
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://codewhisperer.us-east-1.amazonaws.com", strings.NewReader(string(body)))
	if err != nil {
		return ""
	}

	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("x-amz-target", "AmazonCodeWhispererService.ListAvailableCustomizations")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		log.Debugf("ListAvailableCustomizations failed (status %d): %s", resp.StatusCode, string(respBody))
		return ""
	}

	log.Debugf("ListAvailableCustomizations response: %s", string(respBody))

	var result struct {
		Customizations []struct {
			Arn string `json:"arn"`
		} `json:"customizations"`
		ProfileArn string `json:"profileArn"`
	}

	if err := json.Unmarshal(respBody, &result); err != nil {
		return ""
	}

	if result.ProfileArn != "" {
		return result.ProfileArn
	}

	if len(result.Customizations) > 0 {
		return result.Customizations[0].Arn
	}

	return ""
}
