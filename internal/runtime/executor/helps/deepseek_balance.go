package helps

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// DeepSeekBalance holds the parsed usage/balance information for a deepseek auth.
// Data is sourced from https://platform.deepseek.com/api/v0/users/get_user_summary,
// which returns the wallet balance, token estimation, and the current month's cost.
type DeepSeekBalance struct {
	Currency             string    `json:"currency,omitempty"`
	Balance              float64   `json:"balance"`
	TokenEstimation      int64     `json:"token_estimation"`
	BonusBalance         float64   `json:"bonus_balance,omitempty"`
	BonusTokenEstimation int64     `json:"bonus_token_estimation,omitempty"`
	TotalAvailableTokens int64     `json:"total_available_tokens,omitempty"`
	MonthlyCost          float64   `json:"monthly_cost"`
	MonthlyTokenUsage    int64     `json:"monthly_token_usage,omitempty"`
	CurrentToken         int64     `json:"current_token,omitempty"`
	Source               string    `json:"source,omitempty"`
	FetchedAt            time.Time `json:"fetched_at"`
}

// DeepSeekCredentials enumerates the authentication options available for a
// deepseek auth entry. Either an API key (Bearer) or a Cookie header can be
// supplied; the fetcher tries each in turn.
type DeepSeekCredentials struct {
	APIKey  string // Bearer token (DeepSeek API key or platform session token)
	Cookies string // optional Cookie header value for the platform session
}

// HasAny reports whether at least one credential is provided.
func (c DeepSeekCredentials) HasAny() bool {
	return strings.TrimSpace(c.APIKey) != "" || strings.TrimSpace(c.Cookies) != ""
}

const (
	deepseekBalanceRefreshInterval = 10 * time.Minute
	deepseekBalanceFetchTimeout    = 15 * time.Second
	deepseekUserSummaryURL         = "https://platform.deepseek.com/api/v0/users/get_user_summary"
)

var (
	deepseekBalanceCache    sync.Map // key: cred hash → *deepseekBalanceEntry
	deepseekBalanceFetching sync.Map // key: cred hash → struct{} (dedup in-flight fetches)
)

type deepseekBalanceEntry struct {
	balance *DeepSeekBalance
	err     error
}

// GetDeepSeekBalanceWithCreds returns a cached deepseek balance for the given
// credential bundle, or fetches a fresh one if the cache is stale or empty.
func GetDeepSeekBalanceWithCreds(creds DeepSeekCredentials, cfg *config.Config, auth *cliproxyauth.Auth) *DeepSeekBalance {
	if !creds.HasAny() {
		return nil
	}
	key := deepseekCredKey(creds)

	if val, ok := deepseekBalanceCache.Load(key); ok {
		entry := val.(*deepseekBalanceEntry)
		if entry.balance != nil && time.Since(entry.balance.FetchedAt) < deepseekBalanceRefreshInterval {
			return entry.balance
		}
	}

	return fetchAndCacheDeepSeekBalance(key, creds, cfg, auth)
}

// RefreshDeepSeekBalanceWithCreds forces a fresh fetch using the provided
// credential bundle, bypassing the cache.
func RefreshDeepSeekBalanceWithCreds(creds DeepSeekCredentials, cfg *config.Config, auth *cliproxyauth.Auth) (*DeepSeekBalance, error) {
	if !creds.HasAny() {
		return nil, fmt.Errorf("no deepseek credentials provided")
	}
	key := deepseekCredKey(creds)

	deepseekBalanceCache.Delete(key)

	balance := fetchAndCacheDeepSeekBalance(key, creds, cfg, auth)
	if balance == nil {
		if val, ok := deepseekBalanceCache.Load(key); ok {
			entry := val.(*deepseekBalanceEntry)
			if entry.err != nil {
				return nil, entry.err
			}
		}
		return nil, fmt.Errorf("failed to fetch deepseek balance")
	}
	return balance, nil
}

func fetchAndCacheDeepSeekBalance(key string, creds DeepSeekCredentials, cfg *config.Config, auth *cliproxyauth.Auth) *DeepSeekBalance {
	if _, busy := deepseekBalanceFetching.LoadOrStore(key, struct{}{}); busy {
		if val, ok := deepseekBalanceCache.Load(key); ok {
			entry := val.(*deepseekBalanceEntry)
			if entry.balance != nil {
				return entry.balance
			}
		}
		return nil
	}
	defer deepseekBalanceFetching.Delete(key)

	ctx, cancel := context.WithTimeout(context.Background(), deepseekBalanceFetchTimeout)
	defer cancel()

	balance, err := fetchDeepSeekBalance(ctx, creds, cfg, auth)
	if err != nil {
		log.Debugf("deepseek balance fetch failed: %v", err)
		if val, ok := deepseekBalanceCache.Load(key); ok {
			entry := val.(*deepseekBalanceEntry)
			if entry.balance != nil {
				return entry.balance
			}
		}
		deepseekBalanceCache.Store(key, &deepseekBalanceEntry{err: err})
		return nil
	}

	deepseekBalanceCache.Store(key, &deepseekBalanceEntry{balance: balance})
	return balance
}

