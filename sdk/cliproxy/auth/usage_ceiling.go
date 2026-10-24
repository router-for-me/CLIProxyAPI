package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// usageCeilingMaxDelay bounds how far ahead an observed window reset may lie.
// A weekly window plus slack covers clock skew; a later timestamp is not a
// usage window reset and must not pin the credential.
const usageCeilingMaxDelay = 8 * 24 * time.Hour

type usageWindowKind int

const (
	usageWindowShort usageWindowKind = iota
	usageWindowWeekly
)

func (k usageWindowKind) String() string {
	if k == usageWindowWeekly {
		return "weekly"
	}
	return "short"
}

// usageWindow is one provider quota window read from the passive quota snapshot.
type usageWindow struct {
	kind        usageWindowKind
	usedPercent float64
	resetAt     time.Time
}

// usageCeilingError reports that a credential is over its configured usage
// ceiling. It is credential-scoped so the whole credential cools down until
// the window resets.
type usageCeilingError struct {
	message    string
	retryAfter time.Duration
}

func (e *usageCeilingError) Error() string { return e.message }

func (e *usageCeilingError) StatusCode() int { return http.StatusTooManyRequests }

func (e *usageCeilingError) RetryAfter() *time.Duration {
	value := e.retryAfter
	return &value
}

func (e *usageCeilingError) IsCredentialScoped() bool { return true }

// usageCeilingLimits returns the weekly and short window caps for auth. Keys
// set in the auth file "usage_ceiling" object override the global config one
// by one, so an explicit 0 turns a global cap off for that credential.
func usageCeilingLimits(auth *Auth, cfg *internalconfig.Config) (weekly, short float64) {
	if cfg != nil {
		weekly = cfg.Routing.UsageCeiling.WeeklyPercent
		short = cfg.Routing.UsageCeiling.ShortWindowPercent
	}
	if auth == nil || auth.Metadata == nil {
		return weekly, short
	}
	raw, ok := auth.Metadata["usage_ceiling"]
	if !ok {
		raw = auth.Metadata["usage-ceiling"]
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return weekly, short
	}
	if value, okValue := usageCeilingPercent(values, "weekly_percent", "weekly-percent"); okValue {
		weekly = value
	}
	if value, okValue := usageCeilingPercent(values, "short_window_percent", "short-window-percent"); okValue {
		short = value
	}
	return weekly, short
}

func usageCeilingPercent(values map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		raw, ok := values[key]
		if !ok {
			continue
		}
		var value float64
		var errParse error
		switch typed := raw.(type) {
		case float64:
			value = typed
		case int:
			value = float64(typed)
		case int64:
			value = float64(typed)
		case json.Number:
			value, errParse = typed.Float64()
		case string:
			value, errParse = strconv.ParseFloat(strings.TrimSpace(typed), 64)
		default:
			continue
		}
		if errParse != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		return value, true
	}
	return 0, false
}

// observedUsageWindows reads the provider usage windows from the passive
// quota snapshot. A window without a reset time falls back to the snapshot
// time plus the window length.
func observedUsageWindows(provider string, quota QuotaState) []usageWindow {
	if len(quota.Signals) == 0 {
		return nil
	}
	signals := make(map[string]string, len(quota.Signals))
	for key, value := range quota.Signals {
		signals[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude":
		return claudeUsageWindows(signals, quota.ObservedAt)
	case "codex":
		return codexUsageWindows(signals, quota.ObservedAt)
	case "devin":
		return devinUsageWindows(signals, quota.ObservedAt)
	default:
		return nil
	}
}

func claudeUsageWindows(signals map[string]string, observedAt time.Time) []usageWindow {
	var windows []usageWindow
	for _, spec := range []struct {
		name   string
		kind   usageWindowKind
		length time.Duration
	}{
		{name: "5h", kind: usageWindowShort, length: 5 * time.Hour},
		{name: "7d", kind: usageWindowWeekly, length: 7 * 24 * time.Hour},
	} {
		prefix := "anthropic-ratelimit-unified-" + spec.name + "-"
		// Anthropic reports utilization as a 0-1 fraction.
		utilization, ok := parseUsageNumber(signals[prefix+"utilization"])
		if !ok {
			continue
		}
		resetAt, okReset := parseUnixSignal(signals[prefix+"reset"])
		if !okReset {
			resetAt, okReset = fallbackUsageReset(observedAt, spec.length)
		}
		if okReset {
			windows = append(windows, usageWindow{kind: spec.kind, usedPercent: utilization * 100, resetAt: resetAt})
		}
	}
	return windows
}

func codexUsageWindows(signals map[string]string, observedAt time.Time) []usageWindow {
	var windows []usageWindow
	for _, name := range []string{"primary", "secondary"} {
		prefix := "x-codex-" + name + "-"
		used, ok := parseUsageNumber(signals[prefix+"used-percent"])
		if !ok {
			continue
		}
		minutes, _ := parseUsageNumber(signals[prefix+"window-minutes"])
		kind := usageWindowShort
		switch {
		case minutes > 24*60:
			kind = usageWindowWeekly
		case minutes <= 0 && name == "secondary":
			kind = usageWindowWeekly
		}
		resetAt, okReset := parseUnixSignal(signals[prefix+"reset-at"])
		if !okReset && !observedAt.IsZero() {
			if seconds, okSeconds := parseUsageNumber(signals[prefix+"reset-after-seconds"]); okSeconds && seconds >= 0 {
				resetAt, okReset = observedAt.Add(time.Duration(seconds)*time.Second), true
			}
		}
		if !okReset && minutes > 0 {
			resetAt, okReset = fallbackUsageReset(observedAt, time.Duration(minutes)*time.Minute)
		}
		if okReset {
			windows = append(windows, usageWindow{kind: kind, usedPercent: used, resetAt: resetAt})
		}
	}
	return windows
}

func devinUsageWindows(signals map[string]string, observedAt time.Time) []usageWindow {
	var windows []usageWindow
	for _, spec := range []struct {
		name   string
		kind   usageWindowKind
		length time.Duration
	}{
		{name: "daily", kind: usageWindowShort, length: 24 * time.Hour},
		{name: "weekly", kind: usageWindowWeekly, length: 7 * 24 * time.Hour},
	} {
		// Devin reports the remaining percent, such as "35%".
		remaining, ok := parseUsageNumber(strings.TrimSuffix(signals[spec.name+"_quota_remaining_percent"], "%"))
		if !ok {
			continue
		}
		resetAt, errParse := time.Parse(time.RFC3339, signals[spec.name+"_quota_reset_at"])
		okReset := errParse == nil
		if !okReset {
			resetAt, okReset = fallbackUsageReset(observedAt, spec.length)
		}
		if okReset {
			windows = append(windows, usageWindow{kind: spec.kind, usedPercent: 100 - remaining, resetAt: resetAt})
		}
	}
	return windows
}

func parseUsageNumber(raw string) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	value, errParse := strconv.ParseFloat(raw, 64)
	if errParse != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

func parseUnixSignal(raw string) (time.Time, bool) {
	seconds, ok := parseUsageNumber(raw)
	if !ok || seconds <= 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(seconds), 0), true
}

