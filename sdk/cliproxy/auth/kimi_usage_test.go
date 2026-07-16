package auth

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestParseKimiResetTime_ISO8601(t *testing.T) {
	t.Parallel()
	cases := []string{
		"2026-07-21T00:00:00Z",
		"2026-07-15T18:00:00+08:00",
		"2026-07-21T00:00:00.000Z",
	}
	for _, s := range cases {
		tm, ok := parseKimiResetTime(s)
		if !ok || tm.IsZero() {
			t.Errorf("parseKimiResetTime(%q) expected ok, got (zero=%v, ok=%v)", s, tm.IsZero(), ok)
		}
		if tm.Year() != 2026 {
			t.Errorf("parseKimiResetTime(%q) year=%d, want 2026", s, tm.Year())
		}
	}
}

func TestParseKimiResetTime_UnixSeconds(t *testing.T) {
	t.Parallel()
	tm, ok := parseKimiResetTime(float64(1753152000))
	if !ok {
		t.Fatal("expected ok for unix seconds timestamp")
	}
	if tm.IsZero() || tm.Year() != 2025 {
		t.Errorf("unexpected result: %v", tm)
	}
}

func TestParseKimiResetTime_UnixMilliseconds(t *testing.T) {
	t.Parallel()
	ms := float64(1753152000000)
	tm, ok := parseKimiResetTime(ms)
	if !ok {
		t.Fatal("expected ok for unix ms timestamp")
	}
	if tm.IsZero() || tm.Year() != 2025 {
		t.Errorf("unexpected result: %v", tm)
	}
}

func TestParseKimiResetTime_Invalid(t *testing.T) {
	t.Parallel()
	cases := []any{
		"not-a-date",
		"",
		float64(0),
		float64(-1),
		true,
		nil,
		int64(0),
		json.Number(""),
	}
	for _, v := range cases {
		_, ok := parseKimiResetTime(v)
		if ok {
			t.Errorf("parseKimiResetTime(%v) expected !ok", v)
		}
	}
}

func TestParseKimiResetTime_Int64Seconds(t *testing.T) {
	t.Parallel()
	tm, ok := parseKimiResetTime(int64(1753152000))
	if !ok || tm.IsZero() {
		t.Errorf("expected ok for int64 seconds, got ok=%v zero=%v", ok, tm.IsZero())
	}
}

func TestParseKimiResetTime_Int64Milliseconds(t *testing.T) {
	t.Parallel()
	tm, ok := parseKimiResetTime(int64(1753152000000))
	if !ok || tm.IsZero() {
		t.Errorf("expected ok for int64 ms, got ok=%v zero=%v", ok, tm.IsZero())
	}
}

func TestParseKimiResetTime_JsonNumber(t *testing.T) {
	t.Parallel()
	tm, ok := parseKimiResetTime(json.Number("1753152000"))
	if !ok || tm.IsZero() {
		t.Errorf("expected ok for json.Number seconds, got ok=%v", ok)
	}
}

func TestKimiUsageCooldown_SingleExhausted(t *testing.T) {
	t.Parallel()
	reset := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	windows := []kimiUsageWindow{
		{Name: "five_hour", Limit: 100, Remaining: 0, ResetAt: reset, HasReset: true},
		{Name: "weekly", Limit: 1000, Remaining: 500, ResetAt: time.Time{}, HasReset: false},
	}
	recoverAt, ok := kimiUsageCooldown(windows)
	if !ok {
		t.Fatal("expected ok with exhausted window")
	}
	if !recoverAt.Equal(reset) {
		t.Errorf("recoverAt=%v, want %v", recoverAt, reset)
	}
}

func TestKimiUsageCooldown_BothExhausted_ReturnsMax(t *testing.T) {
	t.Parallel()
	early := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	later := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	windows := []kimiUsageWindow{
		{Name: "five_hour", Limit: 100, Remaining: 0, ResetAt: early, HasReset: true},
		{Name: "weekly", Limit: 1000, Remaining: 0, ResetAt: later, HasReset: true},
	}
	recoverAt, ok := kimiUsageCooldown(windows)
	if !ok {
		t.Fatal("expected ok")
	}
	if !recoverAt.Equal(later) {
		t.Errorf("recoverAt=%v, want max=%v", recoverAt, later)
	}
}

