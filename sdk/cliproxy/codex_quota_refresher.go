package cliproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	log "github.com/sirupsen/logrus"
)

const (
	defaultCodexQuotaScanInterval       = 15 * time.Minute
	defaultCodexQuotaMinAccountInterval = 30 * time.Minute
	defaultCodexQuotaAccountStagger     = 3 * time.Second
	codexQuotaRequestTimeout            = 15 * time.Second
)

type codexQuotaRefresher struct {
	manager *coreauth.Manager
	cfg     *config.Config

	mu        sync.Mutex
	lastCheck map[string]time.Time
}

type codexUsageResponse struct {
	RateLimit *codexUsageRateLimit `json:"rate_limit"`
}

type codexUsageRateLimit struct {
	LimitReached    *bool                  `json:"limit_reached"`
	PrimaryWindow   *codexUsageQuotaWindow `json:"primary_window"`
	SecondaryWindow *codexUsageQuotaWindow `json:"secondary_window"`
}

type codexUsageQuotaWindow struct {
	UsedPercent float64 `json:"used_percent"`
}

func newCodexQuotaRefresher(manager *coreauth.Manager, cfg *config.Config) *codexQuotaRefresher {
	return &codexQuotaRefresher{manager: manager, cfg: cfg, lastCheck: make(map[string]time.Time)}
}

