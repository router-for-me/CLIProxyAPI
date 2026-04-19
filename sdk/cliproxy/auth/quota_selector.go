package auth

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type oauthQuotaRoutingMode string

const (
	oauthQuotaRoutingModeBurstSyncSticky  oauthQuotaRoutingMode = "burst-sync-sticky"
	oauthQuotaRoutingModeReserveStaggered oauthQuotaRoutingMode = "reserve-staggered"
	oauthQuotaRoutingModeWeeklyGuarded    oauthQuotaRoutingMode = "weekly-guarded-sticky"
	maxQuotaRoutingResetHorizon                                 = 30 * 24 * time.Hour
	oauthQuotaWeeklySoftFloorMax                                = 40.0
	oauthQuotaWeeklyHardFloorMax                                = 20.0
)

// OAuthQuotaBurstSyncStickySelector eagerly activates accounts with unstarted
// windows, then sticks to the account whose short reset is closest.
type OAuthQuotaBurstSyncStickySelector struct{}

// OAuthQuotaReserveStaggeredSelector consumes already-triggered accounts first
// and keeps fresher accounts in reserve.
type OAuthQuotaReserveStaggeredSelector struct{}

// OAuthQuotaWeeklyGuardedStickySelector prefers already-active healthy accounts,
// but stops over-consuming an account when its weekly window gets too depleted.
type OAuthQuotaWeeklyGuardedStickySelector struct{}

type quotaWindowSignal struct {
	Label        string
	Scope        string
	Triggered    bool
	LimitReached bool
	UsedPercent  float64
	ResetAfter   time.Duration
	ResetKnown   bool
	Duration     time.Duration
}

type quotaRoutingMetrics struct {
	auth                *Auth
	shortWindow         *quotaWindowSignal
	longWindow          *quotaWindowSignal
	shortResetAfter     time.Duration
	longResetAfter      time.Duration
	shortResetKnown     bool
	longResetKnown      bool
	shortRemaining      float64
	longRemaining       float64
	totalRemaining      float64
	untriggeredRelevant int
	triggeredRelevant   int
	hasQuotaSignals     bool
	nextRecoverAfter    time.Duration
	nextRecoverKnown    bool
}

func (s *OAuthQuotaBurstSyncStickySelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	return pickOAuthQuotaAware(ctx, provider, model, opts, auths, oauthQuotaRoutingModeBurstSyncSticky)
}

func (s *OAuthQuotaReserveStaggeredSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	return pickOAuthQuotaAware(ctx, provider, model, opts, auths, oauthQuotaRoutingModeReserveStaggered)
}

func (s *OAuthQuotaWeeklyGuardedStickySelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	return pickOAuthQuotaAware(ctx, provider, model, opts, auths, oauthQuotaRoutingModeWeeklyGuarded)
}

