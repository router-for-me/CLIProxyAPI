package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// This file implements active quota polling + precise cooldown for Kimi Coding
// plan auths (base_url matching api.kimi.com/coding).
//
// Background: Kimi's inference API returns 403 with a vague message when quota is
// exhausted, without a precise reset time. However, the dedicated endpoint
// GET {base_url}/v1/usages returns limit/remaining/resetTime for both the 5-hour
// and weekly rolling windows. This file periodically queries that endpoint and,
// when a window is exhausted (remaining<=0), cools down all models for that auth
// to the real upstream resetTime. Recovery is lazy: once now > NextRetryAfter,
// isAuthBlockedForModel automatically allows the auth through again.
//
// Cooldown state is written at the model level (ModelStates) because the route
// selector isAuthBlockedForModel only reads model-level state when model!="".
// The three required fields are Unavailable + NextRetryAfter + Quota.Exceeded;
// auth-level aggregation is derived by updateAggregatedAvailability.

const (
	// kimiUsageBaseURL is the Kimi API base URL. The probe identifies Kimi auths
	// by matching this prefix against the configured base_url, regardless of
	// config provider type (claude_key or openai-compatibility).
	kimiUsageBaseURL = "https://api.kimi.com/coding"
	// kimiUsageReason is the label written to QuotaState.Reason to aid debugging
	// and cleanup.
	kimiUsageReason = "kimi quota exhausted"
	// kimiUsageMaxBody caps the usage response body to prevent memory blowup from
	// a misbehaving upstream.
	kimiUsageMaxBody = 1 << 20
)

// kimiUsageDetail represents the numeric values of a single window in /v1/usages.
// limit/remaining use json.Number to accept both integers and floats;
// resetTime can be ISO8601 string, Unix seconds, or Unix milliseconds — accepted
// as any and dispatched in parseKimiResetTime.
type kimiUsageDetail struct {
	Limit     json.Number `json:"limit"`
	Remaining json.Number `json:"remaining"`
	ResetTime any         `json:"resetTime"`
}

// kimiUsageResponse is the top-level structure of /v1/usages: limits[] holds
// 5-hour window details (possibly multiple), usage holds the weekly/cycle window,
// totalQuota is an additional account-level quota window (reported separately).
type kimiUsageResponse struct {
	Limits []struct {
		Detail kimiUsageDetail `json:"detail"`
	} `json:"limits"`
	Usage      kimiUsageDetail `json:"usage"`
	TotalQuota kimiUsageDetail `json:"totalQuota"`
}

// kimiUsageWindow is the parsed observable state of a single window.
type kimiUsageWindow struct {
	Name      string
	Limit     float64
	Remaining float64
	ResetAt   time.Time
	HasReset  bool
}

// parseKimiResetTime accepts three resetTime formats: ISO8601 string, Unix
// seconds, and Unix milliseconds. Returns ok=false when parsing fails (including
// zero/negative/empty values); callers should skip that window.
func parseKimiResetTime(v any) (time.Time, bool) {
	switch x := v.(type) {
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return time.Time{}, false
		}
		layouts := []string{
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02T15:04:05Z",
			"2006-01-02T15:04:05.000Z",
			"2006-01-02 15:04:05",
		}
		for _, layout := range layouts {
			if t, err := time.Parse(layout, s); err == nil {
				return t, true
			}
		}
		return time.Time{}, false
	case float64:
		if x <= 0 {
			return time.Time{}, false
		}
		// Values >= 1e12 are treated as milliseconds; smaller values are seconds.
		if x >= 1e12 {
			return time.UnixMilli(int64(x)), true
		}
		return time.Unix(int64(x), 0), true
	case int64:
		if x <= 0 {
			return time.Time{}, false
		}
		if x >= 1e12 {
			return time.UnixMilli(x), true
		}
		return time.Unix(x, 0), true
	case json.Number:
		f, err := x.Float64()
		if err != nil || f <= 0 {
			return time.Time{}, false
		}
		if f >= 1e12 {
			return time.UnixMilli(int64(f)), true
		}
		return time.Unix(int64(f), 0), true
	}
	return time.Time{}, false
}