func TestKimiUsageCooldown_ExhaustedNoReset_NotActionable(t *testing.T) {
	t.Parallel()
	windows := []kimiUsageWindow{
		{Name: "five_hour", Limit: 100, Remaining: 0, ResetAt: time.Time{}, HasReset: false},
	}
	_, ok := kimiUsageCooldown(windows)
	if ok {
		t.Error("expected !ok when exhausted but no reset info")
	}
}

func TestKimiUsageCooldown_AllAvailable(t *testing.T) {
	t.Parallel()
	windows := []kimiUsageWindow{
		{Name: "five_hour", Limit: 100, Remaining: 50, HasReset: true},
		{Name: "weekly", Limit: 1000, Remaining: 800, HasReset: true},
	}
	_, ok := kimiUsageCooldown(windows)
	if ok {
		t.Error("expected !ok when all remaining>0")
	}
}

func TestKimiUsageCooldown_ZeroLimitIgnored(t *testing.T) {
	t.Parallel()
	// A window with limit<=0 should be skipped; it should not trigger a cooldown
	// even if remaining is 0.
	windows := []kimiUsageWindow{
		{Name: "five_hour", Limit: 0, Remaining: 0, ResetAt: time.Now(), HasReset: true},
		{Name: "weekly", Limit: 1000, Remaining: 500, ResetAt: time.Now(), HasReset: true},
	}
	_, ok := kimiUsageCooldown(windows)
	if ok {
		t.Error("expected !ok when only zero-limit window is exhausted")
	}
}

func TestKimiUsageFullyAvailable_AllPositive(t *testing.T) {
	t.Parallel()
	windows := []kimiUsageWindow{
		{Name: "five_hour", Limit: 100, Remaining: 50},
		{Name: "weekly", Limit: 1000, Remaining: 800},
	}
	if !kimiUsageFullyAvailable(windows) {
		t.Error("expected fully available")
	}
}

func TestKimiUsageFullyAvailable_PartialExhausted(t *testing.T) {
	t.Parallel()
	windows := []kimiUsageWindow{
		{Name: "five_hour", Limit: 100, Remaining: 0},
		{Name: "weekly", Limit: 1000, Remaining: 800},
	}
	if kimiUsageFullyAvailable(windows) {
		t.Error("expected not fully available when 5h is 0")
	}
}

func TestKimiUsageFullyAvailable_EmptyList(t *testing.T) {
	t.Parallel()
	if kimiUsageFullyAvailable(nil) {
		t.Error("expected false for empty window list (parse anomaly)")
	}
}

func TestKimiUsageFullyAvailable_ZeroLimitIgnored(t *testing.T) {
	t.Parallel()
	// A window with limit=0 means the upstream didn't report it; should be ignored.
	windows := []kimiUsageWindow{
		{Name: "five_hour", Limit: 0, Remaining: 0}, // irrelevant
		{Name: "weekly", Limit: 1000, Remaining: 500},
	}
	if !kimiUsageFullyAvailable(windows) {
		t.Error("expected available when zero-limit window is ignored")
	}
}

func TestKimiUsageFullyAvailable_AllZeroLimit_NoValidWindows(t *testing.T) {
	t.Parallel()
	// When all windows have limit<=0, there are no valid windows to observe;
	// should return false to avoid clearing cooldown on empty data.
	windows := []kimiUsageWindow{
		{Name: "five_hour", Limit: 0, Remaining: 0},
		{Name: "weekly", Limit: 0, Remaining: 0},
	}
	if kimiUsageFullyAvailable(windows) {
		t.Error("expected false when all windows have zero limit (no valid data)")
	}
}

func TestIsKimiUsageAuth_Valid_ClaudeKey(t *testing.T) {
	t.Parallel()
	// Kimi auth configured via claude_key, Provider is "claude"
	auth := &Auth{
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "sk-test",
			"base_url": "https://api.kimi.com/coding",
		},
	}
	if !isKimiUsageAuth(auth) {
		t.Error("claude_key with kimi base_url should match")
	}
}

func TestIsKimiUsageAuth_Valid_OpenAICompat(t *testing.T) {
	t.Parallel()
	// Kimi auth configured via openai-compatibility, Provider is "openai-compatible-kimi"
	auth := &Auth{
		Provider: "openai-compatible-kimi",
		Attributes: map[string]string{
			"api_key":  "sk-test",
			"base_url": "https://api.kimi.com/coding",
		},
	}
	if !isKimiUsageAuth(auth) {
		t.Error("openai-compatibility with kimi base_url should match")
	}
}

