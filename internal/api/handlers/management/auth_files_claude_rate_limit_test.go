package management

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
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
	coreauth.DeleteAnthropicRateLimitHint(authID)
	t.Cleanup(func() {
		coreauth.DeleteAnthropicRateLimitHint(authID)
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
		HasFallbackPercentage: true,
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
				Status:                "allowed_warning",
				Reset:                 weekResetTime,
				Utilization:           0.85,
				HasUtilization:        true,
				SurpassedThreshold:    0.75,
				HasSurpassedThreshold: true,
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

// claudeAuthWithEmail builds a Claude OAuth auth whose account fingerprint
// resolves from the given email.
func claudeAuthWithEmail(authID, email string) *coreauth.Auth {
	return &coreauth.Auth{
		ID:       authID,
		Provider: "claude",
		Metadata: map[string]any{"email": email},
	}
}

func TestBuildClaudeRateLimitEntry_RejectsHintFromDifferentAccount(t *testing.T) {
	const authID = "claude-test-rotated@example.com"
	resetClaudeRateLimitHint(t, authID)

	previous := claudeAuthWithEmail(authID, "before-rotation@example.com")
	coreauth.SetAnthropicRateLimitHint(authID, coreauth.AnthropicRateLimitHint{
		Known:              true,
		ObservedAt:         fixedObservedAt(),
		Status:             "rejected",
		AccountFingerprint: coreauth.AnthropicAccountFingerprint(previous),
	})

	// Same auth ID, different underlying account: an in-place rotation, or a
	// capture that landed after the credential was swapped out.
	rotated := claudeAuthWithEmail(authID, "after-rotation@example.com")
	if got := buildClaudeRateLimitEntry(rotated); got != nil {
		t.Fatalf("expected nil for a hint captured against a different account, got %v", got)
	}

	// The original account still reads its own capture.
	if got := buildClaudeRateLimitEntry(previous); got == nil {
		t.Fatal("expected the capturing account to still see its own hint")
	}
}

func TestBuildClaudeRateLimitEntry_ServesHintWhenAccountUnknown(t *testing.T) {
	const authID = "claude-test-unknown-account@example.com"
	resetClaudeRateLimitHint(t, authID)

	// A capture stored without a fingerprint (auth had no identifiable
	// account at capture time) must stay readable rather than being treated
	// as belonging to some other account.
	coreauth.SetAnthropicRateLimitHint(authID, coreauth.AnthropicRateLimitHint{
		Known:      true,
		ObservedAt: fixedObservedAt(),
		Status:     "allowed",
	})

	if got := buildClaudeRateLimitEntry(claudeAuthWithEmail(authID, "someone@example.com")); got == nil {
		t.Fatal("hint with no fingerprint should be served, not rejected")
	}

	// And the mirror case: a fingerprinted capture read back through an auth
	// whose account is no longer identifiable (e.g. a refresh that returned
	// no email) must not be discarded.
	resetClaudeRateLimitHint(t, authID)
	coreauth.SetAnthropicRateLimitHint(authID, coreauth.AnthropicRateLimitHint{
		Known:              true,
		ObservedAt:         fixedObservedAt(),
		Status:             "allowed",
		AccountFingerprint: coreauth.AnthropicAccountFingerprint(claudeAuthWithEmail(authID, "someone@example.com")),
	})
	blankAccount := &coreauth.Auth{ID: authID, Provider: "claude"}
	if got := buildClaudeRateLimitEntry(blankAccount); got == nil {
		t.Fatal("hint should survive an auth whose account became unidentifiable")
	}
}

// TestBuildClaudeRateLimitEntry_OmitsAccountFingerprint pins the invariant that
// the account fingerprint never reaches a management response. It is a hash of
// Auth.AccountInfo(), which for an API-key auth is the API key itself, so it
// exists to be compared in-process and must not be published — including via a
// future field added to the hint struct and projected wholesale.
func TestBuildClaudeRateLimitEntry_OmitsAccountFingerprint(t *testing.T) {
	const authID = "auth-fingerprint-not-published"
	coreauth.DeleteAnthropicRateLimitHint(authID)
	t.Cleanup(func() { coreauth.DeleteAnthropicRateLimitHint(authID) })

	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "claude",
		Metadata: map[string]any{"email": "someone@example.com"},
	}
	fingerprint := coreauth.AnthropicAccountFingerprint(auth)
	if fingerprint == "" {
		t.Fatal("precondition: expected a non-empty fingerprint for an auth with an email")
	}

	coreauth.SetAnthropicRateLimitHint(authID, coreauth.AnthropicRateLimitHint{
		Known:              true,
		Status:             "allowed",
		ObservedAt:         time.Unix(1777500000, 0).UTC(),
		AccountFingerprint: fingerprint,
	})

	entry := buildClaudeRateLimitEntry(auth)
	if entry == nil {
		t.Fatal("expected a rate_limit entry for a stored hint")
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal rate_limit entry: %v", err)
	}
	if bytes.Contains(encoded, []byte(fingerprint)) {
		t.Errorf("account fingerprint leaked into the management response: %s", encoded)
	}
	for _, banned := range []string{"fingerprint", "account_fingerprint", "AccountFingerprint"} {
		if bytes.Contains(bytes.ToLower(encoded), bytes.ToLower([]byte(banned))) {
			t.Errorf("management response carries a %q key: %s", banned, encoded)
		}
	}
}

// TestBuildClaudeRateLimitEntry_MalformedNumericsAreNotPublished pins that a
// numeric header which failed to parse is omitted from the projection rather
// than published as a healthy-looking 0, and that a legitimate explicit 0
// survives. Those two cases are indistinguishable without the presence flags,
// and 0 is exactly the reading an operator is least likely to question.
func TestBuildClaudeRateLimitEntry_MalformedNumericsAreNotPublished(t *testing.T) {
	auth := &coreauth.Auth{ID: "auth-malformed-numerics", Provider: "claude"}
	t.Cleanup(func() { coreauth.DeleteAnthropicRateLimitHint(auth.ID) })

	t.Run("malformed omitted", func(t *testing.T) {
		coreauth.SetAnthropicRateLimitHint(auth.ID, coreauth.AnthropicRateLimitHint{
			Known: true, ObservedAt: fixedObservedAt(), Status: "allowed",
			// Parser could not read these, so no presence flag is set.
			FallbackPercentage: 0, HasFallbackPercentage: false,
			Windows: map[string]coreauth.AnthropicQuotaWindow{
				"5h": {Status: "allowed", Utilization: 0, HasUtilization: false,
					SurpassedThreshold: 0, HasSurpassedThreshold: false},
			},
		})
		entry := buildClaudeRateLimitEntry(auth)
		if _, ok := entry["fallback_percentage"]; ok {
			t.Error("malformed fallback_percentage was published as a real reading")
		}
		// gin.H is a named type: asserting to map[string]any yields a nil map
		// and every lookup below would silently report "absent".
		windows, ok := entry["windows"].(gin.H)
		if !ok {
			t.Fatalf("windows has type %T, want gin.H", entry["windows"])
		}
		w, ok := windows["5h"].(gin.H)
		if !ok {
			t.Fatalf("windows[5h] has type %T, want gin.H", windows["5h"])
		}
		if _, ok := w["utilization"]; ok {
			t.Error("malformed utilization was published as a real reading")
		}
		if _, ok := w["surpassed_threshold"]; ok {
			t.Error("malformed surpassed_threshold was published as a real reading")
		}
	})

	t.Run("explicit zero survives", func(t *testing.T) {
		coreauth.SetAnthropicRateLimitHint(auth.ID, coreauth.AnthropicRateLimitHint{
			Known: true, ObservedAt: fixedObservedAt().Add(time.Second), Status: "allowed",
			FallbackPercentage: 0, HasFallbackPercentage: true,
			Windows: map[string]coreauth.AnthropicQuotaWindow{
				"5h": {Status: "allowed", Utilization: 0, HasUtilization: true,
					SurpassedThreshold: 0, HasSurpassedThreshold: true},
			},
		})
		entry := buildClaudeRateLimitEntry(auth)
		if got, ok := entry["fallback_percentage"]; !ok || got != float64(0) {
			t.Errorf("explicit zero fallback_percentage = %v (present=%v), want 0 present", got, ok)
		}
		// gin.H is a named type: asserting to map[string]any yields a nil map
		// and every lookup below would silently report "absent".
		windows, ok := entry["windows"].(gin.H)
		if !ok {
			t.Fatalf("windows has type %T, want gin.H", entry["windows"])
		}
		w, ok := windows["5h"].(gin.H)
		if !ok {
			t.Fatalf("windows[5h] has type %T, want gin.H", windows["5h"])
		}
		if got, ok := w["utilization"]; !ok || got != float64(0) {
			t.Errorf("explicit zero utilization = %v (present=%v), want 0 present", got, ok)
		}
		if got, ok := w["surpassed_threshold"]; !ok || got != float64(0) {
			t.Errorf("explicit zero surpassed_threshold = %v (present=%v), want 0 present", got, ok)
		}
	})
}

// TestBuildClaudeRateLimitEntry_SurfacesPerWindowObservedAt pins the field that
// tells an operator which reading is live and which is inherited. The hint
// store carries a window forward when a later response omits it — an
// ordinary-tier response reports no premium weekly window — so `7d_oi` here
// comes from the earlier capture and must say so, while the windows the newest
// response did report carry the newest timestamp.
func TestBuildClaudeRateLimitEntry_SurfacesPerWindowObservedAt(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "claude-test-window-observed-at@example.com",
		Provider: "claude",
	}
	resetClaudeRateLimitHint(t, auth.ID)

	premiumAt := fixedObservedAt()
	ordinaryAt := premiumAt.Add(4 * time.Minute)
	fingerprint := coreauth.AnthropicAccountFingerprint(auth)

	coreauth.SetAnthropicRateLimitHint(auth.ID, coreauth.AnthropicRateLimitHint{
		Known:              true,
		ObservedAt:         premiumAt,
		Status:             "allowed_warning",
		AccountFingerprint: fingerprint,
		Windows: map[string]coreauth.AnthropicQuotaWindow{
			"5h":    {Status: "allowed", Reset: premiumAt.Add(2 * time.Hour), Utilization: 0.2, HasUtilization: true},
			"7d_oi": {Status: "allowed_warning", Reset: premiumAt.Add(30 * time.Hour), Utilization: 0.88, HasUtilization: true},
		},
	})
	coreauth.SetAnthropicRateLimitHint(auth.ID, coreauth.AnthropicRateLimitHint{
		Known:              true,
		ObservedAt:         ordinaryAt,
		Status:             "allowed",
		AccountFingerprint: fingerprint,
		Windows: map[string]coreauth.AnthropicQuotaWindow{
			"5h": {Status: "allowed", Reset: ordinaryAt.Add(2 * time.Hour), Utilization: 0.3, HasUtilization: true},
		},
	})

	got := buildClaudeRateLimitEntry(auth)
	if got == nil {
		t.Fatal("expected non-nil entry")
	}
	if !got["observed_at"].(time.Time).Equal(ordinaryAt) {
		t.Errorf("observed_at = %v want %v", got["observed_at"], ordinaryAt)
	}

	// gin.H is a named type: asserting to map[string]any yields a nil map
	// and every lookup below would silently report "absent".
	windows, ok := got["windows"].(gin.H)
	if !ok {
		t.Fatalf("windows has type %T, want gin.H", got["windows"])
	}

	carried, ok := windows["7d_oi"].(gin.H)
	if !ok {
		t.Fatalf("windows[7d_oi] has type %T, want gin.H — the carried window did not reach the projection", windows["7d_oi"])
	}
	observedAt, present := carried["observed_at"]
	if !present {
		t.Fatalf("windows[7d_oi]: expected observed_at so a consumer can tell a carried reading from a live one, got %v", carried)
	}
	if !observedAt.(time.Time).Equal(premiumAt) {
		t.Errorf("windows[7d_oi].observed_at = %v want %v — a carried window keeps the stamp of the capture that saw it", observedAt, premiumAt)
	}
	if carried["utilization"] != 0.88 {
		t.Errorf("windows[7d_oi].utilization = %v want 0.88", carried["utilization"])
	}

	live, ok := windows["5h"].(gin.H)
	if !ok {
		t.Fatalf("windows[5h] has type %T, want gin.H", windows["5h"])
	}
	if !live["observed_at"].(time.Time).Equal(ordinaryAt) {
		t.Errorf("windows[5h].observed_at = %v want %v", live["observed_at"], ordinaryAt)
	}
}

