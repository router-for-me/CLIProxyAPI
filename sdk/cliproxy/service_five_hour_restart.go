package cliproxy

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const fiveHourRestartPollInterval = 30 * time.Second
const fiveHourRestartRetryInterval = 5 * time.Minute

type fiveHourRestartAttempt struct {
	inFlight     bool
	lastReset    time.Time
	nextFallback time.Time
	retryAfter   time.Time
}

type fiveHourRestartTracker struct {
	mu       sync.Mutex
	attempts map[string]fiveHourRestartAttempt
}

func (t *fiveHourRestartTracker) start(authID string, now, resetAt time.Time, hasReset bool) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	state := t.attempts[authID]
	if state.inFlight || now.Before(state.retryAfter) {
		return false
	}
	if hasReset && resetAt.After(state.lastReset) {
		if now.Before(resetAt.Add(5 * time.Second)) {
			return false
		}
	} else if !state.nextFallback.IsZero() && now.Before(state.nextFallback) {
		return false
	}
	state.inFlight = true
	t.attempts[authID] = state
	return true
}

func (t *fiveHourRestartTracker) finish(authID string, resetAt, finishedAt time.Time, success bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	state := t.attempts[authID]
	state.inFlight = false
	if success {
		if resetAt.After(state.lastReset) {
			state.lastReset = resetAt
		}
		state.nextFallback = finishedAt.Add(5 * time.Hour)
		state.retryAfter = time.Time{}
	} else {
		state.retryAfter = finishedAt.Add(fiveHourRestartRetryInterval)
	}
	t.attempts[authID] = state
}

// runFiveHourWindowRestart starts a window for credentials without an observed
// reset, then follows provider reset signals or a five-hour fallback schedule.
func (s *Service) runFiveHourWindowRestart(ctx context.Context) {
	if s == nil || s.coreManager == nil {
		return
	}
	ticker := time.NewTicker(fiveHourRestartPollInterval)
	defer ticker.Stop()
	tracker := &fiveHourRestartTracker{attempts: make(map[string]fiveHourRestartAttempt)}
	concurrent := make(chan struct{}, 4)
	for {
		s.restartDueFiveHourWindows(ctx, time.Now(), tracker, concurrent)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) restartDueFiveHourWindows(ctx context.Context, now time.Time, tracker *fiveHourRestartTracker, concurrent chan struct{}) {
	if ctx.Err() != nil {
		return
	}
	s.cfgMu.RLock()
	enabled := s.cfg != nil && s.cfg.RestartFiveHourWindow
	s.cfgMu.RUnlock()
	if !enabled {
		return
	}
	for _, auth := range s.coreManager.List() {
		if auth == nil || auth.Disabled || auth.Status == coreauth.StatusDisabled ||
			(auth.Provider != "codex" && auth.Provider != "claude") || claudeWeeklyWindowBlocked(auth, now) {
			continue
		}
		resetAt, ok := fiveHourResetAt(auth)
		if now.Before(auth.NextRetryAfter) || (auth.Quota.Exceeded && now.Before(auth.Quota.NextRecoverAt)) {
			continue
		}
		model := fiveHourRestartModel(auth)
		if model == "" {
			continue
		}
		select {
		case concurrent <- struct{}{}:
			if !tracker.start(auth.ID, now, resetAt, ok) {
				<-concurrent
				continue
			}
			go func(authID, provider, model string, resetAt time.Time) {
				defer func() { <-concurrent }()
				request, options := fiveHourRestartRequest(provider, model, authID)
				_, err := s.coreManager.Execute(ctx, []string{provider}, request, options)
				tracker.finish(authID, resetAt, time.Now(), err == nil)
				if err != nil && ctx.Err() == nil {
					log.WithError(err).WithFields(log.Fields{"provider": provider, "auth_id": authID}).Warn("five-hour window restart request failed")
				} else if err == nil {
					log.WithFields(log.Fields{"provider": provider, "auth_id": authID}).Info("five-hour window restart request succeeded")
				}
			}(auth.ID, auth.Provider, model, resetAt)
		default:
			return
		}
	}
}

func fiveHourResetAt(auth *coreauth.Auth) (time.Time, bool) {
	if auth == nil || auth.Disabled || auth.Quota.ObservedAt.IsZero() {
		return time.Time{}, false
	}
	signals := auth.Quota.Signals
	switch auth.Provider {
	case "claude":
		return parseQuotaResetUnix(signals["Anthropic-Ratelimit-Unified-5h-Reset"], auth.Quota.ObservedAt)
	case "codex":
		for _, window := range []string{"Primary", "Secondary"} {
			prefix := "X-Codex-" + window + "-"
			if signals[prefix+"Window-Minutes"] != "300" {
				continue
			}
			if reset, ok := parseQuotaResetUnix(signals[prefix+"Reset-At"], auth.Quota.ObservedAt); ok {
				return reset, true
			}
			seconds, err := strconv.ParseInt(signals[prefix+"Reset-After-Seconds"], 10, 64)
			if err == nil && seconds >= 0 && seconds <= int64((5*time.Hour)/time.Second) {
				return auth.Quota.ObservedAt.Add(time.Duration(seconds) * time.Second), true
			}
		}
	}
	return time.Time{}, false
}

func claudeWeeklyWindowBlocked(auth *coreauth.Auth, now time.Time) bool {
	if auth == nil || auth.Provider != "claude" {
		return false
	}
	signals := auth.Quota.Signals
	if !strings.EqualFold(signals["Anthropic-Ratelimit-Unified-7d-Status"], "rejected") {
		return false
	}
	reset, ok := parseQuotaResetUnix(signals["Anthropic-Ratelimit-Unified-7d-Reset"], auth.Quota.ObservedAt)
	return ok && now.Before(reset)
}

func parseQuotaResetUnix(raw string, observedAt time.Time) (time.Time, bool) {
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}, false
	}
	reset := time.Unix(seconds, 0)
	return reset, reset.After(observedAt)
}

func fiveHourRestartModel(auth *coreauth.Auth) string {
	models := registry.GetGlobalRegistry().GetModelsForClient(auth.ID)
	var fallback string
	for _, model := range models {
		if model == nil || model.ID == "" {
			continue
		}
		id := strings.ToLower(model.ID)
		if strings.Contains(id, "image") || strings.Contains(id, "audio") || strings.Contains(id, "realtime") {
			continue
		}
		if fallback == "" {
			fallback = model.ID
		}
		if auth.Provider == "claude" && strings.Contains(id, "haiku") ||
			auth.Provider == "codex" && (strings.Contains(id, "luna") || strings.Contains(id, "mini")) {
			return model.ID
		}
	}
	return fallback
}

func fiveHourRestartRequest(provider, model, authID string) (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	format := sdktranslator.FormatOpenAIResponse
	body := map[string]any{"model": model, "input": "Hi. Reply OK."}
	if provider == "claude" {
		format = sdktranslator.FormatClaude
		body = map[string]any{
			"model": model, "max_tokens": 8,
			"messages": []map[string]string{{"role": "user", "content": "Reply OK."}},
		}
	}
	payload, _ := json.Marshal(body)
	return cliproxyexecutor.Request{Model: model, Payload: payload, Format: format}, cliproxyexecutor.Options{
		SourceFormat: format,
		Metadata:     map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: authID},
	}
}
