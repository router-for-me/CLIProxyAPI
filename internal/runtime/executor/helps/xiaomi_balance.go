package helps

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// XiaomiBalance holds the parsed token-plan usage for a Xiaomi MiMo auth.
// Data is sourced from https://platform.xiaomimimo.com/api/v1/tokenPlan/usage,
// which returns plan/month/compensation token counters.
type XiaomiBalance struct {
	MonthUsed         int64     `json:"month_used"`
	MonthLimit        int64     `json:"month_limit"`
	MonthPercent      float64   `json:"month_percent"`
	PlanUsed          int64     `json:"plan_used"`
	PlanLimit         int64     `json:"plan_limit"`
	PlanPercent       float64   `json:"plan_percent"`
	CompensationUsed  int64     `json:"compensation_used"`
	CompensationLimit int64     `json:"compensation_limit"`
	Source            string    `json:"source,omitempty"`
	FetchedAt         time.Time `json:"fetched_at"`
}

// XiaomiCredentials carries the Cookie header needed to authenticate the
// platform request. The Cookie must include api-platform_serviceToken plus
// userId / api-platform_slh / api-platform_ph as obtained from the browser
// session at platform.xiaomimimo.com.
type XiaomiCredentials struct {
	Cookies string
}

func (c XiaomiCredentials) HasAny() bool { return strings.TrimSpace(c.Cookies) != "" }

const (
	xiaomiBalanceRefreshInterval = 10 * time.Minute
	xiaomiBalanceFetchTimeout    = 15 * time.Second
	xiaomiTokenPlanUsageURL      = "https://platform.xiaomimimo.com/api/v1/tokenPlan/usage"
)

var (
	xiaomiBalanceCache    sync.Map // key: cred hash → *xiaomiBalanceEntry
	xiaomiBalanceFetching sync.Map // key: cred hash → struct{} (dedup in-flight fetches)
)

type xiaomiBalanceEntry struct {
	balance *XiaomiBalance
	err     error
}

// GetXiaomiBalanceWithCreds returns a cached xiaomi balance for the given
// credential bundle, or fetches a fresh one if the cache is stale.
func GetXiaomiBalanceWithCreds(creds XiaomiCredentials, cfg *config.Config, auth *cliproxyauth.Auth) *XiaomiBalance {
	if !creds.HasAny() {
		return nil
	}
	key := xiaomiCredKey(creds)

	if val, ok := xiaomiBalanceCache.Load(key); ok {
		entry := val.(*xiaomiBalanceEntry)
		if entry.balance != nil && time.Since(entry.balance.FetchedAt) < xiaomiBalanceRefreshInterval {
			return entry.balance
		}
	}
	return fetchAndCacheXiaomiBalance(key, creds, cfg, auth)
}

// RefreshXiaomiBalanceWithCreds forces a fresh fetch using the provided
// credential bundle, bypassing the cache.
func RefreshXiaomiBalanceWithCreds(creds XiaomiCredentials, cfg *config.Config, auth *cliproxyauth.Auth) (*XiaomiBalance, error) {
	if !creds.HasAny() {
		return nil, fmt.Errorf("no xiaomi credentials provided")
	}
	key := xiaomiCredKey(creds)
	xiaomiBalanceCache.Delete(key)

	balance := fetchAndCacheXiaomiBalance(key, creds, cfg, auth)
	if balance == nil {
		if val, ok := xiaomiBalanceCache.Load(key); ok {
			entry := val.(*xiaomiBalanceEntry)
			if entry.err != nil {
				return nil, entry.err
			}
		}
		return nil, fmt.Errorf("failed to fetch xiaomi balance")
	}
	return balance, nil
}

func fetchAndCacheXiaomiBalance(key string, creds XiaomiCredentials, cfg *config.Config, auth *cliproxyauth.Auth) *XiaomiBalance {
	if _, busy := xiaomiBalanceFetching.LoadOrStore(key, struct{}{}); busy {
		if val, ok := xiaomiBalanceCache.Load(key); ok {
			entry := val.(*xiaomiBalanceEntry)
			if entry.balance != nil {
				return entry.balance
			}
		}
		return nil
	}
	defer xiaomiBalanceFetching.Delete(key)

	ctx, cancel := context.WithTimeout(context.Background(), xiaomiBalanceFetchTimeout)
	defer cancel()

	balance, err := fetchXiaomiBalance(ctx, creds, cfg, auth)
	if err != nil {
		log.Debugf("xiaomi balance fetch failed: %v", err)
		if val, ok := xiaomiBalanceCache.Load(key); ok {
			entry := val.(*xiaomiBalanceEntry)
			if entry.balance != nil {
				return entry.balance
			}
		}
		xiaomiBalanceCache.Store(key, &xiaomiBalanceEntry{err: err})
		return nil
	}
	xiaomiBalanceCache.Store(key, &xiaomiBalanceEntry{balance: balance})
	return balance
}

