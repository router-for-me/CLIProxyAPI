package helps

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// OllamaBalance holds the parsed usage/balance information for an ollama auth.
// Data is sourced from one of:
//   - https://ollama.com/api/account/usage (proposed JSON endpoint, Bearer auth)
//   - https://ollama.com/settings (HTML page, Bearer or Cookie auth)
type OllamaBalance struct {
	SessionUsagePct float64   `json:"session_usage_pct"`
	SessionResetsAt time.Time `json:"session_resets_at"`
	WeeklyUsagePct  float64   `json:"weekly_usage_pct"`
	WeeklyResetsAt  time.Time `json:"weekly_resets_at"`
	Plan            string    `json:"plan,omitempty"`
	Source          string    `json:"source,omitempty"` // "api-bearer" | "html-bearer" | "html-cookie"
	FetchedAt       time.Time `json:"fetched_at"`
}

// OllamaCredentials enumerates the authentication options available for an ollama
// auth entry. The fetcher will try each non-empty credential in turn.
type OllamaCredentials struct {
	Cookies string // session cookie header value (e.g. "aid=...; __Secure-session=...")
	APIKey  string // Bearer token (API key from openai-compatibility, or OAuth access_token)
}

// HasAny reports whether at least one credential is provided.
func (c OllamaCredentials) HasAny() bool {
	return strings.TrimSpace(c.Cookies) != "" || strings.TrimSpace(c.APIKey) != ""
}

const (
	ollamaBalanceRefreshInterval = 10 * time.Minute
	ollamaBalanceFetchTimeout    = 15 * time.Second
	ollamaSettingsURL            = "https://ollama.com/settings"
	// ollamaUsageAPIURL is the proposed account usage endpoint tracked in
	// https://github.com/ollama/ollama/issues/15663 and #15132. It is queried
	// best-effort: if/when ollama ships it, balance retrieval becomes
	// API-key-only and no cookies are required.
	ollamaUsageAPIURL = "https://ollama.com/api/account/usage"
)

var (
	ollamaBalanceCache    sync.Map // key: cred hash → *ollamaBalanceEntry
	ollamaBalanceFetching sync.Map // key: cred hash → struct{} (dedup in-flight fetches)
)

type ollamaBalanceEntry struct {
	balance *OllamaBalance
	err     error
}

// GetOllamaBalanceWithCreds returns a cached ollama balance for the given
// credential bundle, or fetches a fresh one if the cache is stale or empty.
// It tries each available credential (API key Bearer, then session cookies)
// until one succeeds.
func GetOllamaBalanceWithCreds(creds OllamaCredentials, cfg *config.Config, auth *cliproxyauth.Auth) *OllamaBalance {
	if !creds.HasAny() {
		return nil
	}
	key := ollamaCredKey(creds)

	// Check cache
	if val, ok := ollamaBalanceCache.Load(key); ok {
		entry := val.(*ollamaBalanceEntry)
		if entry.balance != nil && time.Since(entry.balance.FetchedAt) < ollamaBalanceRefreshInterval {
			return entry.balance
		}
	}

	return fetchAndCacheOllamaBalance(key, creds, cfg, auth)
}

// RefreshOllamaBalanceWithCreds forces a fresh fetch using the provided
// credential bundle, bypassing the cache.
func RefreshOllamaBalanceWithCreds(creds OllamaCredentials, cfg *config.Config, auth *cliproxyauth.Auth) (*OllamaBalance, error) {
	if !creds.HasAny() {
		return nil, fmt.Errorf("no ollama credentials provided")
	}
	key := ollamaCredKey(creds)

	// Invalidate cache
	ollamaBalanceCache.Delete(key)

	// Fetch fresh data
	balance := fetchAndCacheOllamaBalance(key, creds, cfg, auth)
	if balance == nil {
		// Check if there was an error
		if val, ok := ollamaBalanceCache.Load(key); ok {
			entry := val.(*ollamaBalanceEntry)
			if entry.err != nil {
				return nil, entry.err
			}
		}
		return nil, fmt.Errorf("failed to fetch ollama balance")
	}
	return balance, nil
}

