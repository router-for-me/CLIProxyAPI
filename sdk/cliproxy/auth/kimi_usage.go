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
// 5-hour window details (possibly multiple), usage holds the weekly/cycle window.
type kimiUsageResponse struct {
	Limits []struct {
		Detail kimiUsageDetail `json:"detail"`
	} `json:"limits"`
	Usage kimiUsageDetail `json:"usage"`
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
		// 数值 >= 1e12 视为毫秒，否则秒。
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
// /v1/usages endpoint. It matches by base_url prefix, not by config provider type.
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

// fetchKimiUsage queries the /v1/usages endpoint for a Kimi auth. It reuses
// Manager.NewHttpRequest/HttpRequest for automatic Bearer injection and per-auth
// proxy routing (same path as inference requests). On failure it returns an error;
// the caller decides whether to log and skip. No cooldown is triggered here.
func (m *Manager) fetchKimiUsage(ctx context.Context, auth *Auth) ([]kimiUsageWindow, error) {
	if m == nil || auth == nil {
		return nil, fmt.Errorf("kimi usage: nil manager or auth")
	}
	baseURL := strings.TrimSpace(auth.Attributes["base_url"])
	if baseURL == "" {
		return nil, fmt.Errorf("kimi usage: empty base_url for auth %s", auth.ID)
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

	windows := make([]kimiUsageWindow, 0, len(parsed.Limits)+1)
	for _, lim := range parsed.Limits {
		windows = append(windows, windowFromDetail(lim.Detail, "five_hour"))
	}
	windows = append(windows, windowFromDetail(parsed.Usage, "weekly"))
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
	for _, w := range windows {
		if w.Limit <= 0 {
			continue
		}
		if w.Remaining > 0 {
			continue
		}
		if !w.HasReset {
			continue
		}
		ok = true
		if w.ResetAt.After(recoverAt) {
			recoverAt = w.ResetAt
		}
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
// concurrency pattern as MarkResult. Non-quota model suspensions (e.g. 404
// model_not_supported, 401 unauthorized) are always preserved. Among quota
// cooldowns, only same-reason (kimiUsageReason) ones are extended; quota
// cooldowns from other causes (e.g. generic 403 payment_required) are replaced
// with the probe's precise upstream reset time.
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
		// Preserve non-quota model suspensions (e.g. 404 model_not_supported,
		// 401 unauthorized). These have NextRetryAfter set but Quota.Exceeded
		// is false - they are structural issues, not quota exhaustion, and
		// the probe should not touch them.
		if !state.Quota.Exceeded && !state.NextRetryAfter.IsZero() {
			continue
		}
		// Only extend when the existing cooldown was set by this same probe
		// (kimiUsageReason) and is already longer. For quota cooldowns from
		// other causes (e.g. generic 403 payment_required from MarkResult),
		// the probe's precise upstream reset time takes precedence.
		if state.Quota.Reason == reason && !state.NextRetryAfter.IsZero() && state.NextRetryAfter.After(recoverAt) {
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

// hasAuthQuotaExceeded checks (on a snapshot or under lock) whether the auth
// currently has an active quota-exhausted cooldown. The probe uses this to avoid
// repeatedly clearing cooldowns on healthy accounts.
func hasAuthQuotaExceeded(auth *Auth, now time.Time) bool {
	if auth == nil {
		return false
	}
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		if state.Quota.Exceeded && !state.NextRetryAfter.IsZero() && state.NextRetryAfter.After(now) {
			return true
		}
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
		// Only clear states that were written by this probe.
		if state.Quota.Reason != kimiUsageReason {
			continue
		}
		resetModelState(state, now)
		clearedModels = append(clearedModels, modelKey)
	}

	if len(clearedModels) > 0 {
		updateAggregatedAvailability(live, now)
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
