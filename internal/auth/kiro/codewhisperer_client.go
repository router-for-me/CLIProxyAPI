// Package kiro provides CodeWhisperer API client for fetching user info.
package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	codeWhispererAPI = "https://codewhisperer.us-east-1.amazonaws.com"
	kiroVersion      = "0.6.18"
)

// CodeWhispererClient handles CodeWhisperer API calls.
type CodeWhispererClient struct {
	httpClient *http.Client
	machineID  string
}

// UsageLimitsResponse represents the getUsageLimits API response.
type UsageLimitsResponse struct {
	DaysUntilReset     *int                `json:"daysUntilReset,omitempty"`
	NextDateReset      *float64            `json:"nextDateReset,omitempty"`
	UserInfo           *UserInfo           `json:"userInfo,omitempty"`
	SubscriptionInfo   *SubscriptionInfo   `json:"subscriptionInfo,omitempty"`
	UsageBreakdownList []UsageBreakdown    `json:"usageBreakdownList,omitempty"`
}

// UserInfo contains user information from the API.
type UserInfo struct {
	Email  string `json:"email,omitempty"`
	UserID string `json:"userId,omitempty"`
}

// SubscriptionInfo contains subscription details.
type SubscriptionInfo struct {
	SubscriptionTitle string `json:"subscriptionTitle,omitempty"`
	Type              string `json:"type,omitempty"`
}

// UsageBreakdown contains usage details.
type UsageBreakdown struct {
	UsageLimit                 *int     `json:"usageLimit,omitempty"`
	CurrentUsage               *int     `json:"currentUsage,omitempty"`
	UsageLimitWithPrecision    *float64 `json:"usageLimitWithPrecision,omitempty"`
	CurrentUsageWithPrecision  *float64 `json:"currentUsageWithPrecision,omitempty"`
	NextDateReset              *float64 `json:"nextDateReset,omitempty"`
	DisplayName                string   `json:"displayName,omitempty"`
	ResourceType               string   `json:"resourceType,omitempty"`
}

// NewCodeWhispererClient creates a new CodeWhisperer client.
func NewCodeWhispererClient(cfg *config.Config, machineID string) *CodeWhispererClient {
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		client = util.SetProxy(&cfg.SDKConfig, client)
	}
	if machineID == "" {
		machineID = uuid.New().String()
	}
	return &CodeWhispererClient{
		httpClient: client,
		machineID:  machineID,
	}
}

// generateInvocationID generates a unique invocation ID.
func generateInvocationID() string {
	return uuid.New().String()
}

// GetUsageLimits fetches usage limits and user info from CodeWhisperer API.
// This is the recommended way to get user email after login.
func (c *CodeWhispererClient) GetUsageLimits(ctx context.Context, accessToken string) (*UsageLimitsResponse, error) {
	url := fmt.Sprintf("%s/getUsageLimits?isEmailRequired=true&origin=AI_EDITOR&resourceType=AGENTIC_REQUEST", codeWhispererAPI)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers to match Kiro IDE
	xAmzUserAgent := fmt.Sprintf("aws-sdk-js/1.0.0 KiroIDE-%s-%s", kiroVersion, c.machineID)
	userAgent := fmt.Sprintf("aws-sdk-js/1.0.0 ua/2.1 os/windows lang/js md/nodejs#20.16.0 api/codewhispererruntime#1.0.0 m/E KiroIDE-%s-%s", kiroVersion, c.machineID)

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("x-amz-user-agent", xAmzUserAgent)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("amz-sdk-invocation-id", generateInvocationID())
	req.Header.Set("amz-sdk-request", "attempt=1; max=1")
	req.Header.Set("Connection", "close")

	log.Debugf("codewhisperer: GET %s", url)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	log.Debugf("codewhisperer: status=%d, body=%s", resp.StatusCode, string(body))

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var result UsageLimitsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &result, nil
}

// FetchUserEmailFromAPI fetches user email using CodeWhisperer getUsageLimits API.
// This is more reliable than JWT parsing as it uses the official API.
func (c *CodeWhispererClient) FetchUserEmailFromAPI(ctx context.Context, accessToken string) string {
	resp, err := c.GetUsageLimits(ctx, accessToken)
	if err != nil {
		log.Debugf("codewhisperer: failed to get usage limits: %v", err)
		return ""
	}

	if resp.UserInfo != nil && resp.UserInfo.Email != "" {
		log.Debugf("codewhisperer: got email from API: %s", resp.UserInfo.Email)
		return resp.UserInfo.Email
	}

	log.Debugf("codewhisperer: no email in response")
	return ""
}

// FetchUserEmailWithFallback fetches user email with multiple fallback methods.
// Priority: 1. CodeWhisperer API  2. userinfo endpoint  3. JWT parsing
func FetchUserEmailWithFallback(ctx context.Context, cfg *config.Config, accessToken string) string {
	// Method 1: Try CodeWhisperer API (most reliable)
	cwClient := NewCodeWhispererClient(cfg, "")
	email := cwClient.FetchUserEmailFromAPI(ctx, accessToken)
	if email != "" {
		return email
	}

	// Method 2: Try SSO OIDC userinfo endpoint
	ssoClient := NewSSOOIDCClient(cfg)
	email = ssoClient.FetchUserEmail(ctx, accessToken)
	if email != "" {
		return email
	}

	// Method 3: Fallback to JWT parsing
	return ExtractEmailFromJWT(accessToken)
}