func pickOAuthQuotaAware(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth, mode oauthQuotaRoutingMode) (*Auth, error) {
	_ = opts
	now := time.Now()
	available, err := getAvailableAuths(auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	if len(available) == 1 {
		return available[0], nil
	}

	metrics := make([]quotaRoutingMetrics, 0, len(available))
	for i := 0; i < len(available); i++ {
		metrics = append(metrics, buildQuotaRoutingMetrics(available[i], model, now))
	}

	sort.SliceStable(metrics, func(i, j int) bool {
		switch mode {
		case oauthQuotaRoutingModeReserveStaggered:
			return compareReserveQuotaRouting(metrics[i], metrics[j])
		case oauthQuotaRoutingModeWeeklyGuarded:
			return compareWeeklyGuardedQuotaRouting(metrics[i], metrics[j])
		default:
			return compareBurstQuotaRouting(metrics[i], metrics[j])
		}
	})
	return metrics[0].auth, nil
}

func buildQuotaRoutingMetrics(auth *Auth, model string, now time.Time) quotaRoutingMetrics {
	metrics := quotaRoutingMetrics{
		auth:            auth,
		shortResetAfter: maxQuotaRoutingResetHorizon,
		longResetAfter:  maxQuotaRoutingResetHorizon,
		shortRemaining:  100,
		longRemaining:   100,
	}

	windowSignals := collectQuotaWindowSignals(auth, model, now)
	if len(windowSignals) > 0 {
		metrics.hasQuotaSignals = true
	}
	for i := 0; i < len(windowSignals); i++ {
		signal := windowSignals[i]
		if signal == nil {
			continue
		}
		remaining := clampPercent(100 - signal.UsedPercent)
		metrics.totalRemaining += remaining

		switch signal.Scope {
		case "short":
			if metrics.shortWindow == nil || quotaWindowSortLess(signal, metrics.shortWindow) {
				metrics.shortWindow = signal
			}
		case "long":
			if metrics.longWindow == nil || quotaWindowSortLess(signal, metrics.longWindow) {
				metrics.longWindow = signal
			}
		}
	}

	if metrics.shortWindow != nil {
		metrics.shortResetAfter = metrics.shortWindow.ResetAfter
		metrics.shortResetKnown = metrics.shortWindow.ResetKnown
		metrics.shortRemaining = clampPercent(100 - metrics.shortWindow.UsedPercent)
		if metrics.shortWindow.Triggered {
			metrics.triggeredRelevant++
		} else {
			metrics.untriggeredRelevant++
		}
	}
	if metrics.longWindow != nil {
		metrics.longResetAfter = metrics.longWindow.ResetAfter
		metrics.longResetKnown = metrics.longWindow.ResetKnown
		metrics.longRemaining = clampPercent(100 - metrics.longWindow.UsedPercent)
		if metrics.longWindow.Triggered {
			metrics.triggeredRelevant++
		} else {
			metrics.untriggeredRelevant++
		}
	}

	nextRecover, ok := authNextRecoverAfter(auth, model, now)
	metrics.nextRecoverAfter = nextRecover
	metrics.nextRecoverKnown = ok

	return metrics
}

func compareBurstQuotaRouting(a, b quotaRoutingMetrics) bool {
	if a.hasQuotaSignals != b.hasQuotaSignals {
		return a.hasQuotaSignals
	}
	if a.untriggeredRelevant != b.untriggeredRelevant {
		return a.untriggeredRelevant > b.untriggeredRelevant
	}
	if lessDurationWithKnown(a.shortResetAfter, a.shortResetKnown, b.shortResetAfter, b.shortResetKnown) {
		return true
	}
	if lessDurationWithKnown(b.shortResetAfter, b.shortResetKnown, a.shortResetAfter, a.shortResetKnown) {
		return false
	}
	if lessDurationWithKnown(a.longResetAfter, a.longResetKnown, b.longResetAfter, b.longResetKnown) {
		return true
	}
	if lessDurationWithKnown(b.longResetAfter, b.longResetKnown, a.longResetAfter, a.longResetKnown) {
		return false
	}
	if !floatAlmostEqual(a.totalRemaining, b.totalRemaining) {
		return a.totalRemaining > b.totalRemaining
	}
	if lessDurationWithKnown(a.nextRecoverAfter, a.nextRecoverKnown, b.nextRecoverAfter, b.nextRecoverKnown) {
		return true
	}
	if lessDurationWithKnown(b.nextRecoverAfter, b.nextRecoverKnown, a.nextRecoverAfter, a.nextRecoverKnown) {
		return false
	}
	return authTieBreakLess(a.auth, b.auth)
}

func compareReserveQuotaRouting(a, b quotaRoutingMetrics) bool {
	if a.hasQuotaSignals != b.hasQuotaSignals {
		return a.hasQuotaSignals
	}
	if a.untriggeredRelevant != b.untriggeredRelevant {
		return a.untriggeredRelevant < b.untriggeredRelevant
	}
	if lessDurationWithKnown(a.shortResetAfter, a.shortResetKnown, b.shortResetAfter, b.shortResetKnown) {
		return true
	}
	if lessDurationWithKnown(b.shortResetAfter, b.shortResetKnown, a.shortResetAfter, a.shortResetKnown) {
		return false
	}
	if lessDurationWithKnown(a.longResetAfter, a.longResetKnown, b.longResetAfter, b.longResetKnown) {
		return true
	}
	if lessDurationWithKnown(b.longResetAfter, b.longResetKnown, a.longResetAfter, a.longResetKnown) {
		return false
	}
	if !floatAlmostEqual(a.longRemaining, b.longRemaining) {
		return a.longRemaining < b.longRemaining
	}
	if !floatAlmostEqual(a.totalRemaining, b.totalRemaining) {
		return a.totalRemaining < b.totalRemaining
	}
	if lessDurationWithKnown(a.nextRecoverAfter, a.nextRecoverKnown, b.nextRecoverAfter, b.nextRecoverKnown) {
		return true
	}
	if lessDurationWithKnown(b.nextRecoverAfter, b.nextRecoverKnown, a.nextRecoverAfter, a.nextRecoverKnown) {
		return false
	}
	return authTieBreakLess(a.auth, b.auth)
}

type quotaGuardTier int

const (
	quotaGuardTierHealthyTriggered quotaGuardTier = iota
	quotaGuardTierCautionTriggered
	quotaGuardTierReserveUntriggered
	quotaGuardTierDangerDepleted
)

func compareWeeklyGuardedQuotaRouting(a, b quotaRoutingMetrics) bool {
	if a.hasQuotaSignals != b.hasQuotaSignals {
		return a.hasQuotaSignals
	}

	aTier := weeklyGuardTier(a)
	bTier := weeklyGuardTier(b)
	if aTier != bTier {
		return aTier < bTier
	}

	switch aTier {
	case quotaGuardTierHealthyTriggered, quotaGuardTierCautionTriggered:
		if lessDurationWithKnown(a.shortResetAfter, a.shortResetKnown, b.shortResetAfter, b.shortResetKnown) {
			return true
		}
		if lessDurationWithKnown(b.shortResetAfter, b.shortResetKnown, a.shortResetAfter, a.shortResetKnown) {
			return false
		}
		if lessDurationWithKnown(a.longResetAfter, a.longResetKnown, b.longResetAfter, b.longResetKnown) {
			return true
		}
		if lessDurationWithKnown(b.longResetAfter, b.longResetKnown, a.longResetAfter, a.longResetKnown) {
			return false
		}

		aSoft, aHard := weeklyGuardFloors(a)
		bSoft, bHard := weeklyGuardFloors(b)
		aHeadroom := weeklyGuardHeadroom(a.longRemaining, aSoft, aHard)
		bHeadroom := weeklyGuardHeadroom(b.longRemaining, bSoft, bHard)
		if !floatAlmostEqual(aHeadroom, bHeadroom) {
			return aHeadroom > bHeadroom
		}
		if !floatAlmostEqual(a.longRemaining, b.longRemaining) {
			return a.longRemaining > b.longRemaining
		}
		if !floatAlmostEqual(a.totalRemaining, b.totalRemaining) {
			return a.totalRemaining > b.totalRemaining
		}
	case quotaGuardTierReserveUntriggered:
		if a.untriggeredRelevant != b.untriggeredRelevant {
			return a.untriggeredRelevant < b.untriggeredRelevant
		}
		if !floatAlmostEqual(a.longRemaining, b.longRemaining) {
			return a.longRemaining > b.longRemaining
		}
		if !floatAlmostEqual(a.totalRemaining, b.totalRemaining) {
			return a.totalRemaining > b.totalRemaining
		}
	case quotaGuardTierDangerDepleted:
		if lessDurationWithKnown(a.shortResetAfter, a.shortResetKnown, b.shortResetAfter, b.shortResetKnown) {
			return true
		}
		if lessDurationWithKnown(b.shortResetAfter, b.shortResetKnown, a.shortResetAfter, a.shortResetKnown) {
			return false
		}
		if !floatAlmostEqual(a.longRemaining, b.longRemaining) {
			return a.longRemaining > b.longRemaining
		}
	}

	if lessDurationWithKnown(a.nextRecoverAfter, a.nextRecoverKnown, b.nextRecoverAfter, b.nextRecoverKnown) {
		return true
	}
	if lessDurationWithKnown(b.nextRecoverAfter, b.nextRecoverKnown, a.nextRecoverAfter, a.nextRecoverKnown) {
		return false
	}
	return authTieBreakLess(a.auth, b.auth)
}

func weeklyGuardTier(metrics quotaRoutingMetrics) quotaGuardTier {
	if metrics.untriggeredRelevant > 0 {
		return quotaGuardTierReserveUntriggered
	}

	softFloor, hardFloor := weeklyGuardFloors(metrics)
	if softFloor <= 0 && hardFloor <= 0 {
		return quotaGuardTierHealthyTriggered
	}
	if metrics.longRemaining <= hardFloor {
		return quotaGuardTierDangerDepleted
	}
	if metrics.longRemaining <= softFloor {
		return quotaGuardTierCautionTriggered
	}
	return quotaGuardTierHealthyTriggered
}

func weeklyGuardFloors(metrics quotaRoutingMetrics) (softFloor float64, hardFloor float64) {
	if metrics.longWindow == nil {
		return 0, 0
	}

	scale := 1.0
	if metrics.longWindow.Duration > 0 {
		if metrics.longResetKnown {
			scale = float64(metrics.longResetAfter) / float64(metrics.longWindow.Duration)
		}
	}
	if scale < 0 {
		scale = 0
	}
	if scale > 1 {
		scale = 1
	}

	softFloor = oauthQuotaWeeklySoftFloorMax * scale
	hardFloor = oauthQuotaWeeklyHardFloorMax * scale
	if hardFloor > softFloor {
		hardFloor = softFloor
	}
	return softFloor, hardFloor
}

func weeklyGuardHeadroom(longRemaining, softFloor, hardFloor float64) float64 {
	if softFloor <= 0 && hardFloor <= 0 {
		return longRemaining
	}
	if longRemaining <= hardFloor {
		return longRemaining - hardFloor
	}
	return longRemaining - softFloor
}

func authTieBreakLess(a, b *Auth) bool {
	left := authStableKey(a)
	right := authStableKey(b)
	if left == right {
		return false
	}
	return left < right
}

func authStableKey(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if idx := strings.TrimSpace(auth.EnsureIndex()); idx != "" {
		return idx
	}
	if id := strings.TrimSpace(auth.ID); id != "" {
		return id
	}
	if name := strings.TrimSpace(auth.FileName); name != "" {
		return name
	}
	return fmt.Sprintf("%p", auth)
}

func lessDurationWithKnown(left time.Duration, leftKnown bool, right time.Duration, rightKnown bool) bool {
	if leftKnown != rightKnown {
		return leftKnown
	}
	if left != right {
		return left < right
	}
	return false
}

func clampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func floatAlmostEqual(a, b float64) bool {
	return math.Abs(a-b) < 0.0001
}

func quotaWindowSortLess(left, right *quotaWindowSignal) bool {
	if left == nil || right == nil {
		return left != nil
	}
	if left.Triggered != right.Triggered {
		return left.Triggered
	}
	if lessDurationWithKnown(left.ResetAfter, left.ResetKnown, right.ResetAfter, right.ResetKnown) {
		return true
	}
	if lessDurationWithKnown(right.ResetAfter, right.ResetKnown, left.ResetAfter, left.ResetKnown) {
		return false
	}
	if !floatAlmostEqual(left.UsedPercent, right.UsedPercent) {
		return left.UsedPercent > right.UsedPercent
	}
	return left.Label < right.Label
}

func authNextRecoverAfter(auth *Auth, model string, now time.Time) (time.Duration, bool) {
	if auth == nil {
		return 0, false
	}
	next := time.Time{}
	if model != "" && len(auth.ModelStates) > 0 {
		if state := lookupModelState(auth, model); state != nil {
			next = earliestNonZeroTime(next, state.NextRetryAfter)
			next = earliestNonZeroTime(next, state.Quota.NextRecoverAt)
		}
	}
	next = earliestNonZeroTime(next, auth.NextRetryAfter)
	next = earliestNonZeroTime(next, auth.Quota.NextRecoverAt)
	if next.IsZero() {
		return 0, false
	}
	if next.Before(now) {
		return 0, true
	}
	return next.Sub(now), true
}

func collectQuotaWindowSignals(auth *Auth, model string, now time.Time) []*quotaWindowSignal {
	if auth == nil {
		return nil
	}
	var signals []*quotaWindowSignal
	signals = append(signals, quotaWindowSignalsFromMap(auth.Metadata, now)...)
	if state := lookupModelState(auth, model); state != nil {
		signals = append(signals, quotaWindowSignalsFromMap(modelStateMetadata(state), now)...)
	}
	return dedupeQuotaWindowSignals(signals)
}

func lookupModelState(auth *Auth, model string) *ModelState {
	if auth == nil || len(auth.ModelStates) == 0 || strings.TrimSpace(model) == "" {
		return nil
	}
	if state, ok := auth.ModelStates[model]; ok && state != nil {
		return state
	}
	baseModel := canonicalModelKey(model)
	if baseModel != "" && baseModel != model {
		if state, ok := auth.ModelStates[baseModel]; ok && state != nil {
			return state
		}
	}
	return nil
}

func modelStateMetadata(state *ModelState) map[string]any {
	if state == nil {
		return nil
	}
	meta := map[string]any{}
	if state.Quota.Exceeded {
		meta["quota_exceeded"] = true
	}
	if !state.Quota.NextRecoverAt.IsZero() {
		meta["next_recover_at"] = state.Quota.NextRecoverAt
	}
	if state.NextRetryAfter.After(state.UpdatedAt) {
		meta["next_retry_after"] = state.NextRetryAfter
	}
	return meta
}

func quotaWindowSignalsFromMap(meta map[string]any, now time.Time) []*quotaWindowSignal {
	if len(meta) == 0 {
		return nil
	}

	var signals []*quotaWindowSignal
	for _, key := range []string{"oauth_quota_windows", "quota_windows", "routing_windows", "quotaWindows"} {
		raw, ok := meta[key]
		if !ok {
			continue
		}
		signals = append(signals, quotaWindowSignalsFromValue(raw, key, now)...)
	}

	for _, key := range []string{
		"five_hour",
		"seven_day",
		"seven_day_oauth_apps",
		"seven_day_opus",
		"seven_day_sonnet",
		"seven_day_cowork",
		"primary_window",
		"secondary_window",
		"short_window",
		"long_window",
	} {
		raw, ok := meta[key]
		if !ok {
			continue
		}
		signals = append(signals, quotaWindowSignalsFromValue(raw, key, now)...)
	}

	return signals
}

func quotaWindowSignalsFromValue(raw any, key string, now time.Time) []*quotaWindowSignal {
	switch typed := raw.(type) {
	case []any:
		var signals []*quotaWindowSignal
		for i := 0; i < len(typed); i++ {
			signals = append(signals, quotaWindowSignalsFromValue(typed[i], key, now)...)
		}
		return signals
	case map[string]any:
		if windowsRaw, ok := typed["windows"]; ok {
			return quotaWindowSignalsFromValue(windowsRaw, key, now)
		}

		if looksLikeQuotaWindowObject(typed) {
			if signal := parseQuotaWindowSignal(typed, key, now); signal != nil {
				return []*quotaWindowSignal{signal}
			}
			return nil
		}

		var signals []*quotaWindowSignal
		for nestedKey, nestedValue := range typed {
			if nestedKey == "windows" {
				continue
			}
			signals = append(signals, quotaWindowSignalsFromValue(nestedValue, nestedKey, now)...)
		}
		return signals
	default:
		return nil
	}
}

func looksLikeQuotaWindowObject(raw map[string]any) bool {
	if len(raw) == 0 {
		return false
	}
	for _, key := range []string{
		"utilization",
		"used_percent",
		"usedPercent",
		"reset_at",
		"resets_at",
		"reset_after_seconds",
		"resetAfterSeconds",
		"window_scope",
		"windowScope",
		"window_duration_seconds",
		"windowDurationSeconds",
		"limit_window_seconds",
		"limitReached",
		"limit_reached",
		"triggered",
		"active",
		"started",
	} {
		if _, ok := raw[key]; ok {
			return true
		}
	}
	return false
}

func parseQuotaWindowSignal(raw map[string]any, fallbackLabel string, now time.Time) *quotaWindowSignal {
	if len(raw) == 0 {
		return nil
	}

	label := firstString(raw, "label", "name", "window_key", "windowKey")
	if label == "" {
		label = fallbackLabel
	}

	scope := normalizeQuotaWindowScope(firstString(raw, "scope", "window_scope", "windowScope"), label)
	duration := durationFromAny(raw["window_duration_seconds"])
	if duration <= 0 {
		duration = durationFromAny(raw["windowDurationSeconds"])
	}
	if duration <= 0 {
		duration = durationFromAny(raw["limit_window_seconds"])
	}
	if scope == "other" {
		scope = inferQuotaWindowScope(label, duration)
	}

	usedPercent := firstFloat(raw, "used_percent", "usedPercent", "utilization")
	limitReached := firstBool(raw, "limit_reached", "limitReached")

	resetAt, okResetAt := firstTime(raw, "reset_at", "resets_at", "next_recover_at")
	resetAfter := durationFromAny(raw["reset_after_seconds"])
	if resetAfter <= 0 {
		resetAfter = durationFromAny(raw["resetAfterSeconds"])
	}
	resetKnown := false
	if okResetAt {
		resetKnown = true
		if resetAt.Before(now) {
			resetAfter = 0
		} else {
			resetAfter = resetAt.Sub(now)
		}
	}
	if !resetKnown && resetAfter > 0 {
		resetKnown = true
	}
	if resetAfter > maxQuotaRoutingResetHorizon {
		resetAfter = maxQuotaRoutingResetHorizon
	}

	triggered, hasTriggered := firstBoolWithPresence(raw, "triggered", "active", "started", "window_triggered", "windowTriggered")
	if !hasTriggered {
		triggered = limitReached || usedPercent > 0 || resetKnown
	}

	return &quotaWindowSignal{
		Label:        label,
		Scope:        scope,
		Triggered:    triggered,
		LimitReached: limitReached,
		UsedPercent:  clampPercent(usedPercent),
		ResetAfter:   resetAfter,
		ResetKnown:   resetKnown,
		Duration:     duration,
	}
}

func dedupeQuotaWindowSignals(signals []*quotaWindowSignal) []*quotaWindowSignal {
	if len(signals) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(signals))
	deduped := make([]*quotaWindowSignal, 0, len(signals))
	for i := 0; i < len(signals); i++ {
		signal := signals[i]
		if signal == nil {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(signal.Scope)) + "|" + strings.ToLower(strings.TrimSpace(signal.Label)) + "|" + strconvDurationSeconds(signal.ResetAfter) + "|" + strconvBool(signal.Triggered)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, signal)
	}
	return deduped
}

func strconvDurationSeconds(value time.Duration) string {
	return fmt.Sprintf("%d", int64(value/time.Second))
}

func strconvBool(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func firstString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			if parsed, okParse := value.(string); okParse {
				parsed = strings.TrimSpace(parsed)
				if parsed != "" {
					return parsed
				}
			}
		}
	}
	return ""
}

func firstBool(raw map[string]any, keys ...string) bool {
	value, _ := firstBoolWithPresence(raw, keys...)
	return value
}

func firstBoolWithPresence(raw map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			if parsed, okParse := parseBoolAny(value); okParse {
				return parsed, true
			}
		}
	}
	return false, false
}

func firstFloat(raw map[string]any, keys ...string) float64 {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			switch typed := value.(type) {
			case float64:
				return typed
			case float32:
				return float64(typed)
			case int:
				return float64(typed)
			case int32:
				return float64(typed)
			case int64:
				return float64(typed)
			case string:
				trimmed := strings.TrimSpace(typed)
				if trimmed == "" {
					continue
				}
				if parsed, err := strconv.ParseFloat(trimmed, 64); err == nil {
					return parsed
				}
			}
		}
	}
	return 0
}

func firstTime(raw map[string]any, keys ...string) (time.Time, bool) {
	for _, key := range keys {
		if value, ok := raw[key]; ok {
			if parsed, okParse := parseTimeValue(value); okParse && !parsed.IsZero() {
				return parsed, true
			}
		}
	}
	return time.Time{}, false
}

