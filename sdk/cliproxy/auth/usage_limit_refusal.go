package auth

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// usageLimitMinResetIn separates cooldowns a client should wait out through
// Retry-After from exhausted quota it should stop on. A shorter cooldown keeps
// the retryable model_cooldown response.
const usageLimitMinResetIn = 5 * time.Minute

// usageLimitRefusal describes a pool whose every credential is out of Codex
// quota: when the first one reopens and the length of its window.
type usageLimitRefusal struct {
	resetAt       time.Time
	windowMinutes int64
}

// NewUsageLimitModelCooldownError creates a model cooldown that reports a
// final Codex usage limit, as when every candidate credential cools down on an
// upstream usage_limit_reached refusal. windowMinutes may be 0 when unknown.
func NewUsageLimitModelCooldownError(model, provider string, resetAt time.Time, windowMinutes int64) error {
	now := time.Now()
	return newModelCooldownError(model, provider, resetAt.Sub(now)).withUsageLimit(&usageLimitRefusal{
		resetAt:       resetAt,
		windowMinutes: windowMinutes,
	})
}

// IsUsageLimitReachedError reports whether err is a model cooldown in which
// every candidate credential is out of quota until a known reset.
func IsUsageLimitReachedError(err error) bool {
	var cooldown *modelCooldownError
	return errors.As(err, &cooldown) && cooldown.IsUsageLimitReached()
}

// usageLimitRefusalFor reports the usage limit that blocks every candidate, or
// nil when any candidate is blocked for another reason, is not blocked, or the
// first reset is too close for the client to stop on. Codex's
// usage_limit_reached refusal is the only evidence: a long or repeated generic
// 429 stays retryable.
func usageLimitRefusalFor(auths []*Auth, modelFor func(*Auth) string, now time.Time) *usageLimitRefusal {
	var refusal *usageLimitRefusal
	for _, auth := range auths {
		model := ""
		if modelFor != nil {
			model = modelFor(auth)
		}
		blocked, reason, next := isAuthBlockedForModel(auth, model, now)
		if !blocked || reason != blockReasonCooldown || !next.After(now) {
			return nil
		}
		window, ok := codexUsageLimitEvidence(auth, model, now)
		if !ok {
			return nil
		}
		if refusal == nil || next.Before(refusal.resetAt) {
			refusal = &usageLimitRefusal{resetAt: next, windowMinutes: window}
		}
	}
	if refusal == nil || refusal.resetAt.Sub(now) <= usageLimitMinResetIn {
		return nil
	}
	return refusal
}

// codexUsageLimitEvidence reports whether the cooldown blocking auth for model
// was set by a Codex usage_limit_reached refusal, and that refusal's window.
// It reads the state isAuthBlockedForModel decides on: a credential-wide quota
// alone, otherwise every blocked state of the model.
func codexUsageLimitEvidence(auth *Auth, model string, now time.Time) (int64, bool) {
	if auth == nil {
		return 0, false
	}
	if auth.Quota.Exceeded && auth.Quota.Reason == "credential_quota" && auth.Quota.NextRecoverAt.After(now) {
		return codexUsageLimitWindow(auth.LastError)
	}
	if model != "" && len(auth.ModelStates) > 0 {
		modelKey := canonicalModelKey(model)
		matched := false
		var window int64
		for stateModel, state := range auth.ModelStates {
			if state == nil || canonicalModelKey(stateModel) != modelKey {
				continue
			}
			blocked, _, _ := availabilityBlock(state.Unavailable, state.Quota.Exceeded, state.NextRetryAfter, state.Quota.NextRecoverAt, now)
			if !blocked {
				continue
			}
			stateWindow, ok := codexUsageLimitWindow(state.LastError)
			if !ok {
				return 0, false
			}
			matched = true
			window = stateWindow
		}
		if matched {
			return window, true
		}
	}
	return codexUsageLimitWindow(auth.LastError)
}

// codexUsageLimitWindow reports whether err records Codex's 429
// usage_limit_reached body, over HTTP or as a WebSocket error event, and the
// window it names in minutes (0 when absent).
func codexUsageLimitWindow(err *Error) (int64, bool) {
	if err == nil || err.HTTPStatus != http.StatusTooManyRequests {
		return 0, false
	}
	body := strings.TrimSpace(err.Message)
	if !gjson.Valid(body) {
		// An executor may prefix the upstream body, as in "status 429: {...}".
		if idx := strings.Index(body, ": {"); idx != -1 && idx < 50 {
			body = strings.TrimSpace(body[idx+2:])
		}
	}
	if !gjson.Valid(body) {
		return 0, false
	}
	errorNode := gjson.Get(body, "error")
	if !errorNode.IsObject() || !strings.EqualFold(strings.TrimSpace(errorNode.Get("type").String()), "usage_limit_reached") {
		return 0, false
	}
	window := errorNode.Get("limit_window_minutes").Int()
	if window < 0 {
		window = 0
	}
	return window, true
}

// codexRateLimitHeaders carries the exhausted window in the rate-limit headers
// Codex reads from a usage_limit_reached response. Codex reports a 5-hour
// window as primary and a weekly one as secondary; a client that knows the
// window learns when to resume.
func (r *usageLimitRefusal) codexRateLimitHeaders(headers http.Header) {
	if r == nil {
		return
	}
	slot := "Primary"
	if r.windowMinutes >= 24*60 {
		slot = "Secondary"
	}
	prefix := "X-Codex-" + slot + "-"
	headers.Set(prefix+"Used-Percent", "100")
	if r.windowMinutes > 0 {
		headers.Set(prefix+"Window-Minutes", strconv.FormatInt(r.windowMinutes, 10))
	}
	headers.Set(prefix+"Reset-At", strconv.FormatInt(r.resetAt.Unix(), 10))
}