func TestIsKimiUsageAuth_BaseURLWithPath(t *testing.T) {
	t.Parallel()
	// base_url with trailing path should also match
	auth := &Auth{
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "sk-test",
			"base_url": "https://api.kimi.com/coding/v1",
		},
	}
	if !isKimiUsageAuth(auth) {
		t.Error("base_url with trailing path should match by prefix")
	}
}

func TestIsKimiUsageAuth_Nil(t *testing.T) {
	t.Parallel()
	if isKimiUsageAuth(nil) {
		t.Error("nil should not match")
	}
}

func TestIsKimiUsageAuth_MissingApiKey(t *testing.T) {
	t.Parallel()
	auth := &Auth{
		Provider: "claude",
		Attributes: map[string]string{
			"base_url": "https://api.kimi.com/coding",
		},
	}
	if isKimiUsageAuth(auth) {
		t.Error("should reject when api_key is missing")
	}
}

func TestIsKimiUsageAuth_WrongBaseURL(t *testing.T) {
	t.Parallel()
	auth := &Auth{
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "sk-test",
			"base_url": "https://api.anthropic.com",
		},
	}
	if isKimiUsageAuth(auth) {
		t.Error("should reject non-kimi base_url")
	}
}

func TestIsKimiUsageAuth_BaseURLPrefixFalsePositive(t *testing.T) {
	t.Parallel()
	// A base_url like https://api.kimi.com/coding-fake must NOT match
	// https://api.kimi.com/coding (strict prefix with "/" boundary check).
	auth := &Auth{
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "sk-test",
			"base_url": "https://api.kimi.com/coding-fake",
		},
	}
	if isKimiUsageAuth(auth) {
		t.Error("should reject base_url that only shares a prefix (coding-fake)")
	}
}

func TestIsKimiUsageAuth_DisabledAuth(t *testing.T) {
	t.Parallel()
	// A disabled auth must not be matched, even if it has a valid Kimi base_url
	// and api_key. The operator took it out of service; the probe should not
	// send background /v1/usages traffic to it.
	auth := &Auth{
		Provider: "claude",
		Disabled: true,
		Attributes: map[string]string{
			"api_key":  "sk-test",
			"base_url": "https://api.kimi.com/coding",
		},
	}
	if isKimiUsageAuth(auth) {
		t.Error("should reject disabled auth")
	}
}

func TestIsKimiUsageAuth_StatusDisabled(t *testing.T) {
	t.Parallel()
	auth := &Auth{
		Provider: "claude",
		Status:   StatusDisabled,
		Attributes: map[string]string{
			"api_key":  "sk-test",
			"base_url": "https://api.kimi.com/coding",
		},
	}
	if isKimiUsageAuth(auth) {
		t.Error("should reject auth with StatusDisabled")
	}
}

func TestWindowFromDetail_Normal(t *testing.T) {
	t.Parallel()
	d := kimiUsageDetail{
		Limit:     json.Number("100"),
		Remaining: json.Number("50"),
		ResetTime: "2026-07-15T18:00:00Z",
	}
	w := windowFromDetail(d, "five_hour")
	if w.Limit != 100 || w.Remaining != 50 {
		t.Errorf("limit=%v remaining=%v want 100/50", w.Limit, w.Remaining)
	}
	if !w.HasReset || w.ResetAt.IsZero() {
		t.Error("expected HasReset with valid resetTime")
	}
	if w.Name != "five_hour" {
		t.Errorf("name=%s want five_hour", w.Name)
	}
}

func TestHasKimiUsageCooldown_ActiveCooldown(t *testing.T) {
	t.Parallel()
	future := time.Now().Add(time.Hour)
	auth := &Auth{
		ModelStates: map[string]*ModelState{
			"kimi-k2": {
				NextRetryAfter: future,
				Quota:          QuotaState{Exceeded: true, Reason: kimiUsageReason, NextRecoverAt: future},
			},
		},
	}
	if !hasKimiUsageCooldown(auth) {
		t.Error("expected true when a Kimi-probe cooldown is present")
	}
}

func TestHasKimiUsageCooldown_ExpiredStillMatched(t *testing.T) {
	t.Parallel()
	// A Kimi-probe cooldown whose NextRetryAfter has already passed must still be
	// reported so the probe clears it (resumes the registry-suspended model and
	// restores auth status) after the reset time.
	past := time.Now().Add(-time.Hour)
	auth := &Auth{
		ModelStates: map[string]*ModelState{
			"kimi-k2": {
				Quota: QuotaState{Exceeded: true, Reason: kimiUsageReason, NextRecoverAt: past},
			},
		},
	}
	if !hasKimiUsageCooldown(auth) {
		t.Error("expected true for expired Kimi-probe cooldown (still needs clearing)")
	}
}