// fetchAndCacheOllamaBalance performs the actual fetch and caches the result.
func fetchAndCacheOllamaBalance(key string, creds OllamaCredentials, cfg *config.Config, auth *cliproxyauth.Auth) *OllamaBalance {
	// Avoid duplicate in-flight fetches
	if _, busy := ollamaBalanceFetching.LoadOrStore(key, struct{}{}); busy {
		// Another goroutine is fetching; return stale cache if available
		if val, ok := ollamaBalanceCache.Load(key); ok {
			entry := val.(*ollamaBalanceEntry)
			if entry.balance != nil {
				return entry.balance
			}
		}
		return nil
	}
	defer ollamaBalanceFetching.Delete(key)

	// Fetch fresh balance
	ctx, cancel := context.WithTimeout(context.Background(), ollamaBalanceFetchTimeout)
	defer cancel()

	balance, err := fetchOllamaBalanceMulti(ctx, creds, cfg, auth)
	if err != nil {
		log.Debugf("ollama balance fetch failed: %v", err)
		// Store error but keep any previous balance
		if val, ok := ollamaBalanceCache.Load(key); ok {
			entry := val.(*ollamaBalanceEntry)
			if entry.balance != nil {
				return entry.balance
			}
		}
		ollamaBalanceCache.Store(key, &ollamaBalanceEntry{err: err})
		return nil
	}

	ollamaBalanceCache.Store(key, &ollamaBalanceEntry{balance: balance})
	return balance
}

