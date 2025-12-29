package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	codexUsageAPIURL = "https://chatgpt.com/backend-api/wham/usage"
)

// CodexFetcher implements quota fetching for Codex/OpenAI provider.
type CodexFetcher struct {
	httpClient *http.Client
}

// NewCodexFetcher creates a new Codex quota fetcher.
func NewCodexFetcher(httpClient *http.Client) *CodexFetcher {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &CodexFetcher{httpClient: httpClient}
}

// Provider returns the provider name.
func (f *CodexFetcher) Provider() string {
	return "codex"
}

// SupportedProviders returns all provider names this fetcher supports.
func (f *CodexFetcher) SupportedProviders() []string {
	return []string{"codex"}
}

// CanFetch returns true if this fetcher can handle the given provider.
func (f *CodexFetcher) CanFetch(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	return provider == "codex"
}

// FetchQuota fetches quota for a Codex auth credential.
func (f *CodexFetcher) FetchQuota(ctx context.Context, auth *coreauth.Auth) (*ProviderQuotaData, error) {
	if auth == nil {
		return nil, fmt.Errorf("auth is nil")
	}

	// Get access token from metadata
	accessToken := f.extractAccessToken(auth)
	if accessToken == "" {
		return UnavailableQuota("no access token available"), nil
	}

	// Fetch usage data
	quotaData, err := f.fetchUsageData(ctx, accessToken)
	if err != nil {
		log.Warnf("codex quota: failed to fetch usage: %v", err)
		return UnavailableQuota(fmt.Sprintf("failed to fetch usage: %v", err)), nil
	}

	return quotaData, nil
}

// extractAccessToken extracts the access token from auth metadata.
func (f *CodexFetcher) extractAccessToken(auth *coreauth.Auth) string {
	if auth.Metadata == nil {
		return ""
	}
	if token, ok := auth.Metadata["access_token"].(string); ok {
		return strings.TrimSpace(token)
	}
	return ""
}

// fetchUsageData fetches usage data from the ChatGPT API.
func (f *CodexFetcher) fetchUsageData(ctx context.Context, accessToken string) (*ProviderQuotaData, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, codexUsageAPIURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized {
		return &ProviderQuotaData{
			LastUpdated: time.Now(),
			IsForbidden: true,
			Error:       fmt.Sprintf("HTTP %d", resp.StatusCode),
		}, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result codexUsageResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return f.parseUsageResponse(&result), nil
}

// API response structures matching the ChatGPT backend API

type codexUsageResponse struct {
	PlanType            string         `json:"plan_type"`
	RateLimit           *rateLimitInfo `json:"rate_limit"`
	CodeReviewRateLimit *rateLimitInfo `json:"code_review_rate_limit"`
}

type rateLimitInfo struct {
	Allowed         bool        `json:"allowed"`
	LimitReached    bool        `json:"limit_reached"`
	PrimaryWindow   *windowInfo `json:"primary_window"`
	SecondaryWindow *windowInfo `json:"secondary_window"`
}

type windowInfo struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

// parseUsageResponse converts the API response to our unified ProviderQuotaData format.
func (f *CodexFetcher) parseUsageResponse(resp *codexUsageResponse) *ProviderQuotaData {
	windows := &RateLimitWindows{}

	if resp.RateLimit != nil {
		// Primary window (session usage - typically 5 hours)
		if resp.RateLimit.PrimaryWindow != nil {
			pw := resp.RateLimit.PrimaryWindow
			// remaining percentage = 100 - used_percent
			percentage := 100.0 - pw.UsedPercent
			if percentage < 0 {
				percentage = 0
			}

			// Convert Unix timestamp to ISO 8601 string for ResetTime
			resetTime := time.Unix(pw.ResetAt, 0).UTC().Format(time.RFC3339)

			windows.Session = &WindowQuota{
				Percentage:    percentage,
				ResetTime:     resetTime,
				WindowSeconds: pw.LimitWindowSeconds,
			}
		}

		// Secondary window (weekly usage - typically 7 days)
		if resp.RateLimit.SecondaryWindow != nil {
			sw := resp.RateLimit.SecondaryWindow
			// remaining percentage = 100 - used_percent
			percentage := 100.0 - sw.UsedPercent
			if percentage < 0 {
				percentage = 0
			}

			// Convert Unix timestamp to ISO 8601 string for ResetTime
			resetTime := time.Unix(sw.ResetAt, 0).UTC().Format(time.RFC3339)

			windows.Weekly = &WindowQuota{
				Percentage:    percentage,
				ResetTime:     resetTime,
				WindowSeconds: sw.LimitWindowSeconds,
			}
		}
	}

	data := &ProviderQuotaData{
		Windows:     windows,
		LastUpdated: time.Now(),
		IsForbidden: false,
	}

	// Set plan type from root level
	if resp.PlanType != "" {
		data.PlanType = resp.PlanType
	}

	// Add extra info about rate limit status
	if resp.RateLimit != nil {
		data.Extra = map[string]any{
			"allowed":       resp.RateLimit.Allowed,
			"limit_reached": resp.RateLimit.LimitReached,
		}
	}

	return data
}