func (s *Service) restartCodexQuotaRefresher(parent context.Context, cfg *config.Config) {
	if s == nil {
		return
	}
	if s.codexQuotaRefreshCancel != nil {
		s.codexQuotaRefreshCancel()
		s.codexQuotaRefreshCancel = nil
	}
	if s.coreManager == nil || cfg == nil || !cfg.CodexQuotaRefresh.IsEnabled() {
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	refreshCtx, cancel := context.WithCancel(parent)
	s.codexQuotaRefreshCancel = cancel
	refresher := newCodexQuotaRefresher(s.coreManager, cfg)
	go refresher.run(refreshCtx)
}

func (r *codexQuotaRefresher) run(ctx context.Context) {
	if r == nil || r.manager == nil || r.cfg == nil || !r.cfg.CodexQuotaRefresh.IsEnabled() {
		return
	}
	interval := quotaRefreshDuration(r.cfg.CodexQuotaRefresh.ScanInterval, defaultCodexQuotaScanInterval)
	if !waitCodexQuotaRefresh(ctx, jitterCodexQuotaDuration(interval)) {
		return
	}
	for {
		r.scan(ctx)
		if !waitCodexQuotaRefresh(ctx, jitterCodexQuotaDuration(interval)) {
			return
		}
	}
}

func (r *codexQuotaRefresher) scan(ctx context.Context) {
	minInterval := quotaRefreshDuration(r.cfg.CodexQuotaRefresh.MinAccountInterval, defaultCodexQuotaMinAccountInterval)
	stagger := quotaRefreshDuration(r.cfg.CodexQuotaRefresh.AccountStagger, defaultCodexQuotaAccountStagger)
	for _, auth := range r.manager.List() {
		if ctx.Err() != nil {
			return
		}
		if !isCodexQuotaCooldown(auth) || !r.shouldCheck(auth.ID, minInterval) {
			continue
		}
		r.markChecked(auth.ID)
		recovered, errCheck := r.checkAuth(ctx, auth)
		if errCheck != nil {
			log.Warnf("codex quota refresh failed for auth %s: %v", auth.ID, errCheck)
		} else if recovered {
			if _, _, errReset := r.manager.ResetQuota(ctx, auth.ID); errReset != nil {
				log.Warnf("codex quota refresh failed to resume auth %s: %v", auth.ID, errReset)
			} else {
				log.Infof("codex quota recovered for auth %s; cooldown cleared", auth.ID)
			}
		}
		if stagger > 0 && !waitCodexQuotaRefresh(ctx, jitterCodexQuotaDuration(stagger)) {
			return
		}
	}
}

func (r *codexQuotaRefresher) shouldCheck(authID string, interval time.Duration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	last := r.lastCheck[authID]
	return last.IsZero() || time.Since(last) >= interval
}

func (r *codexQuotaRefresher) markChecked(authID string) {
	r.mu.Lock()
	r.lastCheck[authID] = time.Now()
	r.mu.Unlock()
}

func (r *codexQuotaRefresher) checkAuth(ctx context.Context, auth *coreauth.Auth) (bool, error) {
	token := authMetadataString(auth, "access_token")
	if token == "" {
		return false, fmt.Errorf("access token is missing")
	}
	accountID := authMetadataString(auth, "account_id")
	urls := codexUsageEndpoints(auth)
	client := helps.NewProxyAwareHTTPClient(ctx, r.cfg, auth, codexQuotaRequestTimeout)
	var lastErr error
	for _, endpoint := range urls {
		req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if errRequest != nil {
			return false, errRequest
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		if accountID != "" {
			req.Header.Set("ChatGPT-Account-ID", accountID)
		}
		resp, errDo := client.Do(req)
		if errDo != nil {
			lastErr = errDo
			continue
		}
		body, errRead := io.ReadAll(resp.Body)
		errClose := resp.Body.Close()
		if errRead != nil {
			lastErr = errRead
			continue
		}
		if errClose != nil {
			log.Debugf("codex quota refresh response close failed: %v", errClose)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("upstream returned status %d", resp.StatusCode)
			continue
		}
		var usage codexUsageResponse
		if errJSON := json.Unmarshal(body, &usage); errJSON != nil || !validCodexUsageRateLimit(usage.RateLimit) {
			lastErr = fmt.Errorf("invalid usage response")
			continue
		}
		return !codexUsageExhausted(usage.RateLimit), nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("usage endpoint unavailable")
	}
	return false, lastErr
}

func isCodexQuotaCooldown(auth *coreauth.Auth) bool {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") || auth.Disabled {
		return false
	}
	if authMetadataString(auth, "access_token") == "" {
		return false
	}
	if auth.Quota.Exceeded && strings.EqualFold(strings.TrimSpace(auth.Quota.Reason), "quota") {
		return true
	}
	for _, state := range auth.ModelStates {
		if state != nil && state.Quota.Exceeded && strings.EqualFold(strings.TrimSpace(state.Quota.Reason), "quota") {
			return true
		}
	}
	return false
}

func codexUsageEndpoints(auth *coreauth.Auth) []string {
	baseURL := "https://chatgpt.com/backend-api"
	if auth != nil && auth.Attributes != nil {
		if configured := strings.TrimRight(strings.TrimSpace(auth.Attributes["base_url"]), "/"); configured != "" {
			baseURL = strings.TrimSuffix(configured, "/codex")
		}
	}
	if strings.Contains(baseURL, "/backend-api") {
		return []string{baseURL + "/wham/usage", baseURL + "/codex/usage"}
	}
	return []string{baseURL + "/api/codex/usage", baseURL + "/codex/usage"}
}

func codexUsageExhausted(limit *codexUsageRateLimit) bool {
	if limit == nil || (limit.LimitReached != nil && *limit.LimitReached) {
		return true
	}
	for _, window := range []*codexUsageQuotaWindow{limit.PrimaryWindow, limit.SecondaryWindow} {
		if window != nil && window.UsedPercent >= 100 {
			return true
		}
	}
	return false
}

func validCodexUsageRateLimit(limit *codexUsageRateLimit) bool {
	return limit != nil && (limit.LimitReached != nil || limit.PrimaryWindow != nil || limit.SecondaryWindow != nil)
}

func authMetadataString(auth *coreauth.Auth, key string) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	value, _ := auth.Metadata[key].(string)
	return strings.TrimSpace(value)
}

func quotaRefreshDuration(raw string, fallback time.Duration) time.Duration {
	parsed, errParse := time.ParseDuration(strings.TrimSpace(raw))
	if errParse != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func jitterCodexQuotaDuration(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	spread := base / 5
	if spread <= 0 {
		return base
	}
	return base - spread + time.Duration(rand.Int64N(int64(spread*2)+1))
}

func waitCodexQuotaRefresh(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
