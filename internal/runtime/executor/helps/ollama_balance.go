package helps

import (
	"context"
	"crypto/sha256"
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

// OllamaBalance holds the parsed usage/balance information from the ollama settings page.
type OllamaBalance struct {
	SessionUsagePct float64   `json:"session_usage_pct"`
	SessionResetsAt time.Time `json:"session_resets_at"`
	WeeklyUsagePct  float64   `json:"weekly_usage_pct"`
	WeeklyResetsAt  time.Time `json:"weekly_resets_at"`
	Plan            string    `json:"plan,omitempty"`
	FetchedAt       time.Time `json:"fetched_at"`
}

const (
	ollamaBalanceRefreshInterval = 10 * time.Minute
	ollamaBalanceFetchTimeout    = 15 * time.Second
	ollamaSettingsURL            = "https://ollama.com/settings"
)

var (
	ollamaBalanceCache    sync.Map // key: cookie hash → *ollamaBalanceEntry
	ollamaBalanceFetching sync.Map // key: cookie hash → struct{} (dedup in-flight fetches)
)

type ollamaBalanceEntry struct {
	balance *OllamaBalance
	err     error
}

// GetOllamaBalance returns the cached ollama balance for the given cookies,
// or fetches a fresh one if the cache is stale or empty.
func GetOllamaBalance(cookies string) *OllamaBalance {
	return GetOllamaBalanceWithConfig(cookies, nil, nil)
}

// GetOllamaBalanceWithConfig returns the cached ollama balance, using the provided
// config and auth for proxy configuration when fetching.
func GetOllamaBalanceWithConfig(cookies string, cfg *config.Config, auth *cliproxyauth.Auth) *OllamaBalance {
	cookies = strings.TrimSpace(cookies)
	if cookies == "" {
		return nil
	}
	key := ollamaCookieKey(cookies)

	// Check cache
	if val, ok := ollamaBalanceCache.Load(key); ok {
		entry := val.(*ollamaBalanceEntry)
		if entry.balance != nil && time.Since(entry.balance.FetchedAt) < ollamaBalanceRefreshInterval {
			return entry.balance
		}
	}

	return fetchAndCacheOllamaBalance(key, cookies, cfg, auth)
}

// RefreshOllamaBalance forces a fresh fetch of the ollama balance, bypassing the cache.
// It invalidates the cached entry and fetches the latest data from ollama.com/settings.
func RefreshOllamaBalance(cookies string) (*OllamaBalance, error) {
	return RefreshOllamaBalanceWithConfig(cookies, nil, nil)
}

// RefreshOllamaBalanceWithConfig forces a fresh fetch with proxy support.
func RefreshOllamaBalanceWithConfig(cookies string, cfg *config.Config, auth *cliproxyauth.Auth) (*OllamaBalance, error) {
	cookies = strings.TrimSpace(cookies)
	if cookies == "" {
		return nil, fmt.Errorf("no ollama cookies provided")
	}
	key := ollamaCookieKey(cookies)

	// Invalidate cache
	ollamaBalanceCache.Delete(key)

	// Fetch fresh data
	balance := fetchAndCacheOllamaBalance(key, cookies, cfg, auth)
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
func fetchAndCacheOllamaBalance(key, cookies string, cfg *config.Config, auth *cliproxyauth.Auth) *OllamaBalance {
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

	balance, err := fetchOllamaBalance(ctx, cookies, cfg, auth)
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

// ollamaCookieKey returns a short deterministic key for deduplication and caching.
func ollamaCookieKey(cookies string) string {
	h := sha256.Sum256([]byte(cookies))
	return fmt.Sprintf("%x", h)[:16]
}

// fetchOllamaBalance fetches the ollama settings page and parses usage info from HTML.
func fetchOllamaBalance(ctx context.Context, cookies string, cfg *config.Config, auth *cliproxyauth.Auth) (*OllamaBalance, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ollamaSettingsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Cookie", cookies)
	req.Header.Set("User-Agent", "cli-proxy-api/ollama-balance")
	req.Header.Set("Accept", "text/html")

	var client *http.Client
	if cfg != nil || auth != nil {
		client = NewProxyAwareHTTPClient(ctx, cfg, auth, ollamaBalanceFetchTimeout)
	} else {
		client = http.DefaultClient
	}

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
