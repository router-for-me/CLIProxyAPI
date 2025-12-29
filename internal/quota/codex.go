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
			Models:      []ModelQuota{},
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

// API response structures based on Quotio's CodexCLIQuotaFetcher

type codexUsageResponse struct {
	RateLimit *rateLimitInfo `json:"rate_limit"`
	Plan      *planInfo      `json:"plan"`
}

type rateLimitInfo struct {
	PrimaryWindow   *windowInfo `json:"primary_window"`
	SecondaryWindow *windowInfo `json:"secondary_window"`
}

type windowInfo struct {
	Used    int64  `json:"used"`
	Limit   int64  `json:"limit"`
	ResetAt string `json:"reset_at"`
}

type planInfo struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

// parseUsageResponse converts the API response to our unified ProviderQuotaData format.
func (f *CodexFetcher) parseUsageResponse(resp *codexUsageResponse) *ProviderQuotaData {
	var models []ModelQuota

	if resp.RateLimit != nil {
		// Primary window (session usage)
		if resp.RateLimit.PrimaryWindow != nil {
			pw := resp.RateLimit.PrimaryWindow
			percentage := float64(100)
			if pw.Limit > 0 {
				remaining := pw.Limit - pw.Used
				percentage = float64(remaining) / float64(pw.Limit) * 100
				if percentage < 0 {
					percentage = 0
				}
			}
			used := pw.Used
			limit := pw.Limit
			remaining := pw.Limit - pw.Used
			if remaining < 0 {
				remaining = 0
			}
			models = append(models, ModelQuota{
				Name:       "codex-session",
				Percentage: percentage,
				ResetTime:  pw.ResetAt,
				Used:       &used,
				Limit:      &limit,
				Remaining:  &remaining,
			})
		}

		// Secondary window (weekly usage)
		if resp.RateLimit.SecondaryWindow != nil {
			sw := resp.RateLimit.SecondaryWindow
			percentage := float64(100)
			if sw.Limit > 0 {
				remaining := sw.Limit - sw.Used
				percentage = float64(remaining) / float64(sw.Limit) * 100
				if percentage < 0 {
					percentage = 0
				}
			}
			used := sw.Used
			limit := sw.Limit
			remaining := sw.Limit - sw.Used
			if remaining < 0 {
				remaining = 0
			}
			models = append(models, ModelQuota{
				Name:       "codex-weekly",
				Percentage: percentage,
				ResetTime:  sw.ResetAt,
				Used:       &used,
				Limit:      &limit,
				Remaining:  &remaining,
			})
		}
	}

	data := &ProviderQuotaData{
		Models:      models,
		LastUpdated: time.Now(),
		IsForbidden: false,
	}

	// Set plan type
	if resp.Plan != nil {
		data.PlanType = resp.Plan.Name
		data.Extra = map[string]any{
			"plan_display_name": resp.Plan.DisplayName,
		}
	}

	return data
}