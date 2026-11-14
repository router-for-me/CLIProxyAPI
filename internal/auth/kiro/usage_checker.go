package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

const (
	pathGetUsageLimits = "getUsageLimits"
)

type UsageQuotaResponse struct {
	DaysUntilReset     *int              `json:"daysUntilReset,omitempty"`
	NextDateReset      *float64          `json:"nextDateReset,omitempty"`
	UserInfo           *UserInfo         `json:"userInfo,omitempty"`
	SubscriptionInfo   *SubscriptionInfo `json:"subscriptionInfo,omitempty"`
	UsageBreakdownList []UsageBreakdown  `json:"usageBreakdownList,omitempty"`
}

type UserInfo struct {
	Email  string `json:"email,omitempty"`
	UserID string `json:"userId,omitempty"`
}

type SubscriptionInfo struct {
	SubscriptionTitle string `json:"subscriptionTitle,omitempty"`
	Type              string `json:"type,omitempty"`
}

type UsageBreakdown struct {
	UsageLimit                *int               `json:"usageLimit,omitempty"`
	CurrentUsage              *int               `json:"currentUsage,omitempty"`
	UsageLimitWithPrecision   *float64           `json:"usageLimitWithPrecision,omitempty"`
	CurrentUsageWithPrecision *float64           `json:"currentUsageWithPrecision,omitempty"`
	NextDateReset             *float64           `json:"nextDateReset,omitempty"`
	DisplayName               string             `json:"displayName,omitempty"`
	ResourceType              string             `json:"resourceType,omitempty"`
	FreeTrialInfo             *FreeTrialInfoData `json:"freeTrialInfo,omitempty"`
}

type FreeTrialInfoData struct {
	FreeTrialStatus           string   `json:"freeTrialStatus,omitempty"`
	UsageLimitWithPrecision   *float64 `json:"usageLimitWithPrecision,omitempty"`
	CurrentUsageWithPrecision *float64 `json:"currentUsageWithPrecision,omitempty"`
}

func (u *UsageBreakdown) Limit() float64 {
	if u.UsageLimitWithPrecision != nil {
		return *u.UsageLimitWithPrecision
	}
	if u.UsageLimit != nil {
		return float64(*u.UsageLimit)
	}
	return 0
}

func (u *UsageBreakdown) Used() float64 {
	if u.CurrentUsageWithPrecision != nil {
		return *u.CurrentUsageWithPrecision
	}
	if u.CurrentUsage != nil {
		return float64(*u.CurrentUsage)
	}
	return 0
}

type UsageChecker struct {
	httpClient *http.Client
	cfg        *config.Config
	apiBaseURL string
}

func (c *UsageChecker) SetAPIBaseURL(u string) {
	c.apiBaseURL = u
}

func NewUsageChecker(cfg *config.Config) *UsageChecker {
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		client = util.SetProxy(&cfg.SDKConfig, client)
	}
	return &UsageChecker{
		httpClient: client,
		cfg:        cfg,
	}
}

func (c *UsageChecker) CheckUsage(ctx context.Context, tokenData *KiroTokenData) (*UsageQuotaResponse, error) {
	if tokenData == nil {
		return nil, fmt.Errorf("token data is nil")
	}
	if tokenData.AccessToken == "" {
		return nil, fmt.Errorf("access token is empty")
	}

	queryParams := map[string]string{
		"origin":       "AI_EDITOR",
		"resourceType": "AGENTIC_REQUEST",
	}

	if tokenData.ProfileArn != "" {
		queryParams["profileArn"] = tokenData.ProfileArn
	} else {
		queryParams["isEmailRequired"] = "true"
	}

	endpoint := c.apiBaseURL
	if endpoint == "" {
		endpoint = tokenData.BaseURL
	}
	if endpoint == "" {
		endpoint = GetKiroAPIEndpointFromProfileArn(tokenData.ProfileArn)
	}
	requestURL := buildURL(endpoint, pathGetUsageLimits, queryParams)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create usage limits request: %w", err)
	}

	accountKey := GetAccountKey(tokenData.ClientID, tokenData.RefreshToken)
	setRuntimeHeaders(req, tokenData.AccessToken, accountKey)
	req.Header.Set("Accept", "application/json, application/cbor")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("usage limits request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read usage limits response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var result UsageQuotaResponse
	if errJSON := json.Unmarshal(body, &result); errJSON == nil {
		return &result, nil
	}

	if errCBOR := cbor.Unmarshal(body, &result); errCBOR == nil {
		return &result, nil
	}

	return nil, fmt.Errorf("failed to parse usage limits response as json or cbor")
}

