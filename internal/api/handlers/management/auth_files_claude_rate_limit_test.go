package management

import (
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// fixedObservedAt pins ObservedAt for snapshot tests. Real captures use
// time.Now(); these tests construct hints directly so we own the timestamp.
func fixedObservedAt() time.Time {
	return time.Date(2026, 4, 30, 12, 34, 56, 0, time.UTC)
}

// resetClaudeRateLimitHint scrubs the hint store for a given authID so each
// test runs against a clean slate. Hint store is package-global; tests share
// it but each test uses a unique authID.
func resetClaudeRateLimitHint(t *testing.T, authID string) {
	t.Helper()
	// SetAnthropicRateLimitHint with Known=false acts as a soft-clear for
	// tests that check Known=false → nil entry. For full removal we'd need
	// a Delete API; absent that, unique authIDs prevent cross-pollution.
	t.Cleanup(func() {
		coreauth.SetAnthropicRateLimitHint(authID, coreauth.AnthropicRateLimitHint{Known: false})
	})
}

func TestBuildClaudeRateLimitEntry_NilAuth(t *testing.T) {
	if got := buildClaudeRateLimitEntry(nil); got != nil {
		t.Fatalf("expected nil for nil auth, got %v", got)
	}
}

func TestBuildClaudeRateLimitEntry_NonClaudeProvider(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "codex-non-claude@example.com",
		Provider: "codex",
	}
	coreauth.SetAnthropicRateLimitHint(auth.ID, coreauth.AnthropicRateLimitHint{
		Known:  true,
		Status: "allowed",
	})
	t.Cleanup(func() {
		coreauth.SetAnthropicRateLimitHint(auth.ID, coreauth.AnthropicRateLimitHint{Known: false})
	})

	if got := buildClaudeRateLimitEntry(auth); got != nil {
		t.Fatalf("expected nil for non-claude provider, got %v", got)
	}
}

func TestBuildClaudeRateLimitEntry_NoHintCaptured(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "claude-test-no-hint@example.com",
		Provider: "claude",
	}
	if got := buildClaudeRateLimitEntry(auth); got != nil {
		t.Fatalf("expected nil when no hint captured, got %v", got)
	}
}

func TestBuildClaudeRateLimitEntry_HintWithKnownFalse(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "claude-test-known-false@example.com",
		Provider: "claude",
	}
	resetClaudeRateLimitHint(t, auth.ID)

	coreauth.SetAnthropicRateLimitHint(auth.ID, coreauth.AnthropicRateLimitHint{
		Known:  false,
		Status: "allowed", // even with content, Known=false should suppress
	})

	if got := buildClaudeRateLimitEntry(auth); got != nil {
		t.Fatalf("expected nil when Known=false even with content, got %v", got)
	}
}

func TestBuildClaudeRateLimitEntry_FullHintRoundTrip(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "claude-test-full-hint@example.com",
		Provider: "claude",
	}
	resetClaudeRateLimitHint(t, auth.ID)

	resetTime := time.Unix(1777500000, 0).UTC()
	weekResetTime := time.Unix(1777561200, 0).UTC()
	hint := coreauth.AnthropicRateLimitHint{
		Known:                 true,
		ObservedAt:            fixedObservedAt(),
		Status:                "allowed_warning",
		RepresentativeClaim:   "seven_day",
		Reset:                 weekResetTime,
		FallbackPercentage:    0.5,
		OverageStatus:         "rejected",
		OverageDisabledReason: "org_level_disabled",
		UpgradePaths:          "upgrade_plan",
		Windows: map[string]coreauth.AnthropicQuotaWindow{
			"5h": {
				Status:         "allowed",
				Reset:          resetTime,
				Utilization:    0.35,
				HasUtilization: true,
			},
			"7d": {
				Status:             "allowed_warning",
				Reset:              weekResetTime,
				Utilization:        0.85,
				HasUtilization:     true,
				SurpassedThreshold: 0.75,
			},
		},
		RawHeaders: map[string]string{
			"anthropic-ratelimit-unified-status":         "allowed_warning",
			"anthropic-ratelimit-unified-7d-utilization": "0.85",
		},
	}
	coreauth.SetAnthropicRateLimitHint(auth.ID, hint)

	got := buildClaudeRateLimitEntry(auth)
	if got == nil {
		t.Fatal("expected non-nil entry for known hint with content")
	}

	checkField := func(key string, want any) {
		t.Helper()
		gotVal, ok := got[key]
		if !ok {
			t.Errorf("missing key %q", key)
			return
		}
		if !reflect.DeepEqual(gotVal, want) {
			t.Errorf("%s = %v want %v", key, gotVal, want)
		}
	}
	checkField("observed_at", fixedObservedAt())
	checkField("status", "allowed_warning")
	checkField("representative_claim", "seven_day")
	checkField("reset_at", weekResetTime)
	checkField("fallback_percentage", 0.5)
	checkField("overage_status", "rejected")
	checkField("overage_disabled_reason", "org_level_disabled")
	checkField("upgrade_paths", "upgrade_plan")

	windows, ok := got["windows"].(gin.H)
	if !ok {
		t.Fatalf("windows: expected gin.H, got %T", got["windows"])
	}
	if len(windows) != 2 {
		t.Fatalf("windows: expected 2 entries, got %d: %v", len(windows), windows)
	}

	w5h, ok := windows["5h"].(gin.H)
	if !ok {
		t.Fatalf("windows[5h]: expected gin.H, got %T", windows["5h"])
	}
	if w5h["status"] != "allowed" {
		t.Errorf("windows[5h].status = %v want allowed", w5h["status"])
	}
	if w5h["utilization"] != 0.35 {
		t.Errorf("windows[5h].utilization = %v want 0.35", w5h["utilization"])
	}
	if !w5h["reset_at"].(time.Time).Equal(resetTime) {
		t.Errorf("windows[5h].reset_at = %v want %v", w5h["reset_at"], resetTime)
	}
	// 5h has no surpassed_threshold; omitempty should drop it.
	if _, present := w5h["surpassed_threshold"]; present {
		t.Errorf("windows[5h]: unexpected surpassed_threshold field (should be omitted when 0)")
	}

	w7d, ok := windows["7d"].(gin.H)
	if !ok {
		t.Fatalf("windows[7d]: expected gin.H")
	}
	if w7d["surpassed_threshold"] != 0.75 {
		t.Errorf("windows[7d].surpassed_threshold = %v want 0.75", w7d["surpassed_threshold"])
	}

	rawHeaders, ok := got["raw_headers"].(map[string]string)
	if !ok {
		t.Fatalf("raw_headers: expected map[string]string, got %T", got["raw_headers"])
	}
	if rawHeaders["anthropic-ratelimit-unified-status"] != "allowed_warning" {
		t.Errorf("raw_headers passthrough broken")
	}
}