func fallbackUsageReset(observedAt time.Time, length time.Duration) (time.Time, bool) {
	if observedAt.IsZero() {
		return time.Time{}, false
	}
	return observedAt.Add(length), true
}

// usageCeilingBreach returns a usageCeilingError when auth's observed usage is
// at or over a configured cap and that window has not reset yet. When several
// windows are over their caps, the latest reset wins.
func usageCeilingBreach(auth *Auth, cfg *internalconfig.Config, now time.Time) error {
	if auth == nil {
		return nil
	}
	weekly, short := usageCeilingLimits(auth, cfg)
	if weekly <= 0 && short <= 0 {
		return nil
	}
	var hit *usageWindow
	var hitLimit float64
	for _, window := range observedUsageWindows(auth.Provider, auth.Quota) {
		limit := short
		if window.kind == usageWindowWeekly {
			limit = weekly
		}
		if limit <= 0 || window.usedPercent < limit {
			continue
		}
		delay := window.resetAt.Sub(now)
		if delay <= 0 || delay > usageCeilingMaxDelay {
			continue
		}
		if hit == nil || window.resetAt.After(hit.resetAt) {
			current := window
			hit = &current
			hitLimit = limit
		}
	}
	if hit == nil {
		return nil
	}
	return &usageCeilingError{
		message: fmt.Sprintf("usage ceiling reached: %s window at %.0f%% (limit %.0f%%), resets at %s",
			hit.kind, hit.usedPercent, hitLimit, hit.resetAt.UTC().Format(time.RFC3339)),
		retryAfter: hit.resetAt.Sub(now),
	}
}

// markUsageCeilingResult records a credential-scoped 429 when auth is over its
// usage ceiling, so routing skips it until the window resets. No upstream
// request is sent. It returns nil when auth is under its ceiling.
func (m *Manager) markUsageCeilingResult(ctx context.Context, auth *Auth, provider, routeModel string, opts cliproxyexecutor.Options) error {
	errCeiling := usageCeilingBreach(auth, m.runtimeConfigSnapshot(), time.Now())
	if errCeiling == nil {
		return nil
	}
	stateModel := m.selectionModelKeyForAuth(auth, routeModel)
	if stateModel == "" {
		stateModel = canonicalModelKey(routeModel)
	}
	m.MarkResult(ctx, Result{
		AuthID:               auth.ID,
		Provider:             provider,
		Model:                stateModel,
		RouteModel:           routeModel,
		Success:              false,
		Error:                resultErrorFromError(errCeiling),
		RetryAfter:           retryAfterFromError(errCeiling),
		CredentialScope:      true,
		Options:              opts,
		SkipQuotaObservation: true,
		CredentialVersion:    auth.CredentialVersion,
		RegistrationEpoch:    auth.RegistrationEpoch,
	})
	logEntryWithRequestID(ctx).Infof("skipping %s: %v", formatAuthIdentity(auth, provider), errCeiling)
	return errCeiling
}