func NormalizeQuotaResponse(resp *UsageQuotaResponse) pluginapi.QuotaFetchResponse {
	out := pluginapi.QuotaFetchResponse{}
	if resp == nil {
		return out
	}

	planName := "Kiro"
	if resp.SubscriptionInfo != nil {
		if resp.SubscriptionInfo.SubscriptionTitle != "" {
			planName = resp.SubscriptionInfo.SubscriptionTitle
		} else if resp.SubscriptionInfo.Type != "" {
			planName = resp.SubscriptionInfo.Type
		}
	}
	out.Subscription = &pluginapi.QuotaSubscription{
		Plan: planName,
	}

	groups := make([]pluginapi.QuotaGroup, 0, len(resp.UsageBreakdownList))
	summaryMetrics := make([]pluginapi.QuotaMetric, 0)

	for _, item := range resp.UsageBreakdownList {
		limit := item.Limit()
		used := item.Used()

		if item.FreeTrialInfo != nil {
			if item.FreeTrialInfo.UsageLimitWithPrecision != nil {
				limit += *item.FreeTrialInfo.UsageLimitWithPrecision
			}
			if item.FreeTrialInfo.CurrentUsageWithPrecision != nil {
				used += *item.FreeTrialInfo.CurrentUsageWithPrecision
			}
		}

		remaining := limit - used
		if remaining < 0 {
			remaining = 0
		}

		var remainingFraction float64
		if limit > 0 {
			remainingFraction = remaining / limit
		}

		var resetTimeStr string
		var nextResetSec float64
		if item.NextDateReset != nil && *item.NextDateReset > 0 {
			nextResetSec = *item.NextDateReset
		} else if resp.NextDateReset != nil && *resp.NextDateReset > 0 {
			nextResetSec = *resp.NextDateReset
		}

		if nextResetSec > 0 {
			resetTimeStr = time.Unix(int64(nextResetSec), 0).UTC().Format(time.RFC3339)
		}

		displayName := item.DisplayName
		if displayName == "" {
			displayName = item.ResourceType
		}
		if displayName == "" {
			displayName = "Agentic Requests"
		}

		bucketDesc := fmt.Sprintf("%.0f / %.0f requests remaining", remaining, limit)
		if limit == 0 {
			bucketDesc = fmt.Sprintf("%.0f requests used", used)
		}

		bucket := pluginapi.QuotaBucket{
			Window:            "monthly",
			RemainingFraction: remainingFraction,
			ResetTime:         resetTimeStr,
			Description:       bucketDesc,
		}

		groups = append(groups, pluginapi.QuotaGroup{
			DisplayName: displayName,
			Buckets:     []pluginapi.QuotaBucket{bucket},
		})

		metricPrefix := strings.ToLower(strings.ReplaceAll(displayName, " ", "_"))
		summaryMetrics = append(summaryMetrics,
			pluginapi.QuotaMetric{
				Key:    metricPrefix + "_used",
				Label:  displayName + " Used",
				Value:  used,
				Format: "number",
			},
			pluginapi.QuotaMetric{
				Key:    metricPrefix + "_limit",
				Label:  displayName + " Limit",
				Value:  limit,
				Format: "number",
			},
			pluginapi.QuotaMetric{
				Key:    metricPrefix + "_remaining",
				Label:  displayName + " Remaining",
				Value:  remaining,
				Format: "number",
			},
		)
	}

	out.Groups = groups
	out.Summary = summaryMetrics
	return out
}

func isTokenExpiredOrNearExpiry(tokenData *KiroTokenData) bool {
	if tokenData == nil || tokenData.ExpiresAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, tokenData.ExpiresAt)
	if err != nil {
		return false
	}
	return time.Now().After(t.Add(-5 * time.Minute))
}

func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "401") ||
		strings.Contains(msg, "403") ||
		strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "not authorized") ||
		strings.Contains(msg, "token expired") ||
		strings.Contains(msg, "expired")
}

func tryRefreshToken(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, tokenData *KiroTokenData) (*KiroTokenData, error) {
	if tokenData == nil || tokenData.RefreshToken == "" {
		return nil, fmt.Errorf("missing refresh token")
	}

	clientID := tokenData.ClientID
	clientSecret := tokenData.ClientSecret

	if (clientID == "" || clientSecret == "") && tokenData.ClientIDHash != "" {
		if cID, cSec, err := LoadDeviceRegistrationCredentials(tokenData.ClientIDHash); err == nil {
			clientID = cID
			clientSecret = cSec
		}
	}

	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("client_id or client_secret missing")
	}

	ssoClient := NewSSOOIDCClient(cfg)
	var newTokens *KiroTokenData
	var err error
	if tokenData.AuthMethod == "idc" && tokenData.Region != "" {
		newTokens, err = ssoClient.RefreshTokenWithRegion(ctx, clientID, clientSecret, tokenData.RefreshToken, tokenData.Region, tokenData.StartURL)
	} else {
		newTokens, err = ssoClient.RefreshToken(ctx, clientID, clientSecret, tokenData.RefreshToken)
	}
	if err != nil {
		return nil, err
	}

	tokenData.AccessToken = newTokens.AccessToken
	if newTokens.RefreshToken != "" {
		tokenData.RefreshToken = newTokens.RefreshToken
	}
	tokenData.ExpiresAt = newTokens.ExpiresAt

	if auth != nil && auth.Metadata != nil {
		auth.Metadata["access_token"] = newTokens.AccessToken
		auth.Metadata["refresh_token"] = tokenData.RefreshToken
		auth.Metadata["expires_at"] = newTokens.ExpiresAt
		auth.Metadata["last_refresh"] = time.Now().UTC().Format(time.RFC3339)
	}
	return tokenData, nil
}