// windowFromDetail parses a single detail into an observable window.
func windowFromDetail(d kimiUsageDetail, name string) kimiUsageWindow {
	limit, _ := d.Limit.Float64()
	remaining, _ := d.Remaining.Float64()
	resetAt, hasReset := parseKimiResetTime(d.ResetTime)
	return kimiUsageWindow{
		Name:      name,
		Limit:     limit,
		Remaining: remaining,
		ResetAt:   resetAt,
		HasReset:  hasReset,
	}
}

// isKimiUsageAuth checks whether an auth is a Kimi Coding auth with a queryable
// /v1/usages endpoint. Two shapes are covered:
//   - Native OAuth auths (Provider == "kimi") identified by provider and a bearer
//     token in Metadata; their base URL defaults to the Kimi coding endpoint.
//   - Manually configured openai-compatible (API key) auths identified by a
//     base_url prefix match.
//
// Disabled auths are excluded to avoid sending background traffic to credentials
// the operator has taken out of service (consistent with routing in selector.go).
func isKimiUsageAuth(auth *Auth) bool {
	if auth == nil || auth.Provider == "" {
		return false
	}
	// Skip disabled auths — the operator explicitly took them out of service.
	// Routing already excludes them; the probe should not waste their quota
	// on background /v1/usages calls.
	if auth.Disabled || auth.Status == StatusDisabled {
		return false
	}

	// Native Kimi OAuth auths (Provider == "kimi") carry the bearer token in
	// Metadata and have no api_key/base_url attributes. The Kimi executor reads
	// that token and defaults the base URL, so the probe polls them through the
	// same credential path as inference requests — covering the primary Kimi
	// login flow, not just manually configured compatibility keys.
	if auth.Provider == "kimi" {
		return hasKimiBearerToken(auth)
	}

	// openai-compatible (API key) auths: identify Kimi by base_url and require
	// an api_key, since the compat path injects the key from Attributes.
	if auth.Attributes == nil || strings.TrimSpace(auth.Attributes["api_key"]) == "" {
		return false
	}
	baseURL := strings.TrimSpace(auth.Attributes["base_url"])
	if baseURL == "" {
		return false
	}
	// Prefix match: base_url must equal kimiUsageBaseURL exactly or start with
	// kimiUsageBaseURL followed by "/" (to avoid matching e.g. coding-fake).
	u := strings.TrimSuffix(baseURL, "/")
	return u == kimiUsageBaseURL || strings.HasPrefix(u, kimiUsageBaseURL+"/")
}

// hasKimiBearerToken reports whether auth carries a usable Kimi bearer token,
// checking OAuth Metadata first then API-key Attributes. This mirrors the
// precedence of the Kimi executor's credential lookup so the probe treats a
// token the same way the request path does.
func hasKimiBearerToken(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["access_token"].(string); ok && strings.TrimSpace(v) != "" {
			return true
		}
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["access_token"]); v != "" {
			return true
		}
		if v := strings.TrimSpace(auth.Attributes["api_key"]); v != "" {
			return true
		}
	}
	return false
}

