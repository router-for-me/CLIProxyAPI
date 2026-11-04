package auth

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
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

// UsageLimitResponseText returns the response body of the usage-limit cooldown
// in err's chain, so a wrapped error still answers with error.type
// usage_limit_reached.
func UsageLimitResponseText(err error) (string, bool) {
	var cooldown *modelCooldownError
	if !errors.As(err, &cooldown) || !cooldown.IsUsageLimitReached() {
		return "", false
	}
	return cooldown.Error(), true
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
		window, ok := codexUsageLimitEvidence(auth, model, next, now)
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
// until next was set by a Codex usage_limit_reached refusal, and that refusal's
// window. A mark MarkResult left for that cooldown decides first; without one it
// reads the state isAuthBlockedForModel decides on: a credential-wide quota
// alone, otherwise every blocked state of the model.
func codexUsageLimitEvidence(auth *Auth, model string, next, now time.Time) (int64, bool) {
	if auth == nil {
		return 0, false
	}
	if window, ok := markedUsageLimit(auth, model, next); ok {
		return window, true
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

// markCodexUsageLimit records on the quota state that err, a Codex
// usage_limit_reached refusal, set the cooldown that now blocks auth for model.
// MarkResult replaces LastError with every later failure, for example a stream
// that ends in a 502 while the quota is still out; the mark keeps the
// classification for as long as that cooldown lasts, across a restart too. It
// changes nothing when err is not that refusal or no cooldown blocks the model.
func markCodexUsageLimit(auth *Auth, model string, err *Error, now time.Time) {
	if auth == nil {
		return
	}
	window, ok := codexUsageLimitWindow(err)
	if !ok {
		return
	}
	blocked, reason, next := isAuthBlockedForModel(auth, model, now)
	if !blocked || reason != blockReasonCooldown || !next.After(now) {
		return
	}
	if auth.Quota.Exceeded && auth.Quota.Reason == "credential_quota" && auth.Quota.NextRecoverAt.After(now) {
		auth.Quota.UsageLimitResetAt, auth.Quota.UsageLimitWindowMinutes = next, window
	}
	if model == "" {
		return
	}
	modelKey := canonicalModelKey(model)
	for stateModel, state := range auth.ModelStates {
		if state != nil && canonicalModelKey(stateModel) == modelKey {
			state.Quota.UsageLimitResetAt, state.Quota.UsageLimitWindowMinutes = next, window
		}
	}
}

// markedUsageLimit returns the window of a usage-limit mark left for the
// cooldown that ends at next. A mark from an earlier cooldown ends at another
// time and does not count.
func markedUsageLimit(auth *Auth, model string, next time.Time) (int64, bool) {
	if usageLimitMarkMatches(auth.Quota, next) {
		return auth.Quota.UsageLimitWindowMinutes, true
	}
	if model == "" {
		return 0, false
	}
	modelKey := canonicalModelKey(model)
	for stateModel, state := range auth.ModelStates {
		if state != nil && canonicalModelKey(stateModel) == modelKey && usageLimitMarkMatches(state.Quota, next) {
			return state.Quota.UsageLimitWindowMinutes, true
		}
	}
	return 0, false
}

func usageLimitMarkMatches(quota QuotaState, next time.Time) bool {
	if quota.UsageLimitResetAt.IsZero() || next.IsZero() {
		return false
	}
	diff := quota.UsageLimitResetAt.Sub(next)
	if diff < 0 {
		diff = -diff
	}
	return diff <= time.Second
}

// restoreUsageLimitMark carries a persisted usage-limit mark onto a restored
// quota state.
func restoreUsageLimitMark(dst *QuotaState, src QuotaState) {
	if dst == nil || src.UsageLimitResetAt.IsZero() {
		return
	}
	dst.UsageLimitResetAt, dst.UsageLimitWindowMinutes = src.UsageLimitResetAt, src.UsageLimitWindowMinutes
}

// withoutPinnedUsageLimit turns a usage-limit cooldown back into the plain,
// retryable one when the request was pinned to one credential. The pick then saw
// only that credential, so its quota says nothing about the rest of the pool: a
// websocket turn whose pinned credential ran out reconnects onto another one,
// and a client told to stop would not.
func withoutPinnedUsageLimit(err error, opts cliproxyexecutor.Options) error {
	if err == nil || pinnedAuthIDFromMetadata(opts.Metadata) == "" {
		return err
	}
	var cooldown *modelCooldownError
	if errors.As(err, &cooldown) && cooldown != nil {
		cooldown.usageLimit = nil
	}
	return err
}

// codexUsageLimitWindow reports whether err records Codex's 429
// usage_limit_reached refusal, in any envelope the Codex executor stores it in
// (HTTP body, top-level object, WebSocket or SSE error event), and the window
// it names in minutes (0 when absent).
func codexUsageLimitWindow(err *Error) (int64, bool) {
	if err == nil || err.HTTPStatus != http.StatusTooManyRequests {
		return 0, false
	}
	body := strings.TrimSpace(err.Message)
	if !ojson.Valid(body) {
		// An executor may prefix the upstream body, as in "status 429: {...}".
		if idx := strings.Index(body, ": {"); idx != -1 && idx < 50 {
			body = strings.TrimSpace(body[idx+2:])
		}
	}
	if !ojson.Valid(body) {
		return 0, false
	}
	root := ojson.Parse(body)
	for _, node := range []ojson.Result{root.Get("error"), root, root.Get("body.error"), root.Get("response.error")} {
		if !node.IsObject() || !strings.EqualFold(strings.TrimSpace(node.Get("type").String()), "usage_limit_reached") {
			continue
		}
		window := node.Get("limit_window_minutes").Int()
		if window < 0 {
			window = 0
		}
		return window, true
	}
	return 0, false
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