func xiaomiCredKey(creds XiaomiCredentials) string {
	h := sha256.New()
	h.Write([]byte(strings.TrimSpace(creds.Cookies)))
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

func httpClientForXiaomi(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) *http.Client {
	if cfg != nil || auth != nil {
		return NewProxyAwareHTTPClient(ctx, cfg, auth, xiaomiBalanceFetchTimeout)
	}
	return http.DefaultClient
}

func fetchXiaomiBalance(ctx context.Context, creds XiaomiCredentials, cfg *config.Config, auth *cliproxyauth.Auth) (*XiaomiBalance, error) {
	cookies := strings.TrimSpace(creds.Cookies)
	if cookies == "" {
		return nil, fmt.Errorf("missing cookie")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, xiaomiTokenPlanUsageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Cookie", cookies)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "cli-proxy-api/xiaomi-balance")
	req.Header.Set("Referer", "https://platform.xiaomimimo.com/console/plan-manage")

	client := httpClientForXiaomi(ctx, cfg, auth)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tokenPlan/usage: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("xiaomi balance: close response body error: %v", errClose)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tokenPlan/usage returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	balance, err := parseXiaomiTokenPlanUsage(body)
	if err != nil {
		return nil, err
	}
	balance.Source = "cookie"
	balance.FetchedAt = time.Now()
	return balance, nil
}

// parseXiaomiTokenPlanUsage extracts month/plan/compensation counters from the
// tokenPlan/usage response.
//
//	{"code":0,"data":{
//	  "monthUsage":{"percent":0.0022,"items":[
//	    {"name":"month_total_token","used":444590,"limit":200000000,"percent":0.0022}]},
//	  "usage":{"percent":0,"items":[
//	    {"name":"plan_total_token","used":444590,"limit":200000000,"percent":0},
//	    {"name":"compensation_total_token","used":0,"limit":0,"percent":0}]}}}
func parseXiaomiTokenPlanUsage(body []byte) (*XiaomiBalance, error) {
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"message"`
		Data struct {
			MonthUsage struct {
				Percent float64 `json:"percent"`
				Items   []struct {
					Name    string  `json:"name"`
					Used    int64   `json:"used"`
					Limit   int64   `json:"limit"`
					Percent float64 `json:"percent"`
				} `json:"items"`
			} `json:"monthUsage"`
			Usage struct {
				Percent float64 `json:"percent"`
				Items   []struct {
					Name    string  `json:"name"`
					Used    int64   `json:"used"`
					Limit   int64   `json:"limit"`
					Percent float64 `json:"percent"`
				} `json:"items"`
			} `json:"usage"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode tokenPlan/usage response: %w", err)
	}
	if payload.Code != 0 {
		msg := strings.TrimSpace(payload.Msg)
		if msg == "" {
			msg = fmt.Sprintf("code=%d", payload.Code)
		}
		return nil, fmt.Errorf("xiaomi platform error: %s", msg)
	}

	balance := &XiaomiBalance{
		MonthPercent: payload.Data.MonthUsage.Percent,
		PlanPercent:  payload.Data.Usage.Percent,
	}
	for _, item := range payload.Data.MonthUsage.Items {
		if item.Name == "month_total_token" || balance.MonthLimit == 0 {
			balance.MonthUsed = item.Used
			balance.MonthLimit = item.Limit
			if item.Percent != 0 {
				balance.MonthPercent = item.Percent
			}
		}
	}
	for _, item := range payload.Data.Usage.Items {
		switch item.Name {
		case "plan_total_token":
			balance.PlanUsed = item.Used
			balance.PlanLimit = item.Limit
			if item.Percent != 0 {
				balance.PlanPercent = item.Percent
			}
		case "compensation_total_token":
			balance.CompensationUsed = item.Used
			balance.CompensationLimit = item.Limit
		}
	}
	return balance, nil
}