// TestBuildClaudeRateLimitEntry_ObservedAtDoesNotResurrectEmptyWindow guards
// the interaction between the per-window timestamp and the emptiness gate. The
// store stamps every window it holds, including one whose every header was
// malformed, so a timestamp emitted before the gate would turn `"7d": {}` into
// `"7d": {"observed_at": ...}` and put window data in front of consumers where
// there is none.
func TestBuildClaudeRateLimitEntry_ObservedAtDoesNotResurrectEmptyWindow(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "claude-test-observed-at-empty-window@example.com",
		Provider: "claude",
	}
	resetClaudeRateLimitHint(t, auth.ID)

	coreauth.SetAnthropicRateLimitHint(auth.ID, coreauth.AnthropicRateLimitHint{
		Known:      true,
		ObservedAt: fixedObservedAt(),
		Status:     "allowed",
		Windows: map[string]coreauth.AnthropicQuotaWindow{
			"5h": {Status: "allowed", Reset: fixedObservedAt().Add(2 * time.Hour), Utilization: 0.4, HasUtilization: true},
			"7d": {},
		},
		RawHeaders: map[string]string{
			"anthropic-ratelimit-unified-7d-reset": "garbage",
		},
	})

	got := buildClaudeRateLimitEntry(auth)
	if got == nil {
		t.Fatal("expected non-nil entry")
	}
	windows, ok := got["windows"].(gin.H)
	if !ok {
		t.Fatalf("windows has type %T, want gin.H", got["windows"])
	}
	if _, present := windows["7d"]; present {
		t.Errorf("windows[7d]: a window with nothing to report must stay dropped, got %v", windows["7d"])
	}
}