func durationFromAny(value any) time.Duration {
	if value == nil {
		return 0
	}
	switch typed := value.(type) {
	case time.Duration:
		if typed > 0 {
			return typed
		}
		return 0
	case int:
		if typed > 0 {
			return time.Duration(typed) * time.Second
		}
	case int32:
		if typed > 0 {
			return time.Duration(typed) * time.Second
		}
	case int64:
		if typed > 0 {
			return time.Duration(typed) * time.Second
		}
	case float64:
		if typed > 0 {
			return time.Duration(typed * float64(time.Second))
		}
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return 0
		}
		if parsedDuration, err := time.ParseDuration(trimmed); err == nil && parsedDuration > 0 {
			return parsedDuration
		}
		if parsed, err := strconv.ParseFloat(trimmed, 64); err == nil && parsed > 0 {
			return time.Duration(parsed * float64(time.Second))
		}
	}
	return 0
}

func normalizeQuotaWindowScope(scope, label string) string {
	normalized := strings.ToLower(strings.TrimSpace(scope))
	switch normalized {
	case "short", "short-term", "short_term":
		return "short"
	case "long", "long-term", "long_term":
		return "long"
	default:
		return inferQuotaWindowScope(label, 0)
	}
}

func inferQuotaWindowScope(label string, duration time.Duration) string {
	normalized := strings.ToLower(strings.TrimSpace(label))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	normalized = strings.ReplaceAll(normalized, " ", "_")
	switch {
	case strings.Contains(normalized, "5_hour"), strings.Contains(normalized, "five_hour"):
		return "short"
	case strings.Contains(normalized, "7_day"), strings.Contains(normalized, "seven_day"), strings.Contains(normalized, "weekly"):
		return "long"
	case duration >= 6*24*time.Hour:
		return "long"
	case duration >= 4*time.Hour:
		return "short"
	default:
		return "other"
	}
}

func earliestNonZeroTime(current, candidate time.Time) time.Time {
	if candidate.IsZero() {
		return current
	}
	if current.IsZero() || candidate.Before(current) {
		return candidate
	}
	return current
}
