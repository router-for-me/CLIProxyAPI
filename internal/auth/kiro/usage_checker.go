// Package kiro provides authentication functionality for AWS CodeWhisperer (Kiro) API.
// This file implements usage quota checking and monitoring.
package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

// UsageQuotaResponse represents the API response structure for usage quota checking.
type UsageQuotaResponse struct {
	UsageBreakdownList []UsageBreakdownExtended `json:"usageBreakdownList"`
	SubscriptionInfo   *SubscriptionInfo        `json:"subscriptionInfo,omitempty"`
	NextDateReset      float64                  `json:"nextDateReset,omitempty"`
}

// UsageBreakdownExtended represents detailed usage information for quota checking.
// Note: UsageBreakdown is already defined in codewhisperer_client.go
type UsageBreakdownExtended struct {
	ResourceType              string                 `json:"resourceType"`
	UsageLimitWithPrecision   float64                `json:"usageLimitWithPrecision"`
	CurrentUsageWithPrecision float64                `json:"currentUsageWithPrecision"`
	FreeTrialInfo             *FreeTrialInfoExtended `json:"freeTrialInfo,omitempty"`
}

// FreeTrialInfoExtended represents free trial usage information.
type FreeTrialInfoExtended struct {
	FreeTrialStatus           string  `json:"freeTrialStatus"`
	UsageLimitWithPrecision   float64 `json:"usageLimitWithPrecision"`
	CurrentUsageWithPrecision float64 `json:"currentUsageWithPrecision"`
}

// QuotaStatus represents the quota status for a token.
type QuotaStatus struct {
	TotalLimit     float64
	CurrentUsage   float64
	RemainingQuota float64
	IsExhausted    bool
	ResourceType   string
	NextReset      time.Time
}

// UsageChecker provides methods for checking token quota usage.
type UsageChecker struct {
	httpClient *http.Client
	endpoint   string
}

// NewUsageChecker creates a new UsageChecker instance.
func NewUsageChecker(cfg *config.Config) *UsageChecker {
	return &UsageChecker{
		httpClient: util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second}),
		endpoint:   awsKiroEndpoint,
	}
}

// NewUsageCheckerWithClient creates a UsageChecker with a custom HTTP client.
func NewUsageCheckerWithClient(client *http.Client) *UsageChecker {
	return &UsageChecker{
		httpClient: client,
		endpoint:   awsKiroEndpoint,
	}
}

// CheckUsage retrieves usage limits for the given token.
func (c *UsageChecker) CheckUsage(ctx context.Context, tokenData *KiroTokenData) (*UsageQuotaResponse, error) {
	if tokenData == nil {
		return nil, fmt.Errorf("token data is nil")
	}

	if tokenData.AccessToken == "" {
		return nil, fmt.Errorf("access token is empty")
	}

	payload := map[string]interface{}{
		"origin":       "AI_EDITOR",
		"profileArn":   tokenData.ProfileArn,
		"resourceType": "AGENTIC_REQUEST",
	}

	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, strings.NewReader(string(jsonBody)))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("x-amz-target", targetGetUsage)
	req.Header.Set("Authorization", "Bearer "+tokenData.AccessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var result UsageQuotaResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse usage response: %w", err)
	}

	return &result, nil
}

// CheckUsageByAccessToken retrieves usage limits using an access token and profile ARN directly.
func (c *UsageChecker) CheckUsageByAccessToken(ctx context.Context, accessToken, profileArn string) (*UsageQuotaResponse, error) {
	tokenData := &KiroTokenData{
		AccessToken: accessToken,
		ProfileArn:  profileArn,
	}
	return c.CheckUsage(ctx, tokenData)
}

// GetRemainingQuota calculates the remaining quota from usage limits.
func GetRemainingQuota(usage *UsageQuotaResponse) float64 {
	if usage == nil || len(usage.UsageBreakdownList) == 0 {
		return 0
	}

	var totalRemaining float64
	for _, breakdown := range usage.UsageBreakdownList {
		remaining := breakdown.UsageLimitWithPrecision - breakdown.CurrentUsageWithPrecision
		if remaining > 0 {
			totalRemaining += remaining
		}

		if breakdown.FreeTrialInfo != nil {
			freeRemaining := breakdown.FreeTrialInfo.UsageLimitWithPrecision - breakdown.FreeTrialInfo.CurrentUsageWithPrecision
			if freeRemaining > 0 {
				totalRemaining += freeRemaining
			}
		}
	}

	return totalRemaining
}

// IsQuotaExhausted checks if the quota is exhausted based on usage limits.
func IsQuotaExhausted(usage *UsageQuotaResponse) bool {
	if usage == nil || len(usage.UsageBreakdownList) == 0 {
		return true
	}

	for _, breakdown := range usage.UsageBreakdownList {
		if breakdown.CurrentUsageWithPrecision < breakdown.UsageLimitWithPrecision {
			return false
		}

		if breakdown.FreeTrialInfo != nil {
			if breakdown.FreeTrialInfo.CurrentUsageWithPrecision < breakdown.FreeTrialInfo.UsageLimitWithPrecision {
				return false
			}
		}
	}

	return true
}

// GetQuotaStatus retrieves a comprehensive quota status for a token.
func (c *UsageChecker) GetQuotaStatus(ctx context.Context, tokenData *KiroTokenData) (*QuotaStatus, error) {
	usage, err := c.CheckUsage(ctx, tokenData)
	if err != nil {
		return nil, err
	}

	status := &QuotaStatus{
		IsExhausted: IsQuotaExhausted(usage),
	}

	if len(usage.UsageBreakdownList) > 0 {
		breakdown := usage.UsageBreakdownList[0]
		status.TotalLimit = breakdown.UsageLimitWithPrecision
		status.CurrentUsage = breakdown.CurrentUsageWithPrecision
		status.RemainingQuota = breakdown.UsageLimitWithPrecision - breakdown.CurrentUsageWithPrecision
		status.ResourceType = breakdown.ResourceType

		if breakdown.FreeTrialInfo != nil {
			status.TotalLimit += breakdown.FreeTrialInfo.UsageLimitWithPrecision
			status.CurrentUsage += breakdown.FreeTrialInfo.CurrentUsageWithPrecision
			freeRemaining := breakdown.FreeTrialInfo.UsageLimitWithPrecision - breakdown.FreeTrialInfo.CurrentUsageWithPrecision
			if freeRemaining > 0 {
				status.RemainingQuota += freeRemaining
			}
		}
	}

	if usage.NextDateReset > 0 {
		status.NextReset = time.Unix(int64(usage.NextDateReset/1000), 0)
	}

	return status, nil
}

// CalculateAvailableCount calculates the available request count based on usage limits.
func CalculateAvailableCount(usage *UsageQuotaResponse) float64 {
	return GetRemainingQuota(usage)
}

// GetUsagePercentage calculates the usage percentage.
func GetUsagePercentage(usage *UsageQuotaResponse) float64 {
	if usage == nil || len(usage.UsageBreakdownList) == 0 {
		return 100.0
	}

	var totalLimit, totalUsage float64
	for _, breakdown := range usage.UsageBreakdownList {
		totalLimit += breakdown.UsageLimitWithPrecision
		totalUsage += breakdown.CurrentUsageWithPrecision

		if breakdown.FreeTrialInfo != nil {
			totalLimit += breakdown.FreeTrialInfo.UsageLimitWithPrecision
			totalUsage += breakdown.FreeTrialInfo.CurrentUsageWithPrecision
		}
	}

	if totalLimit == 0 {
		return 100.0
	}

	return (totalUsage / totalLimit) * 100
}
