package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const warmupCooldown = 20 * time.Second

var warmupMiniModelPatterns = []string{"*mini*"}

type WarmupQuotaSnapshot struct {
	HasFiveHourWindow      bool
	FiveHourResetRemaining time.Duration
	HasWeeklyWindow        bool
	WeeklyResetRemaining   time.Duration
}

type usageWindow struct {
	LimitWindowSeconds int64 `json:"limit_window_seconds"`
	ResetAfterSeconds  int64 `json:"reset_after_seconds"`
	ResetAt            int64 `json:"reset_at"`
}

type usageRateLimit struct {
	PrimaryWindow   *usageWindow `json:"primary_window"`
	SecondaryWindow *usageWindow `json:"secondary_window"`
}

type usageResponse struct {
	RateLimit usageRateLimit `json:"rate_limit"`
}

type WarmupListener struct {
	cfg               *config.Config
	authManager       *coreauth.Manager
	mu                sync.Mutex
	lastAcceptedAt    map[string]time.Time
	warmedAuthIndexes map[string]struct{}
	now               func() time.Time
	execute           func(context.Context, []string, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
}

func NewWarmupListener(cfg *config.Config, manager *coreauth.Manager) *WarmupListener {
	listener := &WarmupListener{
		cfg:               cfg,
		authManager:       manager,
		lastAcceptedAt:    map[string]time.Time{},
		warmedAuthIndexes: map[string]struct{}{},
	}
	if manager != nil {
		listener.execute = manager.Execute
	}
	return listener
}

func (l *WarmupListener) setConfig(cfg *config.Config) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.cfg = cfg
	l.mu.Unlock()
}

func (l *WarmupListener) currentConfig() *config.Config {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cfg
}

func (l *WarmupListener) setAuthManager(manager *coreauth.Manager) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.authManager = manager
	if manager != nil {
		l.execute = manager.Execute
	} else {
		l.execute = nil
	}
	l.mu.Unlock()
}

func (l *WarmupListener) currentAuthManager() *coreauth.Manager {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.authManager
}

func (l *WarmupListener) currentExecute() func(context.Context, []string, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.execute
}

func parseWarmupQuotaSnapshot(body []byte) (WarmupQuotaSnapshot, bool) {
	var resp usageResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return WarmupQuotaSnapshot{}, false
	}

	snapshot := WarmupQuotaSnapshot{}
	for _, window := range []*usageWindow{resp.RateLimit.PrimaryWindow, resp.RateLimit.SecondaryWindow} {
		if window == nil {
			continue
		}
		remaining := time.Duration(window.ResetAfterSeconds) * time.Second
		switch window.LimitWindowSeconds {
		case 5 * 60 * 60:
			snapshot.HasFiveHourWindow = true
			snapshot.FiveHourResetRemaining = remaining
		case 7 * 24 * 60 * 60:
			snapshot.HasWeeklyWindow = true
			snapshot.WeeklyResetRemaining = remaining
		}
	}

	return snapshot, true
}

func warmupAllowed(snapshot WarmupQuotaSnapshot) bool {
	if !snapshot.HasFiveHourWindow || !snapshot.HasWeeklyWindow {
		return false
	}
	return snapshot.FiveHourResetRemaining >= 5*time.Hour &&
		snapshot.WeeklyResetRemaining >= 7*24*time.Hour
}

func (l *WarmupListener) isWarmupEnabled() bool {
	cfg := l.currentConfig()
	if cfg == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(cfg.Routing.Strategy), "fill-first") && cfg.Routing.Warmup
}

func (l *WarmupListener) shouldSkipSource(authIndex string) bool {
	if l == nil {
		return true
	}
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.warmedAuthIndexes == nil {
		return false
	}
	_, ok := l.warmedAuthIndexes[authIndex]
	return ok
}

func (l *WarmupListener) markSourceWarmed(authIndex string) {
	if l == nil {
		return
	}
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.warmedAuthIndexes == nil {
		l.warmedAuthIndexes = map[string]struct{}{}
	}
	l.warmedAuthIndexes[authIndex] = struct{}{}
}

func (l *WarmupListener) shouldIgnoreByCooldown(authIndex string, now time.Time) bool {
	if l == nil {
		return true
	}
	authIndex = strings.TrimSpace(authIndex)
	if authIndex == "" {
		return true
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lastAcceptedAt == nil {
		l.lastAcceptedAt = map[string]time.Time{}
	}
	last, ok := l.lastAcceptedAt[authIndex]
	if ok && now.Sub(last) < warmupCooldown {
		return true
	}
	l.lastAcceptedAt[authIndex] = now
	return false
}

func hasNegativeModelState(auth *coreauth.Auth) bool {
	if auth == nil {
		return false
	}
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		if state.Status == coreauth.StatusDisabled || state.Status == coreauth.StatusError || state.Unavailable || state.Quota.Exceeded {
			return true
		}
		if !state.NextRetryAfter.IsZero() || state.LastError != nil {
			return true
		}
	}
	return false
}

