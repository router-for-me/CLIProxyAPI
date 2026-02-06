// Package kiro provides OAuth2 authentication functionality for AWS CodeWhisperer (Kiro) API.
// This package implements token loading, refresh, and API communication with CodeWhisperer.
package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	// awsKiroEndpoint is used for CodeWhisperer management APIs (GetUsageLimits, ListProfiles, etc.)
	// Note: This is different from the Amazon Q streaming endpoint (q.us-east-1.amazonaws.com)
	// used in kiro_executor.go for GenerateAssistantResponse. Both endpoints are correct
	// for their respective API operations.
	awsKiroEndpoint     = "https://codewhisperer.us-east-1.amazonaws.com"
	defaultTokenFile    = "~/.aws/sso/cache/kiro-auth-token.json"
	targetGetUsage      = "AmazonCodeWhispererService.GetUsageLimits"
	targetListModels    = "AmazonCodeWhispererService.ListAvailableModels"
	targetGenerateChat  = "AmazonCodeWhispererStreamingService.GenerateAssistantResponse"
)

// KiroAuth handles AWS CodeWhisperer authentication and API communication.
// It provides methods for loading tokens, refreshing expired tokens,
// and communicating with the CodeWhisperer API.
type KiroAuth struct {
	httpClient *http.Client
	endpoint   string
}

// NewKiroAuth creates a new Kiro authentication service.
// It initializes the HTTP client with proxy settings from the configuration.
//
// Parameters:
//   - cfg: The application configuration containing proxy settings
//
// Returns:
//   - *KiroAuth: A new Kiro authentication service instance
func NewKiroAuth(cfg *config.Config) *KiroAuth {
	return &KiroAuth{
		httpClient: util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 120 * time.Second}),
		endpoint:   awsKiroEndpoint,
	}
}

// LoadTokenFromFile loads token data from a file path.
// This method reads and parses the token file, expanding ~ to the home directory.
//
// Parameters:
//   - tokenFile: Path to the token file (supports ~ expansion)
//
// Returns:
//   - *KiroTokenData: The parsed token data
//   - error: An error if file reading or parsing fails
func (k *KiroAuth) LoadTokenFromFile(tokenFile string) (*KiroTokenData, error) {
	// Expand ~ to home directory
	if strings.HasPrefix(tokenFile, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		tokenFile = filepath.Join(home, tokenFile[1:])
	}

	data, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read token file: %w", err)
	}

	var tokenData KiroTokenData
	if err := json.Unmarshal(data, &tokenData); err != nil {
		return nil, fmt.Errorf("failed to parse token file: %w", err)
	}

	return &tokenData, nil
}

// IsTokenExpired checks if the token has expired.
// This method parses the expiration timestamp and compares it with the current time.
//
// Parameters:
//   - tokenData: The token data to check
//
// Returns:
//   - bool: True if the token has expired, false otherwise
func (k *KiroAuth) IsTokenExpired(tokenData *KiroTokenData) bool {
	if tokenData.ExpiresAt == "" {
		return true
	}

	expiresAt, err := time.Parse(time.RFC3339, tokenData.ExpiresAt)
	if err != nil {
		// Try alternate format
		expiresAt, err = time.Parse("2006-01-02T15:04:05.000Z", tokenData.ExpiresAt)
		if err != nil {
			return true
		}
	}

	return time.Now().After(expiresAt)
}

