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

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
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
	Cookies  string
	Email    string
	Password string
}

func (c XiaomiCredentials) HasAny() bool {
	return strings.TrimSpace(c.Cookies) != "" || (strings.TrimSpace(c.Email) != "" && strings.TrimSpace(c.Password) != "")
}

const (
	xiaomiBalanceRefreshInterval = 10 * time.Minute
	xiaomiBalanceFetchTimeout    = 60 * time.Second
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
// When creds are empty but config.yaml has xiaomi-platform configured,
// it attempts automatic login to obtain platform cookies.
func GetXiaomiBalanceWithCreds(creds XiaomiCredentials, cfg *config.Config, auth *cliproxyauth.Auth) *XiaomiBalance {
	if !creds.HasAny() && (cfg == nil || !cfg.XiaomiPlatform.Enabled()) {
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
// credential bundle, bypassing the cache. When creds are empty but
// config.yaml has xiaomi-platform credentials configured, it attempts
// automatic login to obtain platform cookies.
func RefreshXiaomiBalanceWithCreds(creds XiaomiCredentials, cfg *config.Config, auth *cliproxyauth.Auth) (*XiaomiBalance, error) {
	if !creds.HasAny() && (cfg == nil || !cfg.XiaomiPlatform.Enabled()) {
		return nil, fmt.Errorf("no xiaomi credentials provided; pass 'cookie' or configure xiaomi-email/xiaomi-password in api-key-entries")
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

	balance, err := fetchXiaomiBalance(creds, cfg, auth)
	if err != nil {
		log.Warnf("xiaomi balance: 获取失败: %v", err)
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
	h.Write([]byte(strings.TrimSpace(creds.Email)))
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

func httpClientForXiaomi() *http.Client {
	return &http.Client{Timeout: xiaomiBalanceFetchTimeout}
}

func fetchXiaomiBalance(creds XiaomiCredentials, cfg *config.Config, auth *cliproxyauth.Auth) (*XiaomiBalance, error) {
	log.Info("xiaomi balance: 开始获取用量")

	cookies, err := obtainXiaomiCookiesForBalance(creds, cfg, auth, false)
	if err != nil {
		return nil, err
	}

	balance, retryKind := doXiaomiBalanceRequest(cookies)
	if balance != nil {
		if creds.Cookies != "" {
			balance.Source = "cookie"
		} else {
			balance.Source = "auto-login"
		}
		balance.FetchedAt = time.Now()
		return balance, nil
	}

	// 只有 401/403（cookie 过期）才清除缓存并重新登录重试。
	// 超时是网络问题，重登也无济于事，直接返回错误避免误触发浏览器弹窗。
	if retryKind == retryAuthFailed {
		log.Info("xiaomi balance: cookie 过期，清除缓存并重新登录...")
		clearXiaomiCookieCache(creds)
		cookies, err = obtainXiaomiCookiesForBalance(creds, cfg, auth, true)
		if err != nil {
			return nil, err
		}
		balance, _ = doXiaomiBalanceRequest(cookies)
		if balance != nil {
			balance.Source = "auto-login"
			balance.FetchedAt = time.Now()
			return balance, nil
		}
	}

	if retryKind == retryTimeout {
		// 超时：清除可能过期的缓存，但不自动重登
		log.Info("xiaomi balance: 请求超时，清除缓存")
		clearXiaomiCookieCache(creds)
		return nil, fmt.Errorf("tokenPlan/usage: timeout")
	}
	return nil, fmt.Errorf("tokenPlan/usage failed")
}

type balanceRetryKind int

const (
	retryNone       balanceRetryKind = iota // 不可重试的错误
	retryTimeout                            // 超时，清缓存但不自动重登
	retryAuthFailed                         // 401/403，清缓存并重登
)

// obtainXiaomiCookiesForBalance 获取 cookies，forceRelogin 为 true 时跳过缓存强制登录。
func obtainXiaomiCookiesForBalance(creds XiaomiCredentials, cfg *config.Config, auth *cliproxyauth.Auth, forceRelogin bool) (string, error) {
	cookies := strings.TrimSpace(creds.Cookies)

	if !forceRelogin {
		// 如果有 per-key 凭据，只使用 per-account 缓存
		if cookies == "" && creds.Email != "" {
			cookies = GetXiaomiAccountCookies(creds.Email)
			if cookies != "" {
				log.Infof("xiaomi balance: 使用 per-account 缓存 cookies (email=%s)", creds.Email)
			}
		}

		// 无 per-key 凭据时，使用全局平台 cookies
		if cookies == "" && creds.Email == "" {
			cookies = GetXiaomiPlatformCookies()
			if cookies != "" {
				log.Info("xiaomi balance: 使用全局缓存 cookies")
			}
		}
	}

	// 如果仍然没有 cookies，尝试自动登录
	if cookies == "" {
		log.Info("xiaomi balance: 无缓存 cookies，尝试自动登录...")
		var err error
		if creds.Email != "" && creds.Password != "" {
			log.Infof("xiaomi balance: 使用 per-key 凭据登录 (email=%s)", creds.Email)
			err = RefreshXiaomiCookiesFromCreds(creds.Email, creds.Password, cfg, auth)
			if err == nil {
				cookies = GetXiaomiAccountCookies(creds.Email)
			}
		} else {
			log.Info("xiaomi balance: 使用全局配置登录")
			err = RefreshXiaomiCookiesFromConfig(cfg, auth)
			if err == nil {
				cookies = GetXiaomiPlatformCookies()
			}
		}
		if err != nil {
			log.Warnf("xiaomi balance: 自动登录失败: %v", err)
			return "", fmt.Errorf("没有 cookie 且自动登录失败: %w", err)
		}
		if cookies == "" {
			return "", fmt.Errorf("自动登录成功但未获取到平台 cookie")
		}
		log.Info("xiaomi balance: 自动登录成功，已获取 cookies")
	}
	return cookies, nil
}

// doXiaomiBalanceRequest 执行一次余额 API 请求。
func doXiaomiBalanceRequest(cookies string) (*XiaomiBalance, balanceRetryKind) {
	log.Infof("xiaomi balance: 请求 %s", xiaomiTokenPlanUsageURL)
	ctx, cancel := context.WithTimeout(context.Background(), xiaomiBalanceFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, xiaomiTokenPlanUsageURL, nil)
	if err != nil {
		log.Warnf("xiaomi balance: create request error: %v", err)
		return nil, retryNone
	}
	req.Header.Set("Cookie", cookies)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "cli-proxy-api/xiaomi-balance")
	req.Header.Set("Referer", "https://platform.xiaomimimo.com/console/plan-manage")

	client := httpClientForXiaomi()
	resp, err := client.Do(req)
	if err != nil {
		log.Warnf("xiaomi balance: HTTP 请求失败: %v", err)
		return nil, retryTimeout
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("xiaomi balance: close response body error: %v", errClose)
		}
	}()
	log.Infof("xiaomi balance: HTTP 响应 status=%d", resp.StatusCode)
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			log.Warnf("xiaomi balance: cookie 过期 (status=%d)，需要重新登录", resp.StatusCode)
			return nil, retryAuthFailed
		}
		log.Warnf("xiaomi balance: 非预期状态码 %d", resp.StatusCode)
		return nil, retryNone
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Warnf("xiaomi balance: read response error: %v", err)
		return nil, retryNone
	}

	balance, err := parseXiaomiTokenPlanUsage(body)
	if err != nil {
		log.Warnf("xiaomi balance: 解析响应失败: %v", err)
		return nil, retryNone
	}
	log.Infof("xiaomi balance: 获取成功 (month=%.2f%%, plan=%.2f%%)", balance.MonthPercent*100, balance.PlanPercent*100)
	return balance, retryNone
}

// clearXiaomiCookieCache 清除指定凭据对应的 cookie 缓存。
func clearXiaomiCookieCache(creds XiaomiCredentials) {
	if creds.Email != "" {
		SetXiaomiAccountCookies(creds.Email, "", 0)
		DeleteXiaomiCookiesFile(creds.Email)
		log.Infof("xiaomi balance: 已清除 per-account 缓存 (email=%s)", creds.Email)
	} else {
		SetXiaomiPlatformCookies("", 0)
		DeleteXiaomiCookiesFile("")
		log.Info("xiaomi balance: 已清除全局缓存")
	}
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