func (l *WarmupListener) collectWarmupTargets(auths []*coreauth.Auth, sourceID string) []*coreauth.Auth {
	out := make([]*coreauth.Auth, 0)
	for _, auth := range auths {
		if auth == nil || auth.ID == sourceID {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			continue
		}
		accountType, _ := auth.AccountInfo()
		if accountType != "oauth" {
			continue
		}
		if auth.Disabled || auth.Status == coreauth.StatusDisabled || auth.Status == coreauth.StatusError || auth.Unavailable || auth.Quota.Exceeded || !auth.NextRetryAfter.IsZero() || auth.LastError != nil || hasNegativeModelState(auth) {
			continue
		}
		out = append(out, auth)
	}
	return out
}

func selectWarmupModel(authID string) string {
	models := registry.GetGlobalRegistry().GetModelsForClient(authID)
	for _, pattern := range warmupMiniModelPatterns {
		for _, model := range models {
			if model == nil {
				continue
			}
			id := strings.TrimSpace(model.ID)
			if id == "" {
				continue
			}
			if matchWarmupModelPattern(pattern, id) {
				return id
			}
		}
	}
	for _, model := range models {
		if model == nil {
			continue
		}
		id := strings.TrimSpace(model.ID)
		if id != "" {
			return id
		}
	}
	return ""
}

func matchWarmupModelPattern(pattern, model string) bool {
	pattern = strings.TrimSpace(pattern)
	model = strings.TrimSpace(model)
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}

	pi, si := 0, 0
	starIdx := -1
	matchIdx := 0
	for si < len(model) {
		if pi < len(pattern) && pattern[pi] == model[si] {
			pi++
			si++
			continue
		}
		if pi < len(pattern) && pattern[pi] == '*' {
			starIdx = pi
			matchIdx = si
			pi++
			continue
		}
		if starIdx != -1 {
			pi = starIdx + 1
			matchIdx++
			si = matchIdx
			continue
		}
		return false
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

func (l *WarmupListener) authByIndex(authIndex string) *coreauth.Auth {
	authIndex = strings.TrimSpace(authIndex)
	manager := l.currentAuthManager()
	if authIndex == "" || manager == nil {
		return nil
	}
	return findAuthByStableIndex(manager.List(), authIndex)
}

func isSourceCodexOAuthCandidate(auth *coreauth.Auth) bool {
	if auth == nil || strings.TrimSpace(auth.ID) == "" {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return false
	}
	accountType, _ := auth.AccountInfo()
	return accountType == "oauth"
}

func isWarmupUsageURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return false
	}
	if !strings.EqualFold(parsed.Hostname(), "api.openai.com") {
		return false
	}
	return parsed.Path == "/v1/usage"
}

func (l *WarmupListener) executeWarmup(ctx context.Context, target *coreauth.Auth, model string) error {
	if target == nil {
		return fmt.Errorf("warmup target is nil")
	}
	targetID := strings.TrimSpace(target.ID)
	model = strings.TrimSpace(model)
	if targetID == "" {
		return fmt.Errorf("warmup target id is empty")
	}
	if model == "" {
		return fmt.Errorf("warmup model is empty")
	}

	execFn := l.currentExecute()
	if execFn == nil {
		if manager := l.currentAuthManager(); manager != nil {
			execFn = manager.Execute
		}
	}
	if execFn == nil {
		return fmt.Errorf("warmup execute function unavailable")
	}

	payload, err := json.Marshal(map[string]string{
		"model": model,
		"input": "hello",
	})
	if err != nil {
		return err
	}

	_, err = execFn(ctx, []string{"codex"}, cliproxyexecutor.Request{
		Model:   model,
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
		Alt:          "",
		Metadata: map[string]any{
			cliproxyexecutor.PinnedAuthMetadataKey: targetID,
		},
	})
	return err
}

func (l *WarmupListener) OnManagementAPICall(ctx context.Context, evt ManagementAPICallEvent) {
	manager := l.currentAuthManager()
	if l == nil || !l.isWarmupEnabled() || manager == nil {
		return
	}
	if evt.StatusCode < 200 || evt.StatusCode >= 300 {
		return
	}

	sourceAuthIndex := strings.TrimSpace(evt.AuthIndex)
	if sourceAuthIndex == "" || l.shouldSkipSource(sourceAuthIndex) {
		return
	}

	source := l.authByIndex(sourceAuthIndex)
	if !isSourceCodexOAuthCandidate(source) {
		return
	}

	if !isWarmupUsageURL(evt.URL) {
		return
	}

	snapshot, ok := parseWarmupQuotaSnapshot(evt.RespBody)
	if !ok || !warmupAllowed(snapshot) {
		return
	}

	now := time.Now()
	if l.now != nil {
		now = l.now()
	}
	if l.shouldIgnoreByCooldown(sourceAuthIndex, now) {
		return
	}

	targets := l.collectWarmupTargets(manager.List(), source.ID)
	if len(targets) == 0 {
		return
	}

	hasSuccess := false
	for _, target := range targets {
		model := selectWarmupModel(target.ID)
		if strings.TrimSpace(model) == "" {
			continue
		}
		if err := l.executeWarmup(ctx, target, model); err != nil {
			log.WithError(err).Warnf("management api-call warmup failed for auth %s", target.ID)
			continue
		}
		hasSuccess = true
	}
	if hasSuccess {
		l.markSourceWarmed(sourceAuthIndex)
	}
}
