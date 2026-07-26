package auth

import (
	"context"
	"encoding/json"
	"net/http"
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

func TestKimiUsageCooldown_ExhaustedMixedReset_Declines(t *testing.T) {
	t.Parallel()
	// One exhausted window has a resetTime, another is exhausted but lacks one.
	// The account is usable only after ALL windows recover, so the probe must not
	// promise recovery at the known window's reset while the other may still be
	// exhausted; it declines (ok=false) and the caller falls back to the generic
	// backoff.
	fiveHourReset := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	windows := []kimiUsageWindow{
		{Name: "five_hour", Limit: 100, Remaining: 0, ResetAt: fiveHourReset, HasReset: true},
		{Name: "weekly", Limit: 1000, Remaining: 0, ResetAt: time.Time{}, HasReset: false},
	}
	recoverAt, ok := kimiUsageCooldown(windows)
	if ok {
		t.Error("expected !ok when an exhausted window lacks a resetTime")
	}
	if !recoverAt.IsZero() {
		t.Errorf("recoverAt = %v, want zero (declined)", recoverAt)
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

// Native Kimi OAuth auths (Provider == "kimi") carry the bearer token in
// Metadata and have no api_key/base_url attributes. The probe must cover them
// too — they are the primary Kimi login path.

func TestIsKimiUsageAuth_NativeOAuth_MetadataToken(t *testing.T) {
	t.Parallel()
	// Native OAuth record as produced by sdk/auth/kimi.go: Provider "kimi",
	// bearer token in Metadata, no api_key/base_url attributes.
	auth := &Auth{
		Provider: "kimi",
		Metadata: map[string]any{
			"access_token": "oauth-bearer-token",
		},
	}
	if !isKimiUsageAuth(auth) {
		t.Error("native kimi OAuth auth with Metadata access_token should match")
	}
}

func TestIsKimiUsageAuth_NativeOAuth_NoToken(t *testing.T) {
	t.Parallel()
	// Provider "kimi" but no usable token anywhere → nothing to authenticate
	// the /v1/usages call with, so skip.
	auth := &Auth{
		Provider: "kimi",
		// No Metadata, no Attributes.
	}
	if isKimiUsageAuth(auth) {
		t.Error("native kimi auth without any token should be rejected")
	}
}

func TestIsKimiUsageAuth_NativeOAuth_Disabled(t *testing.T) {
	t.Parallel()
	auth := &Auth{
		Provider: "kimi",
		Disabled: true,
		Metadata: map[string]any{
			"access_token": "oauth-bearer-token",
		},
	}
	if isKimiUsageAuth(auth) {
		t.Error("disabled native kimi OAuth auth should be rejected")
	}
}

func TestHasKimiBearerToken_Table(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		auth *Auth
		want bool
	}{
		{
			name: "metadata access_token",
			auth: &Auth{Metadata: map[string]any{"access_token": "tok"}},
			want: true,
		},
		{
			name: "attributes access_token",
			auth: &Auth{Attributes: map[string]string{"access_token": "tok"}},
			want: true,
		},
		{
			name: "attributes api_key",
			auth: &Auth{Attributes: map[string]string{"api_key": "tok"}},
			want: true,
		},
		{
			name: "metadata empty string ignored",
			auth: &Auth{Metadata: map[string]any{"access_token": "  "}},
			want: false,
		},
		{
			name: "metadata non-string ignored",
			auth: &Auth{Metadata: map[string]any{"access_token": 123}},
			want: false,
		},
		{
			name: "nil auth",
			auth: nil,
			want: false,
		},
		{
			name: "no token anywhere",
			auth: &Auth{Provider: "kimi"},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := hasKimiBearerToken(tc.auth); got != tc.want {
				t.Errorf("hasKimiBearerToken(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
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

// TestSetAuthQuotaExceeded_ExtendsShorterNonKimiCooldown verifies that when a
// model already has a non-Kimi cooldown (e.g. Cloudflare challenge) that ends
// before the probe's recoverAt, the cooldown is extended to recoverAt so the
// model is not unblocked while the account-level Kimi quota is still exhausted.
func TestSetAuthQuotaExceeded_ExtendsShorterNonKimiCooldown(t *testing.T) {
	t.Parallel()
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "kimi-cf-short-auth"
	model := "kimi-k2"
	// Cloudflare backoff ending in 5 minutes.
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

	// Probe says quota exhausted, recovering in 10 minutes (longer than CF backoff).
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
	// The shorter Cloudflare cooldown must be extended to the Kimi recoverAt
	// so the model stays blocked until the account-level quota recovers.
	if !state.NextRetryAfter.Equal(recoverAt) {
		t.Errorf("NextRetryAfter = %v, want %v (extended to Kimi recoverAt)", state.NextRetryAfter, recoverAt)
	}
	// Extension replaces the reason with Kimi reason so the probe can clear it later.
	if state.Quota.Reason != kimiUsageReason {
		t.Errorf("Quota.Reason = %q, want %q (extended to Kimi reason)", state.Quota.Reason, kimiUsageReason)
	}
}

// TestSetAuthQuotaExceeded_OverwritesGeneric403Fallback verifies that a model
// under the generic 402/403 payment_required fallback IS replaced by the probe's
// precise upstream reset time. This is the probe's core purpose. MarkResult does
// NOT set Quota for the 402/403 fallback (it leaves Reason=""), storing the status
// only in LastError.HTTPStatus, so that status is the overwrite discriminator.
func TestSetAuthQuotaExceeded_OverwritesGeneric403Fallback(t *testing.T) {
	t.Parallel()
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "kimi-generic-403-auth"
	model := "kimi-k2"
	// Realistic generic-403 state from MarkResult: 30 min cooldown, Quota LEFT
	// UNTOUCHED (Reason=""), status carried only by LastError.HTTPStatus.
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
				LastError:      &Error{HTTPStatus: http.StatusForbidden, Message: "quota exhausted, refreshed in the next cycle"},
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

// TestSetAuthQuotaExceeded_PreservesUnauthorized401 verifies that a model under a
// fresh 401 unauthorized cooldown is NOT overwritten, even though it carries no
// Quota.Reason (just like the 402/403 fallback). The discriminator is
// LastError.HTTPStatus: 401 stays, 402/403 gets replaced.
func TestSetAuthQuotaExceeded_PreservesUnauthorized401(t *testing.T) {
	t.Parallel()
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "kimi-401-auth"
	model := "kimi-k2"
	unauthAfter := time.Now().Add(30 * time.Minute)

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
				StatusMessage:  "unauthorized",
				Unavailable:    true,
				NextRetryAfter: unauthAfter,
				Quota:          QuotaState{}, // untouched by MarkResult for 401
				LastError:      &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
				UpdatedAt:      time.Now(),
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

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
	// 401 cooldown must be preserved (not reclassified as Kimi).
	if state.Quota.Reason != "" {
		t.Errorf("Quota.Reason = %q, want empty (401 preserved, not overwritten)", state.Quota.Reason)
	}
	if !state.NextRetryAfter.Equal(unauthAfter) {
		t.Errorf("NextRetryAfter = %v, want %v (401 cooldown preserved)", state.NextRetryAfter, unauthAfter)
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

// TestSetAuthQuotaExceeded_PreservesLongerNonKimiCooldown verifies that when a
// model has a non-Kimi cooldown that already lasts longer than the probe's
// recoverAt, it is preserved (not shortened to the Kimi reset time). This
// avoids replacing a longer backoff with a shorter one.
func TestSetAuthQuotaExceeded_PreservesLongerNonKimiCooldown(t *testing.T) {
	t.Parallel()
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "kimi-cf-long-auth"
	model := "kimi-k2"
	// Cloudflare backoff ending in 30 minutes (longer than probe's 10 min).
	cfAfter := time.Now().Add(30 * time.Minute)

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

	// Probe says quota exhausted, recovering in 10 minutes (shorter than CF backoff).
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
	// The longer Cloudflare cooldown must be preserved.
	if state.Quota.Reason != "cloudflare challenge" {
		t.Errorf("Quota.Reason = %q, want cloudflare challenge (preserved)", state.Quota.Reason)
	}
	if !state.NextRetryAfter.Equal(cfAfter) {
		t.Errorf("NextRetryAfter = %v, want %v (preserved longer CF backoff)", state.NextRetryAfter, cfAfter)
	}
}

// TestHasGenericPaymentFallbackCooldown verifies detection of the generic
// 402/403 payment_required fallback state set by MarkResult.
func TestHasGenericPaymentFallbackCooldown(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		auth *Auth
		want bool
	}{
		{
			name: "nil auth",
			auth: nil,
			want: false,
		},
		{
			name: "empty model states",
			auth: &Auth{ModelStates: map[string]*ModelState{}},
			want: false,
		},
		{
			name: "403 payment_required fallback",
			auth: &Auth{
				ModelStates: map[string]*ModelState{
					"kimi-k2": {
						NextRetryAfter: time.Now().Add(30 * time.Minute),
						Quota:          QuotaState{}, // empty Reason as MarkResult leaves it
						LastError:      &Error{HTTPStatus: http.StatusForbidden},
					},
				},
			},
			want: true,
		},
		{
			name: "402 payment_required fallback",
			auth: &Auth{
				ModelStates: map[string]*ModelState{
					"kimi-k2": {
						NextRetryAfter: time.Now().Add(30 * time.Minute),
						Quota:          QuotaState{},
						LastError:      &Error{HTTPStatus: http.StatusPaymentRequired},
					},
				},
			},
			want: true,
		},
		{
			name: "401 unauthorized ignored",
			auth: &Auth{
				ModelStates: map[string]*ModelState{
					"kimi-k2": {
						NextRetryAfter: time.Now().Add(30 * time.Minute),
						Quota:          QuotaState{},
						LastError:      &Error{HTTPStatus: http.StatusUnauthorized},
					},
				},
			},
			want: false,
		},
		{
			name: "kimi-probe cooldown with Reason set",
			auth: &Auth{
				ModelStates: map[string]*ModelState{
					"kimi-k2": {
						NextRetryAfter: time.Now().Add(time.Hour),
						Quota:          QuotaState{Exceeded: true, Reason: kimiUsageReason},
					},
				},
			},
			want: false,
		},
		{
			name: "nil LastError",
			auth: &Auth{
				ModelStates: map[string]*ModelState{
					"kimi-k2": {
						NextRetryAfter: time.Now().Add(30 * time.Minute),
						Quota:          QuotaState{},
						LastError:      nil,
					},
				},
			},
			want: false,
		},
		{
			name: "Cloudflare 403 not matched (Reason set)",
			auth: &Auth{
				ModelStates: map[string]*ModelState{
					"kimi-k2": {
						NextRetryAfter: time.Now().Add(30 * time.Minute),
						Quota:          QuotaState{Exceeded: true, Reason: "cloudflare challenge"},
						LastError:      &Error{HTTPStatus: http.StatusForbidden},
					},
				},
			},
			want: false,
		},
		{
			name: "stale kimiUsageReason + fresh 403 fallback (matched)",
			auth: &Auth{
				ModelStates: map[string]*ModelState{
					"kimi-k2": {
						NextRetryAfter: time.Now().Add(30 * time.Minute),
						Quota:          QuotaState{Exceeded: true, Reason: kimiUsageReason, NextRecoverAt: time.Now().Add(-time.Hour)},
						LastError:      &Error{HTTPStatus: http.StatusForbidden},
					},
				},
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := hasGenericPaymentFallbackCooldown(tc.auth); got != tc.want {
				t.Errorf("hasGenericPaymentFallbackCooldown(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestClearGenericPaymentFallbackCooldown verifies that clearing the generic
// 402/403 fallback restores the auth-level status and clears model-level state.
func TestClearGenericPaymentFallbackCooldown(t *testing.T) {
	t.Parallel()
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "kimi-clear-generic-auth"
	model := "kimi-k2"

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
				StatusMessage:  "quota exhausted, refreshed in the next cycle",
				Unavailable:    true,
				NextRetryAfter: time.Now().Add(30 * time.Minute),
				Quota:          QuotaState{}, // generic 403 fallback
				LastError:      &Error{HTTPStatus: http.StatusForbidden, Message: "quota exhausted"},
				UpdatedAt:      time.Now(),
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

	now := time.Now()
	if err := manager.clearGenericPaymentFallbackCooldown(ctx, target, now); err != nil {
		t.Fatalf("clearGenericPaymentFallbackCooldown() error = %v", err)
	}

	// Verify the auth status is restored.
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

	// Verify the model state is reset.
	state := after.ModelStates[model]
	if state == nil {
		t.Fatal("model state missing after clear")
	}
	if state.Unavailable {
		t.Error("expected Unavailable=false after clear")
	}
	if !state.NextRetryAfter.IsZero() {
		t.Errorf("NextRetryAfter = %v, want zero", state.NextRetryAfter)
	}
}

// TestClearGenericPaymentFallbackCooldown_Preserves401AndCloudflareCooldown verifies that
// a generic 401 unauthorized cooldown and a Cloudflare 403 challenge (Quota.Reason set)
// are NOT cleared by the generic fallback clearing path — only 402/403 with empty Reason
// states are targeted.
func TestClearGenericPaymentFallbackCooldown_Preserves401AndCloudflareCooldown(t *testing.T) {
	t.Parallel()
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "kimi-clear-mixed-auth"
	model403 := "kimi-k2"      // 403 fallback — should be cleared
	model401 := "kimi-k2-401"  // 401 unauthorized — must be preserved
	modelCF := "kimi-k2-cf"    // Cloudflare 403 — must be preserved

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "openai-compatible-kimi", []*registry.ModelInfo{
		{ID: model403},
		{ID: model401},
		{ID: modelCF},
	})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	unauthAfter := time.Now().Add(30 * time.Minute)
	cfAfter := time.Now().Add(30 * time.Minute)
	if _, err := manager.Register(ctx, &Auth{
		ID:       authID,
		Provider: "openai-compatible-kimi",
		Status:   StatusError,
		ModelStates: map[string]*ModelState{
			model403: {
				Status:         StatusError,
				StatusMessage:  "quota exhausted",
				Unavailable:    true,
				NextRetryAfter: time.Now().Add(30 * time.Minute),
				Quota:          QuotaState{},
				LastError:      &Error{HTTPStatus: http.StatusForbidden},
				UpdatedAt:      time.Now(),
			},
			model401: {
				Status:         StatusError,
				StatusMessage:  "unauthorized",
				Unavailable:    true,
				NextRetryAfter: unauthAfter,
				Quota:          QuotaState{},
				LastError:      &Error{HTTPStatus: http.StatusUnauthorized},
				UpdatedAt:      time.Now(),
			},
			modelCF: {
				Status:         StatusError,
				StatusMessage:  "cloudflare challenge",
				Unavailable:    true,
				NextRetryAfter: cfAfter,
				Quota:          QuotaState{Exceeded: true, Reason: "cloudflare challenge"},
				LastError:      &Error{HTTPStatus: http.StatusForbidden, Message: "cloudflare challenge"},
				UpdatedAt:      time.Now(),
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

	now := time.Now()
	if err := manager.clearGenericPaymentFallbackCooldown(ctx, target, now); err != nil {
		t.Fatalf("clearGenericPaymentFallbackCooldown() error = %v", err)
	}

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

	// 403 model should be cleared.
	state403 := after.ModelStates[model403]
	if state403 == nil {
		t.Fatal("model 403 state missing")
	}
	if state403.Unavailable {
		t.Error("expected Unavailable=false for 403 model after clear")
	}

	// 401 model must be preserved.
	state401 := after.ModelStates[model401]
	if state401 == nil {
		t.Fatal("model 401 state missing")
	}
	if !state401.Unavailable {
		t.Error("expected Unavailable=true for 401 model (preserved)")
	}
	if !state401.NextRetryAfter.Equal(unauthAfter) {
		t.Errorf("NextRetryAfter = %v, want %v (401 cooldown preserved)", state401.NextRetryAfter, unauthAfter)
	}

	// Cloudflare 403 model must be preserved.
	stateCF := after.ModelStates[modelCF]
	if stateCF == nil {
		t.Fatal("model Cloudflare 403 state missing")
	}
	if !stateCF.Unavailable {
		t.Error("expected Unavailable=true for Cloudflare 403 model (preserved)")
	}
	if !stateCF.NextRetryAfter.Equal(cfAfter) {
		t.Errorf("NextRetryAfter = %v, want %v (Cloudflare 403 cooldown preserved)", stateCF.NextRetryAfter, cfAfter)
	}
	if stateCF.Quota.Reason != "cloudflare challenge" {
		t.Errorf("Quota.Reason = %q, want cloudflare challenge (preserved)", stateCF.Quota.Reason)
	}
}

// TestSetAuthQuotaExceeded_OverwritesStaleReasonWith403Fallback verifies that
// when a Kimi probe cooldown has expired and a fresh 402/403 generic fallback
// was layered on top (Quota.Reason == kimiUsageReason but NextRetryAfter
// diverges from NextRecoverAt), the probe still replaces it with the precise
// upstream reset time. The probe must not treat the state as still probe-owned
// (isKimiProbeOwnedCooldown already rejects it), but it also must not preserve
// it as a non-Kimi backoff — the stale Reason is misleading.
func TestSetAuthQuotaExceeded_OverwritesStaleReasonWith403Fallback(t *testing.T) {
	t.Parallel()
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "kimi-stale-403-auth"
	model := "kimi-k2"

	// Simulate: probe set a cooldown 1 hour ago (now expired).
	// Then a request hit the generic 403 fallback; MarkResult updated
	// NextRetryAfter to +30 min but left Quota.Reason == kimiUsageReason.
	pastRecover := time.Now().Add(-time.Hour)
	freshRetry := time.Now().Add(30 * time.Minute)

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
				NextRetryAfter: freshRetry, // overwritten by fresh 403 fallback
				Quota: QuotaState{ // stale probe fields
					Exceeded:      true,
					Reason:        kimiUsageReason,
					NextRecoverAt: pastRecover,
				},
				LastError: &Error{HTTPStatus: http.StatusForbidden, Message: "quota exhausted"},
				UpdatedAt: time.Now(),
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// Probe's precise reset time is 10 minutes (shorter than generic 30 min).
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

// TestSetAuthQuotaExceeded_PreservesStaleReasonWith401Overwrite verifies that
// when a Kimi probe cooldown has expired and a fresh 401 unauthorized failure
// was layered on top, the probe does NOT overwrite it. The stale
// kimiUsageReason is misleading, but LastError.HTTPStatus == 401 confirms it
// is not a generic 402/403 fallback, so the fresh 401 cooldown is preserved.
func TestSetAuthQuotaExceeded_PreservesStaleReasonWith401Overwrite(t *testing.T) {
	t.Parallel()
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "kimi-stale-401-auth"
	model := "kimi-k2"

	pastRecover := time.Now().Add(-time.Hour)
	freshRetry := time.Now().Add(30 * time.Minute)

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
				StatusMessage:  "unauthorized",
				Unavailable:    true,
				NextRetryAfter: freshRetry, // overwritten by fresh 401
				Quota: QuotaState{ // stale probe fields
					Exceeded:      true,
					Reason:        kimiUsageReason,
					NextRecoverAt: pastRecover,
				},
				LastError: &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
				UpdatedAt: time.Now(),
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

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
	// 401 cooldown must be preserved.
	if !state.NextRetryAfter.Equal(freshRetry) {
		t.Errorf("NextRetryAfter = %v, want %v (401 cooldown preserved)", state.NextRetryAfter, freshRetry)
	}
	// Stale Reason stays; the probe must not have touched it.
	if state.Quota.Reason != kimiUsageReason {
		t.Errorf("Quota.Reason = %q, want %q (stale reason preserved, not overwritten)", state.Quota.Reason, kimiUsageReason)
	}
}

// TestClearGenericPaymentFallbackCooldown_StaleReason403 verifies that when a
// Kimi probe cooldown has expired and a fresh 403 generic fallback was layered
// on top (Quota.Reason == kimiUsageReason but NextRetryAfter diverges), the
// probe's healthy-window clearing still recognizes and clears it. Without this,
// the stale Reason hides the state from both hasKimiUsageCooldown and
// hasGenericPaymentFallbackCooldown, leaving the model permanently blocked.
func TestClearGenericPaymentFallbackCooldown_StaleReason403(t *testing.T) {
	t.Parallel()
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "kimi-clear-stale-403-auth"
	model := "kimi-k2"

	pastRecover := time.Now().Add(-time.Hour)
	freshRetry := time.Now().Add(30 * time.Minute)

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
				StatusMessage:  "quota exhausted, refreshed in the next cycle",
				Unavailable:    true,
				NextRetryAfter: freshRetry, // overwritten by fresh 403 fallback
				Quota: QuotaState{ // stale probe fields
					Exceeded:      true,
					Reason:        kimiUsageReason,
					NextRecoverAt: pastRecover,
				},
				LastError: &Error{HTTPStatus: http.StatusForbidden, Message: "quota exhausted"},
				UpdatedAt: time.Now(),
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// Verify hasGenericPaymentFallbackCooldown recognizes this state.
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
	if !hasGenericPaymentFallbackCooldown(target) {
		t.Fatal("hasGenericPaymentFallbackCooldown should recognize stale reason + 403")
	}

	// Clear the generic fallback (simulating the probe seeing healthy windows).
	now := time.Now()
	if err := manager.clearGenericPaymentFallbackCooldown(ctx, target, now); err != nil {
		t.Fatalf("clearGenericPaymentFallbackCooldown() error = %v", err)
	}

	// Verify the state was cleared.
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
	if state.Unavailable {
		t.Error("expected Unavailable=false after clearing stale reason 403")
	}
	if !state.NextRetryAfter.IsZero() {
		t.Errorf("NextRetryAfter = %v, want zero", state.NextRetryAfter)
	}
}

// TestKimiUsageFullyAvailable_TotalQuotaExhausted verifies that when totalQuota
// is exhausted (remaining=0) while the other windows are positive, the account
// is not considered fully available. Without this, the probe would clear
// cooldowns prematurely even though totalQuota is still exhausted.
func TestKimiUsageFullyAvailable_TotalQuotaExhausted(t *testing.T) {
	t.Parallel()
	windows := []kimiUsageWindow{
		{Name: "five_hour", Limit: 100, Remaining: 50},
		{Name: "weekly", Limit: 1000, Remaining: 800},
		{Name: "total_quota", Limit: 100, Remaining: 0},
	}
	if kimiUsageFullyAvailable(windows) {
		t.Error("expected not fully available when total_quota is exhausted")
	}
}

// TestKimiUsageCooldown_TotalQuotaExhausted verifies that totalQuota exhaustion
// contributes to the cooldown calculation. When totalQuota is exhausted and has
// a resetTime, the account recovers at the latest reset among all exhausted
// windows.
func TestKimiUsageCooldown_TotalQuotaExhausted(t *testing.T) {
	t.Parallel()
	totalReset := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fiveHourReset := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	windows := []kimiUsageWindow{
		{Name: "five_hour", Limit: 100, Remaining: 0, ResetAt: fiveHourReset, HasReset: true},
		{Name: "weekly", Limit: 1000, Remaining: 500, HasReset: false},
		{Name: "total_quota", Limit: 100, Remaining: 0, ResetAt: totalReset, HasReset: true},
	}
	recoverAt, ok := kimiUsageCooldown(windows)
	if !ok {
		t.Fatal("expected ok when total_quota is exhausted with resetTime")
	}
	// Should return the latest reset among exhausted windows.
	if !recoverAt.Equal(totalReset) {
		t.Errorf("recoverAt=%v, want %v (total_quota reset is later)", recoverAt, totalReset)
	}
}

