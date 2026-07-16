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

func TestHasAuthQuotaExceeded_True(t *testing.T) {
	t.Parallel()
	future := time.Now().Add(time.Hour)
	auth := &Auth{
		ModelStates: map[string]*ModelState{
			"kimi-k2": {
				Quota:          QuotaState{Exceeded: true},
				NextRetryAfter: future,
			},
		},
	}
	if !hasAuthQuotaExceeded(auth, time.Now()) {
		t.Error("expected true when quota is exceeded and cooldown is active")
	}
}

func TestHasAuthQuotaExceeded_ExpiredIgnored(t *testing.T) {
	t.Parallel()
	past := time.Now().Add(-time.Hour)
	auth := &Auth{
		ModelStates: map[string]*ModelState{
			"kimi-k2": {
				Quota:          QuotaState{Exceeded: true},
				NextRetryAfter: past, // already expired → not active
			},
		},
	}
	if hasAuthQuotaExceeded(auth, time.Now()) {
		t.Error("expected false when cooldown has expired")
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

// TestSetAuthQuotaExceeded_PreservesNonQuotaSuspension verifies that a model
// already suspended for a non-quota reason (e.g. 404 model_not_supported, which
// has Quota.Exceeded=false) is NOT overwritten when the Kimi probe detects
// account-level quota exhaustion. Such structural suspensions must outlive the
// quota cooldown so the model is not prematurely re-enabled when quota recovers.
func TestSetAuthQuotaExceeded_PreservesNonQuotaSuspension(t *testing.T) {
	t.Parallel()
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "kimi-non-quota-auth"
	model := "kimi-k2"
	// Original non-quota suspension: 12h model_not_supported, Quota.Exceeded=false.
	longAfter := time.Now().Add(12 * time.Hour)

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
				StatusMessage:  "model_not_supported",
				Unavailable:    true,
				NextRetryAfter: longAfter,
				Quota:          QuotaState{Exceeded: false, Reason: "model_not_supported"},
				UpdatedAt:      time.Now(),
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// Probe says quota exhausted, recovering in 10 minutes (much sooner).
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
	// Non-quota suspension must be untouched.
	if state.Quota.Exceeded {
		t.Errorf("Quota.Exceeded = true, want false (non-quota suspension preserved)")
	}
	if state.Quota.Reason != "model_not_supported" {
		t.Errorf("Quota.Reason = %q, want model_not_supported (not overwritten)", state.Quota.Reason)
	}
	if !state.NextRetryAfter.Equal(longAfter) {
		t.Errorf("NextRetryAfter = %v, want %v (12h non-quota suspension preserved)", state.NextRetryAfter, longAfter)
	}
}

// TestSetAuthQuotaExceeded_OverwritesGenericQuotaCooldown verifies that a model
// with a quota-related cooldown from another cause (e.g. generic 403
// payment_required, Quota.Exceeded=true) IS replaced by the probe's precise
// upstream reset time when that time is sooner.
func TestSetAuthQuotaExceeded_OverwritesGenericQuotaCooldown(t *testing.T) {
	t.Parallel()
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "kimi-generic-quota-auth"
	model := "kimi-k2"
	// Generic 403 cooldown: 30 min, Quota.Exceeded=true (a quota-like fallback).
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
				StatusMessage:  "payment_required",
				Unavailable:    true,
				NextRetryAfter: genericAfter,
				Quota:          QuotaState{Exceeded: true, Reason: "payment_required", NextRecoverAt: genericAfter},
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
	// Should be overwritten with the probe's precise time + reason.
	if !state.Quota.Exceeded {
		t.Errorf("Quota.Exceeded = false, want true")
	}
	if state.Quota.Reason != kimiUsageReason {
		t.Errorf("Quota.Reason = %q, want %q", state.Quota.Reason, kimiUsageReason)
	}
	if !state.NextRetryAfter.Equal(recoverAt) {
		t.Errorf("NextRetryAfter = %v, want %v (probe precise reset)", state.NextRetryAfter, recoverAt)
	}
}