// TestBuildClaudeRateLimitEntry_WindowEmptinessMatchesStore walks every
// combination of the four fields that count as window content and asserts a
// window is emitted exactly when at least one is set.
//
// The store applies the same rule on the write side to decide whether a
// capture's entry counts as a reported window (coreauth's windowHasContent). If
// the two drift, a window the store treats as unreported still gets published
// as though it were a reading, or a real one silently stops being published.
func TestBuildClaudeRateLimitEntry_WindowEmptinessMatchesStore(t *testing.T) {
	resetAt := fixedObservedAt().Add(2 * time.Hour)
	for combo := 0; combo < 16; combo++ {
		hasStatus := combo&1 != 0
		hasUtilization := combo&2 != 0
		hasThreshold := combo&4 != 0
		hasReset := combo&8 != 0
		name := fmt.Sprintf("status=%v/util=%v/threshold=%v/reset=%v", hasStatus, hasUtilization, hasThreshold, hasReset)

		t.Run(name, func(t *testing.T) {
			auth := &coreauth.Auth{ID: "claude-window-emptiness-" + name, Provider: "claude"}
			resetClaudeRateLimitHint(t, auth.ID)

			window := coreauth.AnthropicQuotaWindow{}
			if hasStatus {
				window.Status = "allowed"
			}
			if hasUtilization {
				window.HasUtilization = true
			}
			if hasThreshold {
				window.HasSurpassedThreshold = true
			}
			if hasReset {
				window.Reset = resetAt
			}
			coreauth.SetAnthropicRateLimitHint(auth.ID, coreauth.AnthropicRateLimitHint{
				Known:      true,
				ObservedAt: fixedObservedAt(),
				Status:     "allowed",
				Windows:    map[string]coreauth.AnthropicQuotaWindow{"7d": window},
			})

			entry := buildClaudeRateLimitEntry(auth)
			if entry == nil {
				t.Fatal("expected an entry: the hint carries a top-level status")
			}
			// gin.H is a named type: asserting to map[string]any yields a nil
			// map and the lookup below would always report "absent".
			var published bool
			if windows, ok := entry["windows"].(gin.H); ok {
				_, published = windows["7d"]
			}
			want := hasStatus || hasUtilization || hasThreshold || hasReset
			if published != want {
				t.Errorf("window published = %v, want %v (%s)", published, want, name)
			}
		})
	}
}