// deepseekCredKey returns a short deterministic key for deduplication and caching.
func deepseekCredKey(creds DeepSeekCredentials) string {
	h := sha256.New()
	h.Write([]byte(strings.TrimSpace(creds.APIKey)))
	h.Write([]byte{'|'})
	h.Write([]byte(strings.TrimSpace(creds.Cookies)))
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

func httpClientForDeepSeek(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) *http.Client {
	if cfg != nil || auth != nil {
		return NewProxyAwareHTTPClient(ctx, cfg, auth, deepseekBalanceFetchTimeout)
	}
	return http.DefaultClient
}

// fetchDeepSeekBalance calls the get_user_summary endpoint with whatever
// credentials are available. The Bearer token takes precedence; if none is
// supplied we fall back to a Cookie-only request (sometimes accepted when the
// platform session is bound to the WAF cookies).
func fetchDeepSeekBalance(ctx context.Context, creds DeepSeekCredentials, cfg *config.Config, auth *cliproxyauth.Auth) (*DeepSeekBalance, error) {
	apiKey := strings.TrimSpace(creds.APIKey)
	cookies := strings.TrimSpace(creds.Cookies)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, deepseekUserSummaryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if cookies != "" {
		req.Header.Set("Cookie", cookies)
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "cli-proxy-api/deepseek-balance")
	req.Header.Set("Referer", "https://platform.deepseek.com/usage")

	client := httpClientForDeepSeek(ctx, cfg, auth)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get_user_summary: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("deepseek balance: close response body error: %v", errClose)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get_user_summary returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	balance, err := parseDeepSeekUserSummary(body)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		balance.Source = "bearer"
	} else {
		balance.Source = "cookie"
	}
	return balance, nil
}

// parseDeepSeekUserSummary extracts wallet balance and monthly cost from the
// get_user_summary response described in the user-provided curl example:
//
//	{
//	  "code": 0,
//	  "data": {
//	    "biz_code": 0,
//	    "biz_data": {
//	      "current_token": 10000000,
//	      "monthly_usage": "172322133",
//	      "normal_wallets": [{"currency":"CNY","balance":"33.93","token_estimation":"11311232"}],
//	      "bonus_wallets":  [{"currency":"CNY","balance":"0","token_estimation":"0"}],
//	      "total_available_token_estimation": "11311232",
//	      "monthly_costs": [{"currency":"CNY","amount":"16.06"}],
//	      "monthly_token_usage": "172322133"
//	    }
//	  }
//	}
//
// Numeric fields are returned as strings to preserve precision; we parse them
// best-effort and fall back to zero when malformed.
func parseDeepSeekUserSummary(body []byte) (*DeepSeekBalance, error) {
	type wallet struct {
		Currency        string `json:"currency"`
		Balance         string `json:"balance"`
		TokenEstimation string `json:"token_estimation"`
	}
	type cost struct {
		Currency string `json:"currency"`
		Amount   string `json:"amount"`
	}
	type bizData struct {
		CurrentToken                  json.Number `json:"current_token"`
		MonthlyUsage                  string      `json:"monthly_usage"`
		NormalWallets                 []wallet    `json:"normal_wallets"`
		BonusWallets                  []wallet    `json:"bonus_wallets"`
		TotalAvailableTokenEstimation string      `json:"total_available_token_estimation"`
		MonthlyCosts                  []cost      `json:"monthly_costs"`
		MonthlyTokenUsage             string      `json:"monthly_token_usage"`
	}
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			BizCode int     `json:"biz_code"`
			BizMsg  string  `json:"biz_msg"`
			BizData bizData `json:"biz_data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse user_summary JSON: %w", err)
	}
	if payload.Code != 0 {
		msg := strings.TrimSpace(payload.Msg)
		if msg == "" {
			msg = strconv.Itoa(payload.Code)
		}
		return nil, fmt.Errorf("user_summary returned code %d: %s", payload.Code, msg)
	}
	if payload.Data.BizCode != 0 {
		msg := strings.TrimSpace(payload.Data.BizMsg)
		if msg == "" {
			msg = strconv.Itoa(payload.Data.BizCode)
		}
		return nil, fmt.Errorf("user_summary biz_code %d: %s", payload.Data.BizCode, msg)
	}

	biz := payload.Data.BizData
	balance := &DeepSeekBalance{FetchedAt: time.Now()}

	if len(biz.NormalWallets) > 0 {
		w := biz.NormalWallets[0]
		balance.Currency = strings.TrimSpace(w.Currency)
		balance.Balance = parseDeepSeekFloat(w.Balance)
		balance.TokenEstimation = parseDeepSeekInt(w.TokenEstimation)
	}
	if len(biz.BonusWallets) > 0 {
		w := biz.BonusWallets[0]
		if balance.Currency == "" {
			balance.Currency = strings.TrimSpace(w.Currency)
		}
		balance.BonusBalance = parseDeepSeekFloat(w.Balance)
		balance.BonusTokenEstimation = parseDeepSeekInt(w.TokenEstimation)
	}
	if len(biz.MonthlyCosts) > 0 {
		c := biz.MonthlyCosts[0]
		if balance.Currency == "" {
			balance.Currency = strings.TrimSpace(c.Currency)
		}
		balance.MonthlyCost = parseDeepSeekFloat(c.Amount)
	}
	balance.TotalAvailableTokens = parseDeepSeekInt(biz.TotalAvailableTokenEstimation)
	balance.MonthlyTokenUsage = parseDeepSeekInt(biz.MonthlyTokenUsage)
	if biz.CurrentToken != "" {
		if v, err := biz.CurrentToken.Int64(); err == nil {
			balance.CurrentToken = v
		}
	}

	if len(biz.NormalWallets) == 0 && len(biz.MonthlyCosts) == 0 {
		return nil, fmt.Errorf("user_summary contained no wallet or cost data")
	}
	return balance, nil
}

func parseDeepSeekFloat(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return v
}

func parseDeepSeekInt(s string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return v
}