func FetchNormalizedQuota(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) (pluginapi.QuotaFetchResponse, error) {
	if auth == nil {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("auth record is nil")
	}

	tokenData := TokenDataFromAuth(auth)
	if tokenData == nil {
		return pluginapi.QuotaFetchResponse{}, fmt.Errorf("failed to extract kiro token data from auth record")
	}

	if isTokenExpiredOrNearExpiry(tokenData) {
		if refreshed, errRef := tryRefreshToken(ctx, cfg, auth, tokenData); errRef == nil {
			tokenData = refreshed
		}
	}

	checker := NewUsageChecker(cfg)
	usageResp, err := checker.CheckUsage(ctx, tokenData)
	if err != nil {
		if isAuthError(err) {
			if refreshed, errRef := tryRefreshToken(ctx, cfg, auth, tokenData); errRef == nil {
				tokenData = refreshed
				usageResp, err = checker.CheckUsage(ctx, tokenData)
			}
		}
	}
	if err != nil {
		log.Warnf("kiro CheckUsage failed: %v", err)
		return pluginapi.QuotaFetchResponse{}, err
	}

	quotaResp := NormalizeQuotaResponse(usageResp)

	now := time.Now().UTC()
	auth.Quota.ObservedAt = now
	isExhausted := true
	if len(quotaResp.Groups) > 0 {
		for _, g := range quotaResp.Groups {
			for _, b := range g.Buckets {
				if b.RemainingFraction > 0 {
					isExhausted = false
					break
				}
			}
		}
	} else {
		isExhausted = false
	}

	if isExhausted {
		auth.Quota.Exceeded = true
		auth.Quota.Reason = "quota"
	} else {
		auth.Quota.Exceeded = false
		auth.Quota.Reason = ""
	}

	return quotaResp, nil
}

func TokenDataFromAuth(auth *coreauth.Auth) *KiroTokenData {
	if auth == nil {
		return nil
	}
	if storage, ok := auth.Storage.(*KiroTokenStorage); ok && storage != nil {
		return storage.ToTokenData()
	}
	if auth.Metadata != nil {
		td := &KiroTokenData{}
		if v, ok := auth.Metadata["access_token"].(string); ok {
			td.AccessToken = v
		} else if v, ok := auth.Metadata["accessToken"].(string); ok {
			td.AccessToken = v
		}
		if v, ok := auth.Metadata["refresh_token"].(string); ok {
			td.RefreshToken = v
		} else if v, ok := auth.Metadata["refreshToken"].(string); ok {
			td.RefreshToken = v
		}
		if v, ok := auth.Metadata["profile_arn"].(string); ok {
			td.ProfileArn = v
		} else if v, ok := auth.Metadata["profileArn"].(string); ok {
			td.ProfileArn = v
		}
		if v, ok := auth.Metadata["client_id"].(string); ok {
			td.ClientID = v
		} else if v, ok := auth.Metadata["clientId"].(string); ok {
			td.ClientID = v
		}
		if v, ok := auth.Metadata["client_secret"].(string); ok {
			td.ClientSecret = v
		} else if v, ok := auth.Metadata["clientSecret"].(string); ok {
			td.ClientSecret = v
		}
		if v, ok := auth.Metadata["client_id_hash"].(string); ok {
			td.ClientIDHash = v
		} else if v, ok := auth.Metadata["clientIdHash"].(string); ok {
			td.ClientIDHash = v
		}
		if v, ok := auth.Metadata["region"].(string); ok {
			td.Region = v
		}
		if v, ok := auth.Metadata["start_url"].(string); ok {
			td.StartURL = v
		} else if v, ok := auth.Metadata["startUrl"].(string); ok {
			td.StartURL = v
		}
		if v, ok := auth.Metadata["auth_method"].(string); ok {
			td.AuthMethod = v
		} else if v, ok := auth.Metadata["authMethod"].(string); ok {
			td.AuthMethod = v
		}
		if v, ok := auth.Metadata["provider"].(string); ok {
			td.Provider = v
		}
		if v, ok := auth.Metadata["email"].(string); ok {
			td.Email = v
		}
		if v, ok := auth.Metadata["expires_at"].(string); ok {
			td.ExpiresAt = v
		} else if v, ok := auth.Metadata["expiresAt"].(string); ok {
			td.ExpiresAt = v
		}
		if v, ok := auth.Metadata["base_url"].(string); ok {
			td.BaseURL = v
		} else if v, ok := auth.Metadata["api_url"].(string); ok {
			td.BaseURL = v
		}
		if td.BaseURL == "" && auth.Attributes != nil {
			if v, ok := auth.Attributes["base_url"]; ok {
				td.BaseURL = v
			}
		}
		return td
	}
	return nil
}