// ollamaCredKey returns a short deterministic key for deduplication and caching.
// The key incorporates both the cookie value and the API key so that updates to
// either credential force a re-fetch.
func ollamaCredKey(creds OllamaCredentials) string {
	h := sha256.New()
	h.Write([]byte(strings.TrimSpace(creds.Cookies)))
	h.Write([]byte{'|'})
	h.Write([]byte(strings.TrimSpace(creds.APIKey)))
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

// httpClientForOllama returns a proxy-aware HTTP client when config/auth is
// available, otherwise the default client.
func httpClientForOllama(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) *http.Client {
	if cfg != nil || auth != nil {
		return NewProxyAwareHTTPClient(ctx, cfg, auth, ollamaBalanceFetchTimeout)
	}
	return http.DefaultClient
}

// fetchOllamaBalanceMulti tries each available credential in turn and returns
// the first successful balance, or the last error encountered.
//
// Order of attempts (best-effort, falls through on failure):
//  1. JSON usage API with Bearer (forward-compatible with the proposed endpoint)
//  2. Settings HTML with Bearer (in case ollama accepts Bearer on the web UI)
//  3. Settings HTML with Cookie (the documented current path)
func fetchOllamaBalanceMulti(ctx context.Context, creds OllamaCredentials, cfg *config.Config, auth *cliproxyauth.Auth) (*OllamaBalance, error) {
	var lastErr error
	apiKey := strings.TrimSpace(creds.APIKey)
	cookies := strings.TrimSpace(creds.Cookies)

	if apiKey != "" {
		if balance, err := tryOllamaUsageJSON(ctx, apiKey, cfg, auth); err == nil && balance != nil {
			balance.Source = "api-bearer"
			return balance, nil
		} else if err != nil {
			lastErr = err
			log.Debugf("ollama balance: usage JSON via Bearer failed: %v", err)
		}

		if balance, err := tryOllamaSettingsHTML(ctx, "", apiKey, cfg, auth); err == nil && balance != nil {
			balance.Source = "html-bearer"
			return balance, nil
		} else if err != nil {
			lastErr = err
			log.Debugf("ollama balance: settings HTML via Bearer failed: %v", err)
		}
	}

	if cookies != "" {
		if balance, err := tryOllamaSettingsHTML(ctx, cookies, "", cfg, auth); err == nil && balance != nil {
			balance.Source = "html-cookie"
			return balance, nil
		} else if err != nil {
			lastErr = err
			log.Debugf("ollama balance: settings HTML via Cookie failed: %v", err)
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no usable ollama credentials")
	}
	return nil, lastErr
}

// tryOllamaUsageJSON attempts to fetch the proposed JSON usage endpoint with
// Bearer auth. Returns nil if the endpoint is missing (404) or unauthenticated
// (401/403) so the caller can fall through to the HTML path.
func tryOllamaUsageJSON(ctx context.Context, apiKey string, cfg *config.Config, auth *cliproxyauth.Auth) (*OllamaBalance, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ollamaUsageAPIURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "cli-proxy-api/ollama-balance")

	client := httpClientForOllama(ctx, cfg, auth)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("usage endpoint: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("ollama balance: close response body error: %v", errClose)
		}
	}()

	// 404 => endpoint not yet shipped. 401/403 => Bearer not honored here.
	// In either case, fall back to other paths silently.
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("usage endpoint returned %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("usage endpoint returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return parseOllamaUsageJSON(body)
}

// tryOllamaSettingsHTML fetches the settings page using either a Cookie header
// or a Bearer token (or both). Returns parsed balance or an error.
func tryOllamaSettingsHTML(ctx context.Context, cookieValue, bearerValue string, cfg *config.Config, auth *cliproxyauth.Auth) (*OllamaBalance, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ollamaSettingsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if cookieValue != "" {
		req.Header.Set("Cookie", cookieValue)
	}
	if bearerValue != "" {
		req.Header.Set("Authorization", "Bearer "+bearerValue)
	}
	req.Header.Set("User-Agent", "cli-proxy-api/ollama-balance")
	req.Header.Set("Accept", "text/html")

	client := httpClientForOllama(ctx, cfg, auth)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch settings: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("ollama balance: close response body error: %v", errClose)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("settings page returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	return parseOllamaSettingsHTML(body)
}

// parseOllamaUsageJSON parses the proposed account-usage JSON response shape
// described in https://github.com/ollama/ollama/issues/15132. The shape is:
//
//	{
//	  "plan": "pro",
//	  "session": {"used_percentage": 21.2, "resets_at": "2026-05-05T13:00:00Z"},
//	  "weekly":  {"used_percentage": 35.3, "resets_at": "2026-05-11T00:00:00Z"}
//	}
//
// The function is forward-compatible: any missing field is left zero so the
// upstream response can evolve without breaking us.
func parseOllamaUsageJSON(body []byte) (*OllamaBalance, error) {
	type usageBlock struct {
		UsedPct  float64 `json:"used_percentage"`
		ResetsAt string  `json:"resets_at"`
	}
	var payload struct {
		Plan    string     `json:"plan"`
		Session usageBlock `json:"session"`
		Weekly  usageBlock `json:"weekly"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse usage JSON: %w", err)
	}

	balance := &OllamaBalance{
		FetchedAt:       time.Now(),
		Plan:            strings.TrimSpace(payload.Plan),
		SessionUsagePct: payload.Session.UsedPct,
		WeeklyUsagePct:  payload.Weekly.UsedPct,
	}
	if t, err := time.Parse(time.RFC3339, payload.Session.ResetsAt); err == nil {
		balance.SessionResetsAt = t
	}
	if t, err := time.Parse(time.RFC3339, payload.Weekly.ResetsAt); err == nil {
		balance.WeeklyResetsAt = t
	}

	if balance.SessionUsagePct == 0 && balance.WeeklyUsagePct == 0 && balance.SessionResetsAt.IsZero() && balance.WeeklyResetsAt.IsZero() {
		return nil, fmt.Errorf("usage JSON contained no data")
	}
	return balance, nil
}

// parseOllamaSettingsHTML extracts usage information from the ollama settings HTML page.
func parseOllamaSettingsHTML(html []byte) (*OllamaBalance, error) {
	s := string(html)
	balance := &OllamaBalance{
		FetchedAt: time.Now(),
	}

	// Extract plan (e.g., "pro" or "free")
	planRe := regexp.MustCompile(`>pro<|>free<|>starter<|>team<`)
	if match := planRe.FindString(s); match != "" {
		balance.Plan = strings.Trim(match, "><")
	}

	// Extract session usage percentage (e.g., "21.2% used")
	sessionPctRe := regexp.MustCompile(`Session usage[\s\S]*?(\d+(?:\.\d+)?)%\s*used`)
	if match := sessionPctRe.FindStringSubmatch(s); len(match) > 1 {
		if pct, err := strconv.ParseFloat(match[1], 64); err == nil {
			balance.SessionUsagePct = pct
		}
	}

	// Extract weekly usage percentage (e.g., "35.3% used")
	weeklyPctRe := regexp.MustCompile(`Weekly usage[\s\S]*?(\d+(?:\.\d+)?)%\s*used`)
	if match := weeklyPctRe.FindStringSubmatch(s); len(match) > 1 {
		if pct, err := strconv.ParseFloat(match[1], 64); err == nil {
			balance.WeeklyUsagePct = pct
		}
	}

	// Extract reset times from data-time attributes.
	// The first data-time after session usage is the session reset time,
	// and the second data-time after weekly usage is the weekly reset time.
	dataTimeRe := regexp.MustCompile(`data-time="([^"]+)"`)
	matches := dataTimeRe.FindAllStringSubmatch(s, -1)
	if len(matches) >= 1 {
		if t, err := time.Parse(time.RFC3339, matches[0][1]); err == nil {
			balance.SessionResetsAt = t
		}
	}
	if len(matches) >= 2 {
		if t, err := time.Parse(time.RFC3339, matches[1][1]); err == nil {
			balance.WeeklyResetsAt = t
		}
	}

	// Validate that we got at least some data
	if balance.SessionUsagePct == 0 && balance.WeeklyUsagePct == 0 && balance.SessionResetsAt.IsZero() && balance.WeeklyResetsAt.IsZero() {
		return nil, fmt.Errorf("no usage data found in settings page")
	}

	return balance, nil
}
