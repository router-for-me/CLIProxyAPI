package management

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Client struct {
	BaseURL       string
	ManagementKey string
	TLSSkipVerify bool
	hc            *http.Client // shared across all calls; nil means lazy-create (no reuse)
}

// NewClient builds a Client with a single shared http.Client so that
// keep-alive connections are reused across repeated calls instead of
// leaking one TCP connection per request.
func NewClient(baseURL, managementKey string, tlsSkipVerify bool) Client {
	var transport http.RoundTripper
	if tlsSkipVerify {
		transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec
	} else {
		transport = http.DefaultTransport
	}
	return Client{
		BaseURL:       baseURL,
		ManagementKey: managementKey,
		TLSSkipVerify: tlsSkipVerify,
		hc:            &http.Client{Transport: transport},
	}
}

type AuthFile struct {
	AuthIndex               string
	Provider                string
	Email                   string
	Account                 string
	AccountID               string
	PlanType                string
	SubscriptionActiveStart time.Time
	SubscriptionActiveUntil time.Time
	Success                 int64
	Failed                  int64
	Disabled                bool
	Unavailable             bool
}

type CodexQuotaWindow struct {
	AuthIndex          string
	WindowID           string
	Email              string
	AccountID          string
	PlanType           string
	Label              string
	RemainingPercent   int
	UsedPercent        int
	ResetAt            time.Time
	LimitWindowSeconds int64
	Allowed            bool
	LimitReached       bool
}

type apiKeysResponse struct {
	APIKeys []string `json:"api-keys"`
}

type authFilesResponse struct {
	Files []struct {
		AuthIndex   string `json:"auth_index"`
		Provider    string `json:"provider"`
		Email       string `json:"email"`
		Account     string `json:"account"`
		Success     int64  `json:"success"`
		Failed      int64  `json:"failed"`
		Disabled    bool   `json:"disabled"`
		Unavailable bool   `json:"unavailable"`
		IDToken     struct {
			AccountID               string `json:"chatgpt_account_id"`
			PlanType                string `json:"plan_type"`
			SubscriptionActiveStart string `json:"chatgpt_subscription_active_start"`
			SubscriptionActiveUntil string `json:"chatgpt_subscription_active_until"`
		} `json:"id_token"`
	} `json:"files"`
}

func (c Client) FetchAuthFiles(ctx context.Context) ([]AuthFile, error) {
	url := strings.TrimRight(c.BaseURL, "/") + "/v0/management/auth-files"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Management-Key", c.ManagementKey)
	resp, err := c.httpClient(20 * time.Second).Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch auth files: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch auth files status %d", resp.StatusCode)
	}
	var parsed authFilesResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode auth files: %w", err)
	}
	out := make([]AuthFile, 0, len(parsed.Files))
	for _, file := range parsed.Files {
		out = append(out, AuthFile{
			AuthIndex:               file.AuthIndex,
			Provider:                file.Provider,
			Email:                   file.Email,
			Account:                 file.Account,
			AccountID:               file.IDToken.AccountID,
			PlanType:                file.IDToken.PlanType,
			SubscriptionActiveStart: parseTime(file.IDToken.SubscriptionActiveStart),
			SubscriptionActiveUntil: parseTime(file.IDToken.SubscriptionActiveUntil),
			Success:                 file.Success,
			Failed:                  file.Failed,
			Disabled:                file.Disabled,
			Unavailable:             file.Unavailable,
		})
	}
	return out, nil
}

func (c Client) FetchAPIKeys(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+"/v0/management/api-keys", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Management-Key", c.ManagementKey)
	resp, err := c.httpClient(0).Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch api keys: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch api keys status %d", resp.StatusCode)
	}
	var parsed apiKeysResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode api keys: %w", err)
	}
	return parsed.APIKeys, nil
}

