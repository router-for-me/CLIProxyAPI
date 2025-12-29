package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	codexUsageAPIURL = "https://chatgpt.com/backend-api/wham/usage"
	codexTokenURL    = "https://auth.openai.com/oauth/token"
	codexClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
	// codexRefreshSkew is the time buffer before token expiry to trigger refresh
	codexRefreshSkew = 5 * time.Minute
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

	// Ensure we have a valid access token (refresh if expired)
	accessToken, err := f.ensureAccessToken(ctx, auth)
	if err != nil {
		log.Warnf("codex quota: failed to ensure access token: %v", err)
		return UnavailableQuota(fmt.Sprintf("failed to ensure access token: %v", err)), nil
	}
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

// ensureAccessToken ensures we have a valid access token, refreshing if expired.
func (f *CodexFetcher) ensureAccessToken(ctx context.Context, auth *coreauth.Auth) (string, error) {
	if auth.Metadata == nil {
		return "", nil
	}

	accessToken := f.extractAccessToken(auth)
	if accessToken == "" {
		return "", nil
	}

	// Check if token is expired or about to expire
	expiry := f.tokenExpiry(auth)
	if expiry.After(time.Now().Add(codexRefreshSkew)) {
		// Token is still valid
		return accessToken, nil
	}

	// Token is expired or about to expire, try to refresh
	log.Debugf("codex quota: access token expired or expiring soon, attempting refresh")

	refreshToken := f.extractRefreshToken(auth)
	if refreshToken == "" {
		// No refresh token, return existing access token (it may still work)
		log.Debugf("codex quota: no refresh token available, using existing access token")
		return accessToken, nil
	}

	// Refresh the token
	newToken, err := f.refreshAccessToken(ctx, auth, refreshToken)
	if err != nil {
		log.Warnf("codex quota: failed to refresh token: %v", err)
		// Return existing token anyway - API might still accept it
		return accessToken, nil
	}

	return newToken, nil
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

// extractRefreshToken extracts the refresh token from auth metadata.
func (f *CodexFetcher) extractRefreshToken(auth *coreauth.Auth) string {
	if auth.Metadata == nil {
		return ""
	}
	if token, ok := auth.Metadata["refresh_token"].(string); ok {
		return strings.TrimSpace(token)
	}
	return ""
}

// tokenExpiry extracts the token expiry time from auth metadata.
func (f *CodexFetcher) tokenExpiry(auth *coreauth.Auth) time.Time {
	if auth.Metadata == nil {
		return time.Time{}
	}

	// Check various expiry field names
	var expiryStr string
	if v, ok := auth.Metadata["expired"].(string); ok {
		expiryStr = v
	} else if v, ok := auth.Metadata["expires_at"].(string); ok {
		expiryStr = v
	} else if v, ok := auth.Metadata["expiry"].(string); ok {
		expiryStr = v
	}

	if expiryStr == "" {
		return time.Time{}
	}

	// Try parsing with various formats
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05-07:00",
		"2006-01-02T15:04:05Z07:00",
	}
	for _, format := range formats {
		if t, err := time.Parse(format, expiryStr); err == nil {
			return t
		}
	}

	return time.Time{}
}

// refreshAccessToken refreshes the access token using the refresh token.
func (f *CodexFetcher) refreshAccessToken(ctx context.Context, auth *coreauth.Auth, refreshToken string) (string, error) {
	form := url.Values{}
	form.Set("client_id", codexClientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("scope", "openid profile email")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("create refresh request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("execute refresh request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("refresh failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("decode refresh response: %w", err)
	}

	// Update auth metadata with new tokens
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = tokenResp.AccessToken
	if tokenResp.RefreshToken != "" {
		auth.Metadata["refresh_token"] = tokenResp.RefreshToken
	}
	if tokenResp.IDToken != "" {
		auth.Metadata["id_token"] = tokenResp.IDToken
	}
	auth.Metadata["expires_in"] = tokenResp.ExpiresIn
	auth.Metadata["expired"] = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339)
	auth.Metadata["type"] = "codex"

	log.Debugf("codex quota: token refreshed successfully, new expiry: %s", auth.Metadata["expired"])

	return tokenResp.AccessToken, nil
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