func TestBuildClaudeRateLimitEntry_OmitemptyDiscipline(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "claude-test-omitempty@example.com",
		Provider: "claude",
	}
	resetClaudeRateLimitHint(t, auth.ID)

	// Minimum-content hint: only Known=true and Status. Everything else
	// should drop out via omitempty gates.
	coreauth.SetAnthropicRateLimitHint(auth.ID, coreauth.AnthropicRateLimitHint{
		Known:      true,
		ObservedAt: fixedObservedAt(),
		Status:     "allowed",
	})

	got := buildClaudeRateLimitEntry(auth)
	if got == nil {
		t.Fatal("expected non-nil entry when at least one field is set")
	}
	for _, shouldBeAbsent := range []string{
		"representative_claim",
		"reset_at",
		"fallback_percentage",
		"overage_status",
		"overage_disabled_reason",
		"upgrade_paths",
		"windows",
		"raw_headers",
	} {
		if _, present := got[shouldBeAbsent]; present {
			t.Errorf("expected %q to be omitted from minimum-content payload, but it was present", shouldBeAbsent)
		}
	}
	if got["status"] != "allowed" {
		t.Errorf("status = %v want allowed", got["status"])
	}
	if !got["observed_at"].(time.Time).Equal(fixedObservedAt()) {
		t.Errorf("observed_at = %v want %v", got["observed_at"], fixedObservedAt())
	}
}

// TestBuildClaudeRateLimitEntry_OmitsUtilizationWhenAbsent asserts that a
// window which never received a `unified-{slug}-utilization` header surfaces
// without the `utilization` field — rather than emitting 0.0, which is
// indistinguishable from a real zero-utilization reading and would mislead
// alerts into treating unknown utilization as healthy usage.
func TestBuildClaudeRateLimitEntry_OmitsUtilizationWhenAbsent(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "claude-test-utilization-absent@example.com",
		Provider: "claude",
	}
	resetClaudeRateLimitHint(t, auth.ID)

	resetTime := time.Unix(1777500000, 0).UTC()
	hint := coreauth.AnthropicRateLimitHint{
		Known:      true,
		ObservedAt: fixedObservedAt(),
		Status:     "allowed",
		Windows: map[string]coreauth.AnthropicQuotaWindow{
			// Header was present with value 0.0 — must surface explicitly.
			"5h": {
				Status:         "allowed",
				Reset:          resetTime,
				Utilization:    0.0,
				HasUtilization: true,
			},
			// Header was absent — must NOT surface as 0.0.
			"7d": {
				Status: "allowed",
				Reset:  resetTime,
			},
		},
	}
	coreauth.SetAnthropicRateLimitHint(auth.ID, hint)

	got := buildClaudeRateLimitEntry(auth)
	if got == nil {
		t.Fatal("expected non-nil entry")
	}

	windows, ok := got["windows"].(gin.H)
	if !ok {
		t.Fatalf("windows: expected gin.H, got %T", got["windows"])
	}

	w5h, ok := windows["5h"].(gin.H)
	if !ok {
		t.Fatalf("windows[5h]: expected gin.H, got %T", windows["5h"])
	}
	if util, present := w5h["utilization"]; !present {
		t.Errorf("windows[5h]: expected utilization to be present (header was sent with value 0.0)")
	} else if util != 0.0 {
		t.Errorf("windows[5h].utilization = %v want 0.0", util)
	}

	w7d, ok := windows["7d"].(gin.H)
	if !ok {
		t.Fatalf("windows[7d]: expected gin.H, got %T", windows["7d"])
	}
	if _, present := w7d["utilization"]; present {
		t.Errorf("windows[7d]: utilization must be omitted when no header was sent (got %v)", w7d["utilization"])
	}
}