func (c Client) CreateAPIKey(ctx context.Context, apiKey string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return fmt.Errorf("api key is required")
	}
	payload, err := json.Marshal(map[string]string{"old": apiKey, "new": apiKey})
	if err != nil {
		return fmt.Errorf("marshal api key patch: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, strings.TrimRight(c.BaseURL, "/")+"/v0/management/api-keys", strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("X-Management-Key", c.ManagementKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient(0).Do(req)
	if err != nil {
		return fmt.Errorf("create api key: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("create api key status %d", resp.StatusCode)
	}
	return nil
}

func (c Client) DeleteAPIKey(ctx context.Context, apiKey string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return fmt.Errorf("api key is required")
	}
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/v0/management/api-keys?value=" + url.QueryEscape(apiKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Management-Key", c.ManagementKey)
	resp, err := c.httpClient(0).Do(req)
	if err != nil {
		return fmt.Errorf("delete api key: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("delete api key status %d", resp.StatusCode)
	}
	return nil
}

func (c Client) FetchCodexQuotaWindows(ctx context.Context, auth AuthFile) ([]CodexQuotaWindow, error) {
	body := map[string]any{
		"auth_index": auth.AuthIndex,
		"method":     http.MethodGet,
		"url":        "https://chatgpt.com/backend-api/wham/usage",
		"header": map[string]string{
			"Authorization":      "Bearer $TOKEN$",
			"Accept":             "application/json",
			"ChatGPT-Account-Id": auth.AccountID,
			"Origin":             "https://chatgpt.com",
			"Referer":            "https://chatgpt.com/",
			"User-Agent":         "codex-tui/0.118.0 (Mac OS 26.3.1; arm64) iTerm.app/3.6.9 (codex-tui; 0.118.0)",
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal codex quota request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/v0/management/api-call", strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Management-Key", c.ManagementKey)
	req.Header.Set("Content-Type", "application/json")
	client := c.httpClient(0)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch codex quota: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch codex quota status %d", resp.StatusCode)
	}
	var parsed struct {
		StatusCode int    `json:"status_code"`
		Body       string `json:"body"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode codex quota response: %w", err)
	}
	if parsed.StatusCode >= 300 {
		return nil, fmt.Errorf("codex quota upstream status %d", parsed.StatusCode)
	}
	return ParseCodexQuotaWindows([]byte(parsed.Body), auth)
}

func (c Client) httpClient(timeout time.Duration) *http.Client {
	if c.hc != nil {
		if timeout == 0 {
			return c.hc
		}
		// Return a shallow copy with the requested timeout but the same
		// shared transport so connections are still reused.
		cp := *c.hc
		cp.Timeout = timeout
		return &cp
	}
	// Zero-value Client (e.g. old struct-literal callers): fall back to a
	// fresh client. No connection reuse, but functionally correct.
	client := &http.Client{Timeout: timeout}
	if c.TLSSkipVerify {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec
	}
	return client
}

func ParseCodexQuotaWindows(data []byte, auth AuthFile) ([]CodexQuotaWindow, error) {
	var payload struct {
		AccountID            string           `json:"account_id"`
		Email                string           `json:"email"`
		PlanType             string           `json:"plan_type"`
		RateLimit            rateLimitPayload `json:"rate_limit"`
		AdditionalRateLimits []struct {
			LimitName string           `json:"limit_name"`
			RateLimit rateLimitPayload `json:"rate_limit"`
		} `json:"additional_rate_limits"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("parse codex quota: %w", err)
	}
	accountID := firstText(payload.AccountID, auth.AccountID)
	email := firstText(payload.Email, auth.Email)
	planType := firstText(payload.PlanType, auth.PlanType)
	var out []CodexQuotaWindow
	out = append(out, quotaWindow(auth.AuthIndex, "five-hour", "5 小时限额", email, accountID, planType, payload.RateLimit, payload.RateLimit.PrimaryWindow))
	out = append(out, quotaWindow(auth.AuthIndex, "weekly", "周限额", email, accountID, planType, payload.RateLimit, payload.RateLimit.SecondaryWindow))
	for _, limit := range payload.AdditionalRateLimits {
		name := strings.TrimSpace(limit.LimitName)
		if name == "" {
			continue
		}
		prefix := slug(name)
		out = append(out,
			quotaWindow(auth.AuthIndex, prefix+"-five-hour", name+" 5 小时限额", email, accountID, planType, limit.RateLimit, limit.RateLimit.PrimaryWindow),
			quotaWindow(auth.AuthIndex, prefix+"-weekly", name+" 周限额", email, accountID, planType, limit.RateLimit, limit.RateLimit.SecondaryWindow),
		)
	}
	filtered := out[:0]
	for _, window := range out {
		if window.LimitWindowSeconds <= 0 && window.ResetAt.IsZero() && window.UsedPercent == 0 {
			continue
		}
		filtered = append(filtered, window)
	}
	return filtered, nil
}

type rateLimitPayload struct {
	Allowed         bool               `json:"allowed"`
	LimitReached    bool               `json:"limit_reached"`
	PrimaryWindow   quotaWindowPayload `json:"primary_window"`
	SecondaryWindow quotaWindowPayload `json:"secondary_window"`
}

type quotaWindowPayload struct {
	UsedPercent        int   `json:"used_percent"`
	LimitWindowSeconds int64 `json:"limit_window_seconds"`
	ResetAt            int64 `json:"reset_at"`
}

func quotaWindow(authIndex, windowID, label, email, accountID, planType string, limit rateLimitPayload, window quotaWindowPayload) CodexQuotaWindow {
	used := clampPercent(window.UsedPercent)
	return CodexQuotaWindow{
		AuthIndex:          authIndex,
		WindowID:           windowID,
		Email:              email,
		AccountID:          accountID,
		PlanType:           planType,
		Label:              label,
		RemainingPercent:   100 - used,
		UsedPercent:        used,
		ResetAt:            unixTime(window.ResetAt),
		LimitWindowSeconds: window.LimitWindowSeconds,
		Allowed:            limit.Allowed,
		LimitReached:       limit.LimitReached,
	}
}

func parseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t
	}
	return time.Time{}
}

func unixTime(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.Unix(value, 0)
}

func clampPercent(value int) int {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func firstText(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func slug(value string) string {
	lower := strings.ToLower(strings.TrimSpace(value))
	re := regexp.MustCompile(`[^a-z0-9]+`)
	slugged := strings.Trim(re.ReplaceAllString(lower, "-"), "-")
	if slugged == "" {
		return "limit"
	}
	return slugged
}