// fetchKimiUsage queries the /v1/usages endpoint for a Kimi auth. It reuses
// Manager.NewHttpRequest/HttpRequest for automatic Bearer injection and per-auth
// proxy routing (same path as inference requests). On failure it returns an error;
// the caller decides whether to log and skip. No cooldown is triggered here.
func (m *Manager) fetchKimiUsage(ctx context.Context, auth *Auth) ([]kimiUsageWindow, error) {
	if m == nil || auth == nil {
		return nil, fmt.Errorf("kimi usage: nil manager or auth")
	}
	// Native Kimi OAuth auths have no base_url attribute; default to the known
	// Kimi coding base URL (same constant the executor uses). Compatibility
	// (API key) auths carry their configured base_url.
	baseURL := strings.TrimSpace(auth.Attributes["base_url"])
	if baseURL == "" {
		baseURL = kimiUsageBaseURL
	}
	// Normalize versioned base URLs (e.g. https://api.kimi.com/coding/v1) so
	// that appending "/v1/usages" does not produce ".../v1/v1/usages".
	u := strings.TrimSuffix(baseURL, "/")
	if strings.HasSuffix(u, "/v1") {
		u = strings.TrimSuffix(u, "/v1")
	}
	targetURL := u + "/v1/usages"

	// Inject the per-auth RoundTripper so the probe request routes through the
	// same proxy as inference requests (e.g. for regional bypass).
	execCtx := ctx
	if rt := m.roundTripperFor(auth); rt != nil {
		execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
		execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
	}

	req, err := m.NewHttpRequest(execCtx, auth, http.MethodGet, targetURL, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("kimi usage: build request: %w", err)
	}
	resp, err := m.HttpRequest(execCtx, auth, req)
	if err != nil {
		return nil, fmt.Errorf("kimi usage: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, kimiUsageMaxBody))
	if err != nil {
		return nil, fmt.Errorf("kimi usage: read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 401/403 typically means the key is invalid, not "quota exhausted".
		// Let the normal error cooldown path handle it.
		return nil, fmt.Errorf("kimi usage: upstream status %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed kimiUsageResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("kimi usage: parse: %w", err)
	}

	windows := make([]kimiUsageWindow, 0, len(parsed.Limits)+2)
	for _, lim := range parsed.Limits {
		windows = append(windows, windowFromDetail(lim.Detail, "five_hour"))
	}
	windows = append(windows, windowFromDetail(parsed.Usage, "weekly"))
	// totalQuota is an additional account-level window reported separately from
	// limits[] and usage. When exhausted (remaining=0) while the parsed windows
	// are still positive, kimiUsageFullyAvailable would otherwise clear the
	// cooldown prematurely. Treat it as a separate observable window.
	windows = append(windows, windowFromDetail(parsed.TotalQuota, "total_quota"))
	return windows, nil
}

// kimiUsageCooldown computes the "account recovery time". The account is usable
// only when all windows have remaining quota, so we cool down to the latest
// resetTime among exhausted windows that have a valid resetTime.
// Returns ok=true when there is at least one actionable exhausted window with a
// resetTime; ok=false means either no window is exhausted, or all exhausted
// windows lack a resetTime (callers should fall back to the normal error cooldown).
// Windows with Limit<=0 (inactive/unreported) are skipped.
func kimiUsageCooldown(windows []kimiUsageWindow) (recoverAt time.Time, ok bool) {
	// An exhausted window with no usable resetTime makes the precise recovery time
	// unknowable: the account is usable only after ALL windows recover, so we must
	// not resume the auth at another window's reset while this one is still
	// exhausted. Track it and decline to set a precise cooldown (ok=false) so the
	// caller falls back to the generic backoff instead of guessing.
	hasExhaustedWithoutReset := false
	for _, w := range windows {
		if w.Limit <= 0 {
			continue
		}
		if w.Remaining > 0 {
			continue
		}
		if !w.HasReset {
			hasExhaustedWithoutReset = true
			continue
		}
		ok = true
		if w.ResetAt.After(recoverAt) {
			recoverAt = w.ResetAt
		}
	}
	if hasExhaustedWithoutReset {
		return time.Time{}, false
	}
	return recoverAt, ok
}

// kimiUsageFullyAvailable checks whether all observable windows have remaining
// quota (used to decide whether to clear a previously-set cooldown). Windows with
// Limit<=0 are considered inactive/unreported and are skipped. Returns false when
// the window list is empty or when no window with Limit>0 was observed (so a
// cooldown is never cleared based on empty/unparseable response data).
func kimiUsageFullyAvailable(windows []kimiUsageWindow) bool {
	if len(windows) == 0 {
		return false
	}
	hasValidWindow := false
	for _, w := range windows {
		if w.Limit <= 0 {
			continue
		}
		hasValidWindow = true
		if w.Remaining <= 0 {
			return false
		}
	}
	return hasValidWindow
}

// SetAuthQuotaExceeded marks all registered models for the given auth as
// "quota exhausted, cooled down until recoverAt". Called by the background usage
// probe, not through the request path. Under lock: writes model-level state +
// aggregates; outside lock: registry visibility + persistence. Follows the same
// concurrency pattern as MarkResult. Existing model states are classified as
// follows: the probe's own same-reason cooldowns are only extended, never
// shortened; explicit non-Kimi backoffs (Cloudflare, 429) are preserved; the
// generic 402/403 payment_required fallback (no Quota.Reason, LastError status
// 402/403) is replaced with the probe's precise upstream reset time; other
// no-reason cooldowns (401/404/model_not_supported/invalid_grant, distinguished
// from the 402/403 fallback by LastError.HTTPStatus) are preserved.
func (m *Manager) SetAuthQuotaExceeded(ctx context.Context, authID string, recoverAt time.Time, reason string) (*Auth, error) {
	if m == nil {
		return nil, nil
	}
	authID = strings.TrimSpace(authID)
	now := time.Now()
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = kimiUsageReason
	}
	// recoverAt must be in the future, otherwise it's meaningless (lazy expiry
	// would allow the auth through immediately).
	if authID == "" || recoverAt.IsZero() || !recoverAt.After(now) {
		return nil, nil
	}

	var snapshot *Auth
	changedModels := make([]string, 0)
	cooldownStateChanged := false

	m.mu.Lock()
	auth, ok := m.auths[authID]
	if !ok || auth == nil {
		m.mu.Unlock()
		return nil, nil
	}
	// Skip when cooldown is globally disabled; same semantics as MarkResult's disableCooling.
	if m.cooldownDisabledForAuth(auth) {
		m.mu.Unlock()
		return nil, nil
	}

	var cooldownRecordsBefore []CooldownStateRecord
	trackCooldownState := m.cooldownStore != nil
	if trackCooldownState {
		cooldownRecordsBefore = m.cooldownStateRecordsForAuthLocked(auth, now)
	}

	// Model set = registry-registered models ∪ existing ModelStates keys.
	// Registered models cover those that haven't triggered a failure yet, so
	// account-level exhaustion pre-cools all models without missing any.
	modelSet := make(map[string]struct{})
	for _, mid := range modelsForRegisteredAuth(authID) {
		modelSet[mid] = struct{}{}
	}
	for mid := range auth.ModelStates {
		modelSet[mid] = struct{}{}
	}

	for model := range modelSet {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		state := ensureModelState(auth, model)
		// Decide whether to overwrite this state with the probe's precise
		// cooldown. The probe's job is to replace the imprecise generic 402/403
		// fallback; every other cooldown must be preserved.
		switch {
		case state.Quota.Reason == reason && isKimiProbeOwnedCooldown(state):
			// Probe's own cooldown: only extend, never shorten.
			if !state.NextRetryAfter.IsZero() && state.NextRetryAfter.After(recoverAt) {
				continue
			}
		case state.Quota.Reason == reason:
			// Stale kimiUsageReason from an expired probe cooldown, now
			// overwritten by a fresh failure. If the fresh failure is a
			// generic 402/403 payment_required fallback, replace it with
			// the probe's precise reset time. Otherwise preserve the
			// fresh cooldown (401/404/etc.).
			// NOTE: isGenericPaymentFallback requires Quota.Reason == "",
			// which is false here (it is kimiUsageReason). Check
			// LastError.HTTPStatus directly instead.
			if state.LastError == nil || (state.LastError.HTTPStatus != http.StatusPaymentRequired && state.LastError.HTTPStatus != http.StatusForbidden) {
				continue
			}
		case state.Quota.Reason != "":
			// Explicit non-Kimi backoff (Cloudflare, 429): if the existing
			// cooldown ends before the Kimi quota reset, extend it to the
			// probe's recoverAt so the model is not unblocked while the
			// account-level Kimi quota is still exhausted. Only preserve
			// cooldowns that already last at least until recoverAt.
			if !state.NextRetryAfter.IsZero() && state.NextRetryAfter.After(recoverAt) {
				continue
			}
		case state.NextRetryAfter.IsZero():
			// Fresh model with no cooldown: set.
		case isGenericPaymentFallback(state):
			// Generic 402/403 payment_required fallback: replace with the
			// probe's precise reset time. MarkResult leaves Quota untouched for
			// 402/403, so it is identified by LastError.HTTPStatus.
		default:
			// Other no-reason cooldown (401/404/model_not_supported/invalid_grant):
			// MarkResult leaves Quota untouched for these too, but LastError
			// records the status, so we can tell them apart from the 402/403
			// fallback and preserve them.
			continue
		}
		state.Unavailable = true
		state.Status = StatusError
		state.StatusMessage = reason
		state.NextRetryAfter = recoverAt
		state.Quota = QuotaState{Exceeded: true, Reason: reason, NextRecoverAt: recoverAt}
		state.UpdatedAt = now
		changedModels = append(changedModels, model)
	}

	if len(changedModels) > 0 {
		auth.Status = StatusError
		auth.UpdatedAt = now
		updateAggregatedAvailability(auth, now)
	}
	// persist is a no-op for config-api-key auths (cooldown state goes to .cds
	// store), but we call it anyway for consistency.
	_ = m.persist(ctx, auth)
	snapshot = auth.Clone()
	if trackCooldownState {
		after := m.cooldownStateRecordsForAuthLocked(auth, now)
		cooldownStateChanged = !cooldownStateRecordsEqual(cooldownRecordsBefore, after)
	}
	m.mu.Unlock()

	// Outside lock: registry visibility (aligns with MarkResult's quota path;
	// affects /models listing and client routing).
	for _, model := range changedModels {
		registry.GetGlobalRegistry().SetModelQuotaExceeded(authID, model)
		registry.GetGlobalRegistry().SuspendClientModel(authID, model, "quota")
	}
	if m.scheduler != nil && snapshot != nil {
		m.scheduler.upsertAuth(snapshot)
	}
	if snapshot != nil && cooldownStateChanged {
		m.persistCooldownStates(context.Background())
	}
	return snapshot, nil
}

// hasKimiUsageCooldown reports whether any of the auth's model states still carry
// a cooldown written by the Kimi usage probe (Quota.Reason == kimiUsageReason),
// regardless of whether that cooldown's NextRetryAfter has already passed. The
// probe uses this instead of hasAuthQuotaExceeded (which only matches
// still-active future cooldowns) to decide whether to call clearKimiUsageCooldown
// after a healthy /v1/usages response. This matters because SetAuthQuotaExceeded
// also suspends each model in the registry; that registry suspension is only
// resumed inside clearKimiUsageCooldown. Once the reset time has passed the
// routing block expires lazily (isAuthBlockedForModel), but the registry-level
// suspension and the auth-level Status do not, so the probe still needs to clear
// the now-expired state to resume the model and restore the status.
func hasKimiUsageCooldown(auth *Auth) bool {
	if auth == nil {
		return false
	}
	for _, state := range auth.ModelStates {
		if isKimiProbeOwnedCooldown(state) {
			return true
		}
	}
	return false
}

// isKimiProbeOwnedCooldown reports whether state still represents a cooldown
// owned by the Kimi usage probe, as opposed to one that began as a Kimi cooldown
// but was since overwritten by a fresh non-quota failure.
//
// SetAuthQuotaExceeded writes NextRetryAfter == Quota.NextRecoverAt == recoverAt.
// A later non-quota MarkResult (401/404/5xx/...) overwrites NextRetryAfter but
// leaves Quota.Reason/NextRecoverAt untouched, so the two times diverge and the
// state must no longer be treated as a Kimi cooldown - clearing it would resume a
// model that is actually cooling down for a fresh unauthorized/not-found error.
// The state is probe-owned only when NextRetryAfter is still consistent with the
// probe's recover time: equal to Quota.NextRecoverAt, or zeroed by
// updateAggregatedAvailability after the Kimi cooldown expired (lazy clear).
func isKimiProbeOwnedCooldown(state *ModelState) bool {
	if state == nil || state.Quota.Reason != kimiUsageReason {
		return false
	}
	if state.NextRetryAfter.IsZero() {
		return true
	}
	return state.NextRetryAfter.Equal(state.Quota.NextRecoverAt)
}

// isGenericPaymentFallback reports whether state is the generic 402/403
// payment_required fallback set by MarkResult - the imprecise cooldown this probe
// is meant to replace. MarkResult does not touch state.Quota for 402/403 (so
// Quota.Reason is empty); 401/404/model_not_supported also have an empty reason,
// so the discriminator is LastError.HTTPStatus.
//
// There are two cases:
//  1. Quota.Reason == "" and LastError is 402/403 — the common case, a fresh
//     generic fallback on a model that never had a Kimi probe cooldown.
//  2. Quota.Reason == kimiUsageReason but isKimiProbeOwnedCooldown is false and
//     LastError is 402/403 — stale Reason from an expired probe cooldown, now
//     overwritten by a fresh 402/403 fallback. The stale Reason prevents the
//     empty-Reason path from matching, so we match it explicitly here.
//
// Cloudflare challenges return HTTP 403 but set Quota.Reason = "cloudflare
// challenge", so they are excluded by both paths.
func isGenericPaymentFallback(state *ModelState) bool {
	if state == nil || state.LastError == nil {
		return false
	}
	s := state.LastError.HTTPStatus
	if s != http.StatusPaymentRequired && s != http.StatusForbidden {
		return false
	}
	// Case 1: empty Reason (the common MarkResult 402/403 path).
	if state.Quota.Reason == "" {
		return true
	}
	// Case 2: stale kimiUsageReason from an expired probe cooldown, now
	// overwritten by a fresh 402/403 fallback. The stale Reason would
	// otherwise hide this state from both hasKimiUsageCooldown and
	// hasGenericPaymentFallbackCooldown, leaving the model blocked even
	// after the probe confirms quota is available.
	if state.Quota.Reason == kimiUsageReason && !isKimiProbeOwnedCooldown(state) {
		return true
	}
	return false
}

// clearKimiUsageCooldown clears only the model-level cooldown states that were
// set by the Kimi usage probe (Quota.Reason == kimiUsageReason). It deliberately
// does NOT call ResetQuota, which would also clear cooldowns from other causes
// (Cloudflare challenges, generic 429 backoff, etc.).
func (m *Manager) clearKimiUsageCooldown(ctx context.Context, auth *Auth, now time.Time) error {
	if m == nil || auth == nil {
		return nil
	}

	var snapshot *Auth
	clearedModels := make([]string, 0)
	cooldownStateChanged := false

	m.mu.Lock()
	live, ok := m.auths[auth.ID]
	if !ok || live == nil {
		m.mu.Unlock()
		return nil
	}

	var cooldownRecordsBefore []CooldownStateRecord
	trackCooldownState := m.cooldownStore != nil
	if trackCooldownState {
		cooldownRecordsBefore = m.cooldownStateRecordsForAuthLocked(live, now)
	}

	for modelKey, state := range live.ModelStates {
		modelKey = strings.TrimSpace(modelKey)
		if modelKey == "" || state == nil {
			continue
		}
		// Only clear states still owned by this probe; skip states that began as a
		// Kimi cooldown but were since overwritten by a fresh non-quota failure.
		if !isKimiProbeOwnedCooldown(state) {
			continue
		}
		resetModelState(state, now)
		clearedModels = append(clearedModels, modelKey)
	}

	if len(clearedModels) > 0 {
		updateAggregatedAvailability(live, now)
		// Mirror ResetQuota: updateAggregatedAvailability only touches
		// Unavailable/NextRetryAfter/Quota, not Status/StatusMessage. If clearing
		// the Kimi cooldown left no remaining model error, restore the auth-level
		// status so management views and scheduler metadata no longer report a
		// recovered credential as errored.
		if !live.Disabled && live.Status != StatusDisabled && !hasModelError(live, now) {
			live.LastError = nil
			live.StatusMessage = ""
			live.Status = StatusActive
		}
	}
	_ = m.persist(ctx, live)
	snapshot = live.Clone()
	if trackCooldownState {
		after := m.cooldownStateRecordsForAuthLocked(live, now)
		cooldownStateChanged = !cooldownStateRecordsEqual(cooldownRecordsBefore, after)
	}
	m.mu.Unlock()

	for _, modelKey := range clearedModels {
		registry.GetGlobalRegistry().ClearModelQuotaExceeded(auth.ID, modelKey)
		registry.GetGlobalRegistry().ResumeClientModel(auth.ID, modelKey)
	}
	if m.scheduler != nil && snapshot != nil {
		m.scheduler.upsertAuth(snapshot)
	}
	if snapshot != nil && cooldownStateChanged {
		m.persistCooldownStates(context.Background())
	}
	return nil
}

// hasGenericPaymentFallbackCooldown reports whether any model state still carries
// a generic 402/403 payment_required fallback cooldown set by MarkResult. Unlike
// hasKimiUsageCooldown, this matches cooldowns with empty Quota.Reason (the
// MarkResult path for 402/403 does not set Quota) and LastError.HTTPStatus 402/403.
// The probe uses this to decide whether the healthy /v1/usages response should
// also clear these imprecise fallback cooldowns.
func hasGenericPaymentFallbackCooldown(auth *Auth) bool {
	if auth == nil {
		return false
	}
	for _, state := range auth.ModelStates {
		if isGenericPaymentFallback(state) {
			return true
		}
	}
	return false
}

// clearGenericPaymentFallbackCooldown clears only the model-level cooldown states
// that are generic 402/403 payment_required fallbacks (isGenericPaymentFallback).
// It follows the same lock/concurrency pattern as clearKimiUsageCooldown.
func (m *Manager) clearGenericPaymentFallbackCooldown(ctx context.Context, auth *Auth, now time.Time) error {
	if m == nil || auth == nil {
		return nil
	}

	var snapshot *Auth
	clearedModels := make([]string, 0)
	cooldownStateChanged := false

	m.mu.Lock()
	live, ok := m.auths[auth.ID]
	if !ok || live == nil {
		m.mu.Unlock()
		return nil
	}

	var cooldownRecordsBefore []CooldownStateRecord
	trackCooldownState := m.cooldownStore != nil
	if trackCooldownState {
		cooldownRecordsBefore = m.cooldownStateRecordsForAuthLocked(live, now)
	}

	for modelKey, state := range live.ModelStates {
		modelKey = strings.TrimSpace(modelKey)
		if modelKey == "" || state == nil {
			continue
		}
		if !isGenericPaymentFallback(state) {
			continue
		}
		resetModelState(state, now)
		clearedModels = append(clearedModels, modelKey)
	}

	if len(clearedModels) > 0 {
		updateAggregatedAvailability(live, now)
		if !live.Disabled && live.Status != StatusDisabled && !hasModelError(live, now) {
			live.LastError = nil
			live.StatusMessage = ""
			live.Status = StatusActive
		}
	}
	_ = m.persist(ctx, live)
	snapshot = live.Clone()
	if trackCooldownState {
		after := m.cooldownStateRecordsForAuthLocked(live, now)
		cooldownStateChanged = !cooldownStateRecordsEqual(cooldownRecordsBefore, after)
	}
	m.mu.Unlock()

	for _, modelKey := range clearedModels {
		registry.GetGlobalRegistry().ClearModelQuotaExceeded(auth.ID, modelKey)
		registry.GetGlobalRegistry().ResumeClientModel(auth.ID, modelKey)
	}
	if m.scheduler != nil && snapshot != nil {
		m.scheduler.upsertAuth(snapshot)
	}
	if snapshot != nil && cooldownStateChanged {
		m.persistCooldownStates(context.Background())
	}
	return nil
}