func TestHasKimiUsageCooldown_OtherReasonIgnored(t *testing.T) {
	t.Parallel()
	auth := &Auth{
		ModelStates: map[string]*ModelState{
			"kimi-k2": {
				Quota: QuotaState{Exceeded: true, Reason: "payment_required"},
			},
		},
	}
	if hasKimiUsageCooldown(auth) {
		t.Error("expected false when cooldown reason is not kimiUsageReason")
	}
}

func TestIsKimiProbeOwnedCooldown(t *testing.T) {
	t.Parallel()
	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)
	fortyOne := time.Now().Add(30 * time.Minute) // a fresh 401 cooldown time
	cases := []struct {
		name  string
		state *ModelState
		want  bool
	}{
		{
			name:  "nil",
			state: nil,
			want:  false,
		},
		{
			name: "non-kimi reason",
			state: &ModelState{
				NextRetryAfter: future,
				Quota:          QuotaState{Exceeded: true, Reason: "cloudflare challenge", NextRecoverAt: future},
			},
			want: false,
		},
		{
			name: "active kimi (NextRetryAfter equals NextRecoverAt)",
			state: &ModelState{
				NextRetryAfter: future,
				Quota:          QuotaState{Exceeded: true, Reason: kimiUsageReason, NextRecoverAt: future},
			},
			want: true,
		},
		{
			name: "expired kimi lazily cleared (NextRetryAfter zeroed)",
			state: &ModelState{
				NextRetryAfter: time.Time{},
				Quota:          QuotaState{Exceeded: true, Reason: kimiUsageReason, NextRecoverAt: past},
			},
			want: true,
		},
		{
			name: "expired kimi not yet aggregated (NextRetryAfter equals past NextRecoverAt)",
			state: &ModelState{
				NextRetryAfter: past,
				Quota:          QuotaState{Exceeded: true, Reason: kimiUsageReason, NextRecoverAt: past},
			},
			want: true,
		},
		{
			name: "kimi cooldown overwritten by fresh 401 (NextRetryAfter diverges)",
			state: &ModelState{
				NextRetryAfter: fortyOne,
				Quota:          QuotaState{Exceeded: true, Reason: kimiUsageReason, NextRecoverAt: past},
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isKimiProbeOwnedCooldown(tc.state); got != tc.want {
				t.Errorf("isKimiProbeOwnedCooldown(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestKimiUsageCooldown_ShorterThanGeneric403 verifies that when a model
// already has a cooldown from a different cause (e.g. generic 403
// payment_required, Quota.Reason != kimiUsageReason), the probe's precise
// upstream reset time is allowed to shorten it. This is tested indirectly
// via the cooldown calculation: the probe always reports the real reset
// time, and SetAuthQuotaExceeded only skips when the existing cooldown
// shares the same reason AND is already longer.
func TestKimiUsageCooldown_ShorterThanGeneric403(t *testing.T) {
	t.Parallel()
	// Simulate: the probe sees a real reset time in 10 minutes.
	// Even if a generic 403 cooldown is set to 30 minutes, the probe's
	// shorter reset time should take effect because the reasons differ.
	// This test validates the cooldown calculation itself produces the
	// correct recoverAt, which is the precise reset time from upstream.
	probeReset := time.Date(2026, 7, 15, 18, 10, 0, 0, time.UTC)
	windows := []kimiUsageWindow{
		{Name: "five_hour", Limit: 100, Remaining: 0, ResetAt: probeReset, HasReset: true},
	}
	recoverAt, ok := kimiUsageCooldown(windows)
	if !ok {
		t.Fatal("expected ok")
	}
	if !recoverAt.Equal(probeReset) {
		t.Errorf("recoverAt=%v, want %v (precise upstream reset)", recoverAt, probeReset)
	}
}

// TestSetAuthQuotaExceeded_PreservesCloudflareBackoff verifies that a model
// under a Cloudflare challenge backoff (Quota.Exceeded=true, Reason="cloudflare
// challenge" as set by MarkResult) is NOT overwritten by the Kimi probe. A
// Cloudflare/edge block is unrelated to Kimi quota, so overwriting it and later
// clearing it would let requests through before the backoff elapses.
func TestSetAuthQuotaExceeded_PreservesCloudflareBackoff(t *testing.T) {
	t.Parallel()
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "kimi-cf-auth"
	model := "kimi-k2"
	// Realistic Cloudflare state from MarkResult: Exceeded=true, explicit reason.
	cfAfter := time.Now().Add(5 * time.Minute)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "openai-compatible-kimi", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	if _, err := manager.Register(ctx, &Auth{
		ID:       authID,
		Provider: "openai-compatible-kimi",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				StatusMessage:  "cloudflare challenge",
				Unavailable:    true,
				NextRetryAfter: cfAfter,
				Quota:          QuotaState{Exceeded: true, Reason: "cloudflare challenge", NextRecoverAt: cfAfter},
				UpdatedAt:      time.Now(),
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// Probe says quota exhausted, recovering in 10 minutes.
	recoverAt := time.Now().Add(10 * time.Minute)
	snapshot, err := manager.SetAuthQuotaExceeded(ctx, authID, recoverAt, kimiUsageReason)
	if err != nil {
		t.Fatalf("SetAuthQuotaExceeded() error = %v", err)
	}
	if snapshot == nil {
		t.Fatal("SetAuthQuotaExceeded() returned nil snapshot")
	}

	state := snapshot.ModelStates[model]
	if state == nil {
		t.Fatal("model state missing after SetAuthQuotaExceeded")
	}
	// Cloudflare backoff must be untouched.
	if state.Quota.Reason != "cloudflare challenge" {
		t.Errorf("Quota.Reason = %q, want cloudflare challenge (preserved)", state.Quota.Reason)
	}
	if !state.NextRetryAfter.Equal(cfAfter) {
		t.Errorf("NextRetryAfter = %v, want %v (Cloudflare backoff preserved)", state.NextRetryAfter, cfAfter)
	}
}

// TestSetAuthQuotaExceeded_OverwritesGeneric403Fallback verifies that a model
// under the generic 402/403 payment_required fallback IS replaced by the probe's
// precise upstream reset time. This is the probe's core purpose. MarkResult does
// NOT set Quota for the 402/403 fallback (it leaves Exceeded=false, Reason=""),
// so the overwrite discriminator is the empty reason, not Quota.Exceeded.
func TestSetAuthQuotaExceeded_OverwritesGeneric403Fallback(t *testing.T) {
	t.Parallel()
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "kimi-generic-403-auth"
	model := "kimi-k2"
	// Realistic generic-403 state from MarkResult: 30 min cooldown, Quota LEFT
	// UNTOUCHED (Exceeded=false, Reason=""), StatusMessage = upstream message.
	genericAfter := time.Now().Add(30 * time.Minute)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "openai-compatible-kimi", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	if _, err := manager.Register(ctx, &Auth{
		ID:       authID,
		Provider: "openai-compatible-kimi",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				StatusMessage:  "quota exhausted, refreshed in the next cycle",
				Unavailable:    true,
				NextRetryAfter: genericAfter,
				Quota:          QuotaState{}, // untouched by MarkResult for 402/403
				UpdatedAt:      time.Now(),
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// Probe's precise reset time is sooner (10 min).
	recoverAt := time.Now().Add(10 * time.Minute)
	snapshot, err := manager.SetAuthQuotaExceeded(ctx, authID, recoverAt, kimiUsageReason)
	if err != nil {
		t.Fatalf("SetAuthQuotaExceeded() error = %v", err)
	}
	if snapshot == nil {
		t.Fatal("SetAuthQuotaExceeded() returned nil snapshot")
	}

	state := snapshot.ModelStates[model]
	if state == nil {
		t.Fatal("model state missing after SetAuthQuotaExceeded")
	}
	// Should be overwritten with the probe's precise time + Kimi reason.
	if state.Quota.Reason != kimiUsageReason {
		t.Errorf("Quota.Reason = %q, want %q", state.Quota.Reason, kimiUsageReason)
	}
	if !state.NextRetryAfter.Equal(recoverAt) {
		t.Errorf("NextRetryAfter = %v, want %v (probe precise reset)", state.NextRetryAfter, recoverAt)
	}
}

// TestClearKimiUsageCooldown_RestoresAuthStatus verifies that clearing a Kimi
// probe cooldown also restores the auth-level Status to active (mirrors
// ResetQuota). SetAuthQuotaExceeded sets auth.Status = StatusError; after the
// probe clears the cooldown and no model error remains, the status must be
// StatusActive again so management views and scheduler metadata do not report a
// recovered credential as errored.
func TestClearKimiUsageCooldown_RestoresAuthStatus(t *testing.T) {
	t.Parallel()
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "kimi-clear-auth"
	model := "kimi-k2"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "openai-compatible-kimi", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	if _, err := manager.Register(ctx, &Auth{
		ID:       authID,
		Provider: "openai-compatible-kimi",
		Status:   StatusActive,
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// Probe sets a Kimi quota cooldown (this also flips auth.Status to StatusError).
	recoverAt := time.Now().Add(time.Hour)
	if _, err := manager.SetAuthQuotaExceeded(ctx, authID, recoverAt, kimiUsageReason); err != nil {
		t.Fatalf("SetAuthQuotaExceeded() error = %v", err)
	}

	// Confirm the auth is now errored at the aggregated level.
	live := manager.snapshotAuths()
	var before *Auth
	for i := range live {
		if live[i].ID == authID {
			before = live[i]
			break
		}
	}
	if before == nil {
		t.Fatal("auth not found in snapshot")
	}
	if before.Status != StatusError {
		t.Fatalf("pre-clear Status = %v, want StatusError", before.Status)
	}

	// Clear the Kimi cooldown (simulating the probe seeing recovered quota).
	now := time.Now()
	if err := manager.clearKimiUsageCooldown(ctx, before, now); err != nil {
		t.Fatalf("clearKimiUsageCooldown() error = %v", err)
	}

	// Re-snapshot and assert the auth-level status is restored to active.
	live = manager.snapshotAuths()
	var after *Auth
	for i := range live {
		if live[i].ID == authID {
			after = live[i]
			break
		}
	}
	if after == nil {
		t.Fatal("auth not found in snapshot after clear")
	}
	if after.Status != StatusActive {
		t.Errorf("post-clear Status = %v, want StatusActive", after.Status)
	}
	if after.StatusMessage != "" {
		t.Errorf("post-clear StatusMessage = %q, want empty", after.StatusMessage)
	}
}

// TestClearKimiUsageCooldown_PreservesOverwrittenByNonQuotaFailure verifies that a
// state which began as a Kimi cooldown but was overwritten by a fresh non-quota
// failure is NOT cleared. Scenario: Kimi cooldown expired, request was let
// through (lazy expiry), then hit a 401 - MarkResult updates NextRetryAfter and
// StatusMessage but leaves the stale Quota.Reason == kimiUsageReason. The probe
// must not treat that as a Kimi cooldown and resume the model prematurely.
func TestClearKimiUsageCooldown_PreservesOverwrittenByNonQuotaFailure(t *testing.T) {
	t.Parallel()
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "kimi-overwritten-auth"
	model := "kimi-k2"

	freshRetry := time.Now().Add(30 * time.Minute) // a fresh 401 cooldown
	pastRecover := time.Now().Add(-time.Hour)      // the expired Kimi recover time

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "openai-compatible-kimi", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	if _, err := manager.Register(ctx, &Auth{
		ID:       authID,
		Provider: "openai-compatible-kimi",
		Status:   StatusError,
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				StatusMessage:  "unauthorized",
				Unavailable:    true,
				NextRetryAfter: freshRetry, // overwritten by the fresh 401
				Quota: QuotaState{ // stale Kimi probe fields, untouched by MarkResult
					Exceeded:      true,
					Reason:        kimiUsageReason,
					NextRecoverAt: pastRecover,
				},
				UpdatedAt: time.Now(),
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	live := manager.snapshotAuths()
	var target *Auth
	for i := range live {
		if live[i].ID == authID {
			target = live[i]
			break
		}
	}
	if target == nil {
		t.Fatal("auth not found in snapshot")
	}

	if err := manager.clearKimiUsageCooldown(ctx, target, time.Now()); err != nil {
		t.Fatalf("clearKimiUsageCooldown() error = %v", err)
	}

	// Re-snapshot: the overwritten model must still be cooling down for the 401.
	live = manager.snapshotAuths()
	var after *Auth
	for i := range live {
		if live[i].ID == authID {
			after = live[i]
			break
		}
	}
	if after == nil {
		t.Fatal("auth not found in snapshot after clear")
	}
	state := after.ModelStates[model]
	if state == nil {
		t.Fatal("model state missing after clear")
	}
	if !state.NextRetryAfter.Equal(freshRetry) {
		t.Errorf("NextRetryAfter = %v, want %v (401 cooldown must not be cleared)", state.NextRetryAfter, freshRetry)
	}
	if state.Status != StatusError {
		t.Errorf("Status = %v, want StatusError (401 cooldown preserved)", state.Status)
	}
}