// TestBuildClaudeRateLimitEntry_OmitsEmptyWindow asserts that a slug whose
// fields all parsed to empty/zero (e.g. only a malformed
// `unified-5h-reset: garbage` arrived) is dropped from structured output
// rather than surfaced as `"5h": {}`. The forensic signal — the literal
// header value — remains accessible via raw_headers.
func TestBuildClaudeRateLimitEntry_OmitsEmptyWindow(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "claude-test-empty-window-mixed@example.com",
		Provider: "claude",
	}
	resetClaudeRateLimitHint(t, auth.ID)

	resetTime := time.Unix(1777500000, 0).UTC()
	hint := coreauth.AnthropicRateLimitHint{
		Known:      true,
		ObservedAt: fixedObservedAt(),
		Status:     "allowed",
		Windows: map[string]coreauth.AnthropicQuotaWindow{
			"5h": {
				Status:         "allowed",
				Reset:          resetTime,
				Utilization:    0.4,
				HasUtilization: true,
			},
			// All fields zero/empty: simulates a slug that only saw a
			// malformed header on the parser side.
			"7d": {},
		},
		RawHeaders: map[string]string{
			"anthropic-ratelimit-unified-7d-reset": "garbage",
		},
	}
	coreauth.SetAnthropicRateLimitHint(auth.ID, hint)

	got := buildClaudeRateLimitEntry(auth)
	if got == nil {
		t.Fatal("expected non-nil entry")
	}

	windows, ok := got["windows"].(gin.H)
	if !ok {
		t.Fatalf("windows: expected gin.H, got %T", got["windows"])
	}
	if _, present := windows["7d"]; present {
		t.Errorf("windows[7d]: empty window must not be emitted (got %v)", windows["7d"])
	}
	if _, present := windows["5h"]; !present {
		t.Error("windows[5h]: expected populated window to survive")
	}

	rawHeaders, ok := got["raw_headers"].(map[string]string)
	if !ok {
		t.Fatalf("raw_headers: expected map[string]string, got %T", got["raw_headers"])
	}
	if rawHeaders["anthropic-ratelimit-unified-7d-reset"] != "garbage" {
		t.Error("raw_headers must preserve the malformed header for forensic visibility")
	}
}

// TestBuildClaudeRateLimitEntry_OmitsWindowsKeyWhenAllEmpty asserts that
// `windows` is dropped entirely (not emitted as `{}`) when every slug got
// gated out. Top-level fields still surface.
func TestBuildClaudeRateLimitEntry_OmitsWindowsKeyWhenAllEmpty(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "claude-test-empty-window-all@example.com",
		Provider: "claude",
	}
	resetClaudeRateLimitHint(t, auth.ID)

	hint := coreauth.AnthropicRateLimitHint{
		Known:      true,
		ObservedAt: fixedObservedAt(),
		Status:     "allowed",
		Windows: map[string]coreauth.AnthropicQuotaWindow{
			"5h": {},
			"7d": {},
		},
	}
	coreauth.SetAnthropicRateLimitHint(auth.ID, hint)

	got := buildClaudeRateLimitEntry(auth)
	if got == nil {
		t.Fatal("expected non-nil entry (top-level status should survive)")
	}
	if _, present := got["windows"]; present {
		t.Errorf("windows: expected key to be omitted entirely when all slugs are empty, got %v", got["windows"])
	}
	if got["status"] != "allowed" {
		t.Errorf("status = %v want allowed", got["status"])
	}
}

func TestBuildClaudeRateLimitEntry_ProviderCasingIsTolerant(t *testing.T) {
	for _, providerCasing := range []string{"claude", "Claude", "CLAUDE", "  claude  "} {
		auth := &coreauth.Auth{
			ID:       "claude-test-casing-" + providerCasing + "@example.com",
			Provider: providerCasing,
		}
		coreauth.SetAnthropicRateLimitHint(auth.ID, coreauth.AnthropicRateLimitHint{
			Known:  true,
			Status: "allowed",
		})
		t.Cleanup(func() {
			coreauth.SetAnthropicRateLimitHint(auth.ID, coreauth.AnthropicRateLimitHint{Known: false})
		})

		if got := buildClaudeRateLimitEntry(auth); got == nil {
			t.Errorf("provider %q: expected non-nil entry but got nil", providerCasing)
		}
	}
}
