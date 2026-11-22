package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// DefaultUsageURL is the CodeWhisperer endpoint used by kiro-cli `/usage`.
const DefaultUsageURL = "https://q.us-east-1.amazonaws.com/getUsageLimits"

// UsageBreakdown is one metered resource (e.g. credits) reported by GetUsageLimits.
type UsageBreakdown struct {
	DisplayName               string   `json:"displayName"`
	DisplayNamePlural         string   `json:"displayNamePlural"`
	ResourceType              string   `json:"resourceType"`
	Unit                      string   `json:"unit"`
	Currency                  string   `json:"currency"`
	CurrentUsage              float64  `json:"currentUsage"`
	CurrentUsageWithPrecision *float64 `json:"currentUsageWithPrecision"`
	UsageLimit                float64  `json:"usageLimit"`
	UsageLimitWithPrecision   *float64 `json:"usageLimitWithPrecision"`
	CurrentOverages           float64  `json:"currentOverages"`
	OverageCharges            float64  `json:"overageCharges"`
	NextDateReset             float64  `json:"nextDateReset"`
	FreeTrialInfo             *struct {
		FreeTrialStatus           string   `json:"freeTrialStatus"`
		UsageLimit                float64  `json:"usageLimit"`
		UsageLimitWithPrecision   *float64 `json:"usageLimitWithPrecision"`
		CurrentUsage              float64  `json:"currentUsage"`
		CurrentUsageWithPrecision *float64 `json:"currentUsageWithPrecision"`
		FreeTrialExpiry           float64  `json:"freeTrialExpiry"`
	} `json:"freeTrialInfo"`
}

// Used returns the precise current usage.
func (b UsageBreakdown) Used() float64 {
	if b.CurrentUsageWithPrecision != nil {
		return *b.CurrentUsageWithPrecision
	}
	return b.CurrentUsage
}

// Limit returns the precise usage limit.
func (b UsageBreakdown) Limit() float64 {
	if b.UsageLimitWithPrecision != nil {
		return *b.UsageLimitWithPrecision
	}
	return b.UsageLimit
}

// UsageLimits models the GetUsageLimits response.
type UsageLimits struct {
	DaysUntilReset   int     `json:"daysUntilReset"`
	NextDateReset    float64 `json:"nextDateReset"`
	SubscriptionInfo struct {
		SubscriptionTitle string `json:"subscriptionTitle"`
		Type              string `json:"type"`
	} `json:"subscriptionInfo"`
	OverageConfiguration struct {
		OverageStatus string `json:"overageStatus"`
	} `json:"overageConfiguration"`
	UsageBreakdownList []UsageBreakdown `json:"usageBreakdownList"`
}

// UsageHTTPError reports a non-2xx response from GetUsageLimits.
type UsageHTTPError struct {
	StatusCode int
	Body       string
}

func (e *UsageHTTPError) Error() string {
	return fmt.Sprintf("kiro usage: status %d: %s", e.StatusCode, e.Body)
}

// GetUsageLimits fetches the account entitlements and current usage for a Kiro token.
func (s *KiroAuthService) GetUsageLimits(ctx context.Context, accessToken, profileArn string) (*UsageLimits, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("kiro usage: access token is required")
	}
	q := url.Values{}
	q.Set("origin", "AI_EDITOR")
	q.Set("resourceType", "AGENTIC_REQUEST")
	if arn := strings.TrimSpace(profileArn); arn != "" {
		q.Set("profileArn", arn)
	}
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, DefaultUsageURL+"?"+q.Encode(), nil)
	if errReq != nil {
		return nil, fmt.Errorf("kiro usage: failed to create request: %w", errReq)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, errDo := s.client.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("kiro usage: request failed: %w", errDo)
	}
	defer resp.Body.Close()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		return nil, fmt.Errorf("kiro usage: failed to read response: %w", errRead)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, &UsageHTTPError{StatusCode: resp.StatusCode, Body: msg}
	}
	var out UsageLimits
	if errUnmarshal := json.Unmarshal(body, &out); errUnmarshal != nil {
		return nil, fmt.Errorf("kiro usage: failed to decode response: %w", errUnmarshal)
	}
	return &out, nil
}