// makeRequest sends a request to the CodeWhisperer API.
// This is an internal method for making authenticated API calls.
//
// Parameters:
//   - ctx: The context for the request
//   - target: The API target (e.g., "AmazonCodeWhispererService.GetUsageLimits")
//   - accessToken: The OAuth access token
//   - payload: The request payload
//
// Returns:
//   - []byte: The response body
//   - error: An error if the request fails
func (k *KiroAuth) makeRequest(ctx context.Context, target string, accessToken string, payload interface{}) ([]byte, error) {
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, k.endpoint, strings.NewReader(string(jsonBody)))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("x-amz-target", target)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := k.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("failed to close response body: %v", errClose)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// GetUsageLimits retrieves usage information from the CodeWhisperer API.
// This method fetches the current usage statistics and subscription information.
//
// Parameters:
//   - ctx: The context for the request
//   - tokenData: The token data containing access token and profile ARN
//
// Returns:
//   - *KiroUsageInfo: The usage information
//   - error: An error if the request fails
func (k *KiroAuth) GetUsageLimits(ctx context.Context, tokenData *KiroTokenData) (*KiroUsageInfo, error) {
	payload := map[string]interface{}{
		"origin":       "AI_EDITOR",
		"profileArn":   tokenData.ProfileArn,
		"resourceType": "AGENTIC_REQUEST",
	}

	body, err := k.makeRequest(ctx, targetGetUsage, tokenData.AccessToken, payload)
	if err != nil {
		return nil, err
	}

	var result struct {
		SubscriptionInfo struct {
			SubscriptionTitle string `json:"subscriptionTitle"`
		} `json:"subscriptionInfo"`
		UsageBreakdownList []struct {
			CurrentUsageWithPrecision float64 `json:"currentUsageWithPrecision"`
			UsageLimitWithPrecision   float64 `json:"usageLimitWithPrecision"`
		} `json:"usageBreakdownList"`
		NextDateReset float64 `json:"nextDateReset"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse usage response: %w", err)
	}

	usage := &KiroUsageInfo{
		SubscriptionTitle: result.SubscriptionInfo.SubscriptionTitle,
		NextReset:         fmt.Sprintf("%v", result.NextDateReset),
	}

	if len(result.UsageBreakdownList) > 0 {
		usage.CurrentUsage = result.UsageBreakdownList[0].CurrentUsageWithPrecision
		usage.UsageLimit = result.UsageBreakdownList[0].UsageLimitWithPrecision
	}

	return usage, nil
}

// ListAvailableModels retrieves available models from the CodeWhisperer API.
// This method fetches the list of AI models available for the authenticated user.
//
// Parameters:
//   - ctx: The context for the request
//   - tokenData: The token data containing access token and profile ARN
//
// Returns:
//   - []*KiroModel: The list of available models
//   - error: An error if the request fails
func (k *KiroAuth) ListAvailableModels(ctx context.Context, tokenData *KiroTokenData) ([]*KiroModel, error) {
	payload := map[string]interface{}{
		"origin":     "AI_EDITOR",
		"profileArn": tokenData.ProfileArn,
	}

	body, err := k.makeRequest(ctx, targetListModels, tokenData.AccessToken, payload)
	if err != nil {
		return nil, err
	}

	var result struct {
		Models []struct {
			ModelID        string  `json:"modelId"`
			ModelName      string  `json:"modelName"`
			Description    string  `json:"description"`
			RateMultiplier float64 `json:"rateMultiplier"`
			RateUnit       string  `json:"rateUnit"`
			TokenLimits    struct {
				MaxInputTokens int `json:"maxInputTokens"`
			} `json:"tokenLimits"`
		} `json:"models"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse models response: %w", err)
	}

	models := make([]*KiroModel, 0, len(result.Models))
	for _, m := range result.Models {
		models = append(models, &KiroModel{
			ModelID:        m.ModelID,
			ModelName:      m.ModelName,
			Description:    m.Description,
			RateMultiplier: m.RateMultiplier,
			RateUnit:       m.RateUnit,
			MaxInputTokens: m.TokenLimits.MaxInputTokens,
		})
	}

	return models, nil
}

// CreateTokenStorage creates a new KiroTokenStorage from token data.
// This method converts the token data into a storage structure suitable for persistence.
//
// Parameters:
//   - tokenData: The token data to convert
//
// Returns:
//   - *KiroTokenStorage: A new token storage instance
func (k *KiroAuth) CreateTokenStorage(tokenData *KiroTokenData) *KiroTokenStorage {
	return &KiroTokenStorage{
		AccessToken:  tokenData.AccessToken,
		RefreshToken: tokenData.RefreshToken,
		ProfileArn:   tokenData.ProfileArn,
		ExpiresAt:    tokenData.ExpiresAt,
		AuthMethod:   tokenData.AuthMethod,
		Provider:     tokenData.Provider,
		LastRefresh:  time.Now().Format(time.RFC3339),
		ClientID:     tokenData.ClientID,
		ClientSecret: tokenData.ClientSecret,
		Region:       tokenData.Region,
		StartURL:     tokenData.StartURL,
		Email:        tokenData.Email,
	}
}

// ValidateToken checks if the token is valid by making a test API call.
// This method verifies the token by attempting to fetch usage limits.
//
// Parameters:
//   - ctx: The context for the request
//   - tokenData: The token data to validate
//
// Returns:
//   - error: An error if the token is invalid
func (k *KiroAuth) ValidateToken(ctx context.Context, tokenData *KiroTokenData) error {
	_, err := k.GetUsageLimits(ctx, tokenData)
	return err
}

// UpdateTokenStorage updates an existing token storage with new token data.
// This method refreshes the token storage with newly obtained access and refresh tokens.
//
// Parameters:
//   - storage: The existing token storage to update
//   - tokenData: The new token data to apply
func (k *KiroAuth) UpdateTokenStorage(storage *KiroTokenStorage, tokenData *KiroTokenData) {
	storage.AccessToken = tokenData.AccessToken
	storage.RefreshToken = tokenData.RefreshToken
	storage.ProfileArn = tokenData.ProfileArn
	storage.ExpiresAt = tokenData.ExpiresAt
	storage.AuthMethod = tokenData.AuthMethod
	storage.Provider = tokenData.Provider
	storage.LastRefresh = time.Now().Format(time.RFC3339)
	if tokenData.ClientID != "" {
		storage.ClientID = tokenData.ClientID
	}
	if tokenData.ClientSecret != "" {
		storage.ClientSecret = tokenData.ClientSecret
	}
	if tokenData.Region != "" {
		storage.Region = tokenData.Region
	}
	if tokenData.StartURL != "" {
		storage.StartURL = tokenData.StartURL
	}
	if tokenData.Email != "" {
		storage.Email = tokenData.Email
	}
}
