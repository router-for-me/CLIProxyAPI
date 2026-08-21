package helps

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// resetAnthropicHint clears any prior hint for an authID so each test runs
// against a clean slate. The hint store is package-global; tests share it.
func resetAnthropicHint(t *testing.T, authID string) {
	t.Helper()
	cliproxyauth.DeleteAnthropicRateLimitHint(authID)
	t.Cleanup(func() {
		cliproxyauth.DeleteAnthropicRateLimitHint(authID)
	})
}

// testAuth builds the minimal OAuth auth the capture path needs: an ID to key
// the store by, and an email so AnthropicAccountFingerprint resolves to a
// stable non-empty value. Tests that care about account identity pass an
// explicit email; the rest reuse the authID.
func testAuth(authID string) *cliproxyauth.Auth {
	return testAuthWithEmail(authID, authID)
}

func testAuthWithEmail(authID, email string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       authID,
		Provider: "claude",
		Metadata: map[string]any{"email": email},
	}
}

// fixturePinnedNow is when the captured fixtures were observed. Pinning keeps
// time-dependent assertions deterministic.
//
// Captured 2026-04-29 from real api.anthropic.com /v1/messages traffic;
// 1777408679 is the earliest sample's `ts`.
func fixturePinnedNow() time.Time {
	return time.Unix(1777408679, 0).UTC()
}

// realCapture200Allowed mirrors the most common /v1/messages 200 response on a
// healthy subscription — `unified-status: allowed`, both 5h and 7d at 0.0
// utilization, no surpassed-threshold, no upgrade-paths.
//
// Source: ~/.cache/claude-rate-limits.jsonl ts=1777482383.7548852.
func realCapture200Allowed() http.Header {
	return http.Header{
		"Anthropic-Ratelimit-Unified-Status":                  {"allowed"},
		"Anthropic-Ratelimit-Unified-5h-Status":               {"allowed"},
		"Anthropic-Ratelimit-Unified-5h-Reset":                {"1777500000"},
		"Anthropic-Ratelimit-Unified-5h-Utilization":          {"0.0"},
		"Anthropic-Ratelimit-Unified-7d-Status":               {"allowed"},
		"Anthropic-Ratelimit-Unified-7d-Reset":                {"1777561200"},
		"Anthropic-Ratelimit-Unified-7d-Utilization":          {"0.0"},
		"Anthropic-Ratelimit-Unified-Representative-Claim":    {"five_hour"},
		"Anthropic-Ratelimit-Unified-Fallback-Percentage":     {"0.5"},
		"Anthropic-Ratelimit-Unified-Reset":                   {"1777500000"},
		"Anthropic-Ratelimit-Unified-Overage-Disabled-Reason": {"org_level_disabled"},
		"Anthropic-Ratelimit-Unified-Overage-Status":          {"rejected"},
	}
}

// realCapture200WarningWithTierWindow mirrors a 200 response where the 7d
// window is past the 0.75 surpassed-threshold AND a tier-specific 7d_sonnet
// window is reported alongside the family 7d window. This is the "generative
// windows map" load-bearing case — `7d_sonnet` is not a hard-coded enum value.
//
// Source: ~/.cache/claude-rate-limits.jsonl ts=1777413065.625119.
func realCapture200WarningWithTierWindow() http.Header {
	return http.Header{
		"Anthropic-Ratelimit-Unified-Status":                  {"allowed_warning"},
		"Anthropic-Ratelimit-Unified-5h-Status":               {"allowed"},
		"Anthropic-Ratelimit-Unified-5h-Reset":                {"1777420800"},
		"Anthropic-Ratelimit-Unified-5h-Utilization":          {"0.35"},
		"Anthropic-Ratelimit-Unified-7d-Status":               {"allowed_warning"},
		"Anthropic-Ratelimit-Unified-7d-Reset":                {"1777575600"},
		"Anthropic-Ratelimit-Unified-7d-Utilization":          {"0.85"},
		"Anthropic-Ratelimit-Unified-7d-Surpassed-Threshold":  {"0.75"},
		"Anthropic-Ratelimit-Unified-7d_sonnet-Status":        {"allowed"},
		"Anthropic-Ratelimit-Unified-7d_sonnet-Reset":         {"1777575600"},
		"Anthropic-Ratelimit-Unified-7d_sonnet-Utilization":   {"0.07"},
		"Anthropic-Ratelimit-Unified-Representative-Claim":    {"seven_day"},
		"Anthropic-Ratelimit-Unified-Fallback-Percentage":     {"0.5"},
		"Anthropic-Ratelimit-Unified-Reset":                   {"1777575600"},
		"Anthropic-Ratelimit-Unified-Overage-Disabled-Reason": {"org_level_disabled_until"},
		"Anthropic-Ratelimit-Unified-Overage-Status":          {"rejected"},
	}
}

// realCapture429Rejected mirrors a 429 response where the 5h window is past
// 1.0 utilization (overage is permitted but `overage-status: rejected` means
// the org has it disabled). Includes `upgrade-paths`, which is observed only
// at 429 / near-cap.
//
// Source: ~/.cache/claude-rate-limits.jsonl ts=1777482352.032908.
func realCapture429Rejected() http.Header {
	return http.Header{
		"Anthropic-Ratelimit-Unified-Status":                  {"rejected"},
		"Anthropic-Ratelimit-Unified-5h-Status":               {"rejected"},
		"Anthropic-Ratelimit-Unified-5h-Reset":                {"1777500000"},
		"Anthropic-Ratelimit-Unified-5h-Utilization":          {"1.13"},
		"Anthropic-Ratelimit-Unified-5h-Surpassed-Threshold":  {"1.0"},
		"Anthropic-Ratelimit-Unified-7d-Status":               {"allowed"},
		"Anthropic-Ratelimit-Unified-7d-Reset":                {"1777561200"},
		"Anthropic-Ratelimit-Unified-7d-Utilization":          {"0.09"},
		"Anthropic-Ratelimit-Unified-Representative-Claim":    {"five_hour"},
		"Anthropic-Ratelimit-Unified-Fallback-Percentage":     {"0.5"},
		"Anthropic-Ratelimit-Unified-Reset":                   {"1777500000"},
		"Anthropic-Ratelimit-Unified-Overage-Disabled-Reason": {"org_level_disabled"},
		"Anthropic-Ratelimit-Unified-Overage-Status":          {"rejected"},
		"Anthropic-Ratelimit-Unified-Upgrade-Paths":           {"upgrade_plan"},
	}
}

func TestRecordAnthropicRateLimit_Typical200(t *testing.T) {
	const authID = "claude-test-200-allowed@example.com"
	resetAnthropicHint(t, authID)

	now := fixturePinnedNow()
	RecordAnthropicRateLimit(testAuth(authID), realCapture200Allowed(), now)

	hint, ok := cliproxyauth.GetAnthropicRateLimitHint(authID)
	if !ok || !hint.Known {
		t.Fatalf("expected hint to be set with Known=true after typical 200")
	}
	if hint.Status != "allowed" {
		t.Errorf("Status=%q want %q", hint.Status, "allowed")
	}
	if hint.RepresentativeClaim != "five_hour" {
		t.Errorf("RepresentativeClaim=%q want %q", hint.RepresentativeClaim, "five_hour")
	}
	if want := time.Unix(1777500000, 0).UTC(); !hint.Reset.Equal(want) {
		t.Errorf("Reset=%v want %v", hint.Reset, want)
	}
	if hint.FallbackPercentage != 0.5 {
		t.Errorf("FallbackPercentage=%v want 0.5", hint.FallbackPercentage)
	}
	if hint.OverageStatus != "rejected" {
		t.Errorf("OverageStatus=%q want %q", hint.OverageStatus, "rejected")
	}
	if hint.OverageDisabledReason != "org_level_disabled" {
		t.Errorf("OverageDisabledReason=%q want %q", hint.OverageDisabledReason, "org_level_disabled")
	}
	if hint.UpgradePaths != "" {
		t.Errorf("UpgradePaths should be empty when absent, got %q", hint.UpgradePaths)
	}
	if !hint.ObservedAt.Equal(now) {
		t.Errorf("ObservedAt=%v want %v", hint.ObservedAt, now)
	}

	if len(hint.Windows) != 2 {
		t.Fatalf("expected 2 windows (5h, 7d), got %d: %v", len(hint.Windows), hint.Windows)
	}
	if w, ok := hint.Windows["5h"]; !ok {
		t.Errorf("missing 5h window")
	} else {
		if w.Status != "allowed" {
			t.Errorf("5h.Status=%q", w.Status)
		}
		if w.Utilization != 0.0 {
			t.Errorf("5h.Utilization=%v", w.Utilization)
		}
		if !w.HasUtilization {
			t.Errorf("5h.HasUtilization=false want true (header was present with value 0.0)")
		}
		if w.SurpassedThreshold != 0 {
			t.Errorf("5h.SurpassedThreshold=%v want 0 (absent)", w.SurpassedThreshold)
		}
	}
	if w, ok := hint.Windows["7d"]; !ok {
		t.Errorf("missing 7d window")
	} else {
		if !w.Reset.Equal(time.Unix(1777561200, 0).UTC()) {
			t.Errorf("7d.Reset=%v", w.Reset)
		}
	}

	// raw_headers should preserve every observed unified-* header verbatim.
	if got := hint.RawHeaders["anthropic-ratelimit-unified-5h-utilization"]; got != "0.0" {
		t.Errorf("raw_headers[5h-utilization]=%q want %q", got, "0.0")
	}
	if len(hint.RawHeaders) != 12 {
		t.Errorf("expected 12 raw headers, got %d: %v", len(hint.RawHeaders), hint.RawHeaders)
	}
}

func TestRecordAnthropicRateLimit_GenerativeTierWindow(t *testing.T) {
	const authID = "claude-test-tier-window@example.com"
	resetAnthropicHint(t, authID)

	RecordAnthropicRateLimit(testAuth(authID), realCapture200WarningWithTierWindow(), fixturePinnedNow())

	hint, _ := cliproxyauth.GetAnthropicRateLimitHint(authID)
	if hint.Status != "allowed_warning" {
		t.Fatalf("Status=%q want %q", hint.Status, "allowed_warning")
	}
	if hint.RepresentativeClaim != "seven_day" {
		t.Fatalf("RepresentativeClaim=%q want %q", hint.RepresentativeClaim, "seven_day")
	}
	if hint.OverageDisabledReason != "org_level_disabled_until" {
		t.Errorf("OverageDisabledReason=%q want %q", hint.OverageDisabledReason, "org_level_disabled_until")
	}

	// The load-bearing assertion: we captured a window we never declared
	// statically. If this slug ever needs special-casing, the design has
	// failed; it must remain pure pass-through.
	if len(hint.Windows) != 3 {
		t.Fatalf("expected 3 windows (5h, 7d, 7d_sonnet), got %d: %v", len(hint.Windows), hint.Windows)
	}
	tier, ok := hint.Windows["7d_sonnet"]
	if !ok {
		t.Fatalf("missing 7d_sonnet window — generative-map design broken")
	}
	if tier.Utilization != 0.07 {
		t.Errorf("7d_sonnet.Utilization=%v want 0.07", tier.Utilization)
	}
	if tier.Status != "allowed" {
		t.Errorf("7d_sonnet.Status=%q want %q", tier.Status, "allowed")
	}

	// Surpassed-threshold should attach to the 7d window (where it was sent),
	// NOT to 7d_sonnet (which has no -surpassed-threshold header).
	if hint.Windows["7d"].SurpassedThreshold != 0.75 {
		t.Errorf("7d.SurpassedThreshold=%v want 0.75", hint.Windows["7d"].SurpassedThreshold)
	}
	if hint.Windows["7d_sonnet"].SurpassedThreshold != 0 {
		t.Errorf("7d_sonnet.SurpassedThreshold should be 0 (absent), got %v",
			hint.Windows["7d_sonnet"].SurpassedThreshold)
	}
}

func TestRecordAnthropicRateLimit_429Rejected(t *testing.T) {
	const authID = "claude-test-429-rejected@example.com"
	resetAnthropicHint(t, authID)

	RecordAnthropicRateLimit(testAuth(authID), realCapture429Rejected(), fixturePinnedNow())

	hint, ok := cliproxyauth.GetAnthropicRateLimitHint(authID)
	if !ok || !hint.Known {
		t.Fatal("expected hint Known after 429 — capture must run on errors too")
	}
	if hint.Status != "rejected" {
		t.Errorf("Status=%q want %q", hint.Status, "rejected")
	}
	if hint.UpgradePaths != "upgrade_plan" {
		t.Errorf("UpgradePaths=%q want %q (only present at/near-cap)", hint.UpgradePaths, "upgrade_plan")
	}
	w := hint.Windows["5h"]
	if w.Utilization != 1.13 {
		t.Errorf("5h.Utilization=%v want 1.13 (overage)", w.Utilization)
	}
	if w.SurpassedThreshold != 1.0 {
		t.Errorf("5h.SurpassedThreshold=%v want 1.0", w.SurpassedThreshold)
	}
	if w.Status != "rejected" {
		t.Errorf("5h.Status=%q want %q", w.Status, "rejected")
	}
}

func TestRecordAnthropicRateLimit_NoUnifiedHeaders(t *testing.T) {
	// Regression case: NousResearch/hermes-agent#17169 reports that some 429
	// paths now ship without `unified-*` headers entirely. The capture must
	// be a no-op in that case, leaving the prior hint untouched.
	const authID = "claude-test-no-unified@example.com"
	resetAnthropicHint(t, authID)

	priorHint := cliproxyauth.AnthropicRateLimitHint{
		Known:               true,
		Status:              "allowed",
		RepresentativeClaim: "five_hour",
	}
	cliproxyauth.SetAnthropicRateLimitHint(authID, priorHint)

	headersWithoutFamily := http.Header{
		"Content-Type": {"application/json"},
		"Retry-After":  {"60"},
	}
	RecordAnthropicRateLimit(testAuth(authID), headersWithoutFamily, fixturePinnedNow())

	got, ok := cliproxyauth.GetAnthropicRateLimitHint(authID)
	if !ok {
		t.Fatal("prior hint disappeared — capture should have been a no-op")
	}
	if got.Status != "allowed" || got.RepresentativeClaim != "five_hour" {
		t.Fatalf("prior hint mutated: got=%+v", got)
	}
}

// TestRecordAnthropicRateLimit_HeadersWithoutUnifiedStatus asserts that the
// capture still records hints when the response carries other `unified-*`
// fields but lacks `unified-status` specifically. Earlier drafts of this
// function used `unified-status` as a probe to early-bail, which dropped data
// in this case (raised by chatgpt-codex-connector on PR #3170). The current
// implementation iterates every header before deciding whether to write.
func TestRecordAnthropicRateLimit_HeadersWithoutUnifiedStatus(t *testing.T) {
	const authID = "claude-test-no-unified-status@example.com"
	resetAnthropicHint(t, authID)

	headers := http.Header{
		// `unified-status` deliberately absent.
		"Anthropic-Ratelimit-Unified-5h-Status":            {"allowed"},
		"Anthropic-Ratelimit-Unified-5h-Reset":             {"1777500000"},
		"Anthropic-Ratelimit-Unified-5h-Utilization":       {"0.42"},
		"Anthropic-Ratelimit-Unified-Representative-Claim": {"five_hour"},
		"Anthropic-Ratelimit-Unified-Reset":                {"1777500000"},
	}
	RecordAnthropicRateLimit(testAuth(authID), headers, fixturePinnedNow())

	hint, ok := cliproxyauth.GetAnthropicRateLimitHint(authID)
	if !ok || !hint.Known {
		t.Fatal("expected hint to be set even without unified-status header")
	}
	if hint.Status != "" {
		t.Errorf("Status should be empty when header absent, got %q", hint.Status)
	}
	if hint.RepresentativeClaim != "five_hour" {
		t.Errorf("RepresentativeClaim=%q want five_hour", hint.RepresentativeClaim)
	}
	w := hint.Windows["5h"]
	if w.Utilization != 0.42 {
		t.Errorf("5h.Utilization=%v want 0.42", w.Utilization)
	}
	if got := hint.RawHeaders["anthropic-ratelimit-unified-5h-utilization"]; got != "0.42" {
		t.Errorf("raw_headers[5h-utilization]=%q want %q", got, "0.42")
	}
}

func TestRecordAnthropicRateLimit_NilHeadersNoop(t *testing.T) {
	const authID = "claude-test-nil-headers@example.com"
	resetAnthropicHint(t, authID)

	RecordAnthropicRateLimit(testAuth(authID), nil, fixturePinnedNow())

	if _, ok := cliproxyauth.GetAnthropicRateLimitHint(authID); ok {
		t.Fatal("nil headers should not create a hint")
	}
}

func TestRecordAnthropicRateLimit_EmptyAuthIDNoop(t *testing.T) {
	RecordAnthropicRateLimit(testAuth(""), realCapture200Allowed(), fixturePinnedNow())
	RecordAnthropicRateLimit(testAuth("   "), realCapture200Allowed(), fixturePinnedNow())

	// Assert the no-op rather than only relying on the absence of a panic:
	// a blank ID must not seed an entry that a later blank-ID read returns.
	for _, id := range []string{"", "   "} {
		if _, ok := cliproxyauth.GetAnthropicRateLimitHint(id); ok {
			t.Errorf("a blank auth ID (%q) seeded the hint store", id)
		}
	}
}

// The parameter is unused on purpose: the assertion here is that the call does
// not panic, which the test framework reports without any explicit check. The
// executor call sites nil-check auth today, but the helper must not rely on it.
func TestRecordAnthropicRateLimit_NilAuthNoop(_ *testing.T) {
	RecordAnthropicRateLimit(nil, realCapture200Allowed(), fixturePinnedNow())
}

func TestRecordAnthropicRateLimit_MalformedNumericsAreTolerated(t *testing.T) {
	const authID = "claude-test-malformed@example.com"
	resetAnthropicHint(t, authID)

	headers := http.Header{
		"Anthropic-Ratelimit-Unified-Status":               {"allowed"},
		"Anthropic-Ratelimit-Unified-5h-Status":            {"allowed"},
		"Anthropic-Ratelimit-Unified-5h-Reset":             {"not-a-number"},
		"Anthropic-Ratelimit-Unified-5h-Utilization":       {"???"},
		"Anthropic-Ratelimit-Unified-Representative-Claim": {"five_hour"},
		"Anthropic-Ratelimit-Unified-Fallback-Percentage":  {""},
		"Anthropic-Ratelimit-Unified-Reset":                {"1777500000"},
	}
	RecordAnthropicRateLimit(testAuth(authID), headers, fixturePinnedNow())

	hint, _ := cliproxyauth.GetAnthropicRateLimitHint(authID)
	if hint.Status != "allowed" {
		t.Errorf("Status=%q (well-formed string field should still parse)", hint.Status)
	}
	if !hint.Reset.Equal(time.Unix(1777500000, 0).UTC()) {
		t.Errorf("top-level Reset=%v (well-formed epoch should still parse)", hint.Reset)
	}
	w := hint.Windows["5h"]
	if w.Status != "allowed" {
		t.Errorf("5h.Status=%q (string field unaffected by neighbor's malformed value)", w.Status)
	}
	if !w.Reset.IsZero() {
		t.Errorf("5h.Reset=%v want zero (malformed epoch falls back to zero)", w.Reset)
	}
	if w.Utilization != 0 {
		t.Errorf("5h.Utilization=%v want 0 (malformed float falls back to zero)", w.Utilization)
	}
	if hint.FallbackPercentage != 0 {
		t.Errorf("FallbackPercentage=%v want 0 (empty string falls back to zero)", hint.FallbackPercentage)
	}

	// Raw headers preserve the malformed strings verbatim — operators reading
	// raw_headers can recover the original payload for debugging.
	if got := hint.RawHeaders["anthropic-ratelimit-unified-5h-reset"]; got != "not-a-number" {
		t.Errorf("raw_headers[5h-reset]=%q want %q", got, "not-a-number")
	}
}

func TestRecordAnthropicRateLimit_OverwritesPriorHint(t *testing.T) {
	const authID = "claude-test-overwrite@example.com"
	resetAnthropicHint(t, authID)

	RecordAnthropicRateLimit(testAuth(authID), realCapture200Allowed(), fixturePinnedNow())
	RecordAnthropicRateLimit(testAuth(authID), realCapture429Rejected(), fixturePinnedNow().Add(time.Minute))

	hint, _ := cliproxyauth.GetAnthropicRateLimitHint(authID)
	if hint.Status != "rejected" {
		t.Fatalf("expected last-seen 429 to overwrite prior 200; got Status=%q", hint.Status)
	}
	if hint.UpgradePaths != "upgrade_plan" {
		t.Errorf("expected last-seen UpgradePaths to be present; got %q", hint.UpgradePaths)
	}
}

func TestRecordAnthropicRateLimit_UnknownFieldGoesToRawHeadersOnly(t *testing.T) {
	const authID = "claude-test-unknown-field@example.com"
	resetAnthropicHint(t, authID)

	headers := http.Header{
		"Anthropic-Ratelimit-Unified-Status":               {"allowed"},
		"Anthropic-Ratelimit-Unified-Representative-Claim": {"five_hour"},
		"Anthropic-Ratelimit-Unified-Reset":                {"1777500000"},
		"Anthropic-Ratelimit-Unified-Future-Field-Type-X":  {"someValue"}, // not a known top-level or per-window suffix
	}
	RecordAnthropicRateLimit(testAuth(authID), headers, fixturePinnedNow())

	hint, _ := cliproxyauth.GetAnthropicRateLimitHint(authID)
	if got := hint.RawHeaders["anthropic-ratelimit-unified-future-field-type-x"]; got != "someValue" {
		t.Errorf("unknown header should round-trip via raw_headers, got %q", got)
	}
	if hint.Status != "allowed" {
		t.Errorf("known fields should still parse alongside unknowns; Status=%q", hint.Status)
	}
}

// TestRecordAnthropicRateLimit_UnknownTopLevelDoesNotFabricateWindow asserts
// that a future top-level header ending in a per-window field suffix
// (`...-status`, `...-reset`, `...-utilization`, `...-surpassed-threshold`) is
// NOT misparsed into a synthetic windows[slug] entry. Without the slug-regex
// gate, `unified-overage-reset` would create windows["overage"]; with the
// gate, the header stays raw-only and surfaces via RawHeaders for
// forward-compat visibility.
func TestRecordAnthropicRateLimit_UnknownTopLevelDoesNotFabricateWindow(t *testing.T) {
	const authID = "claude-test-future-toplevel@example.com"
	resetAnthropicHint(t, authID)

	headers := http.Header{
		"Anthropic-Ratelimit-Unified-Status":               {"allowed"},
		"Anthropic-Ratelimit-Unified-5h-Status":            {"allowed"},
		"Anthropic-Ratelimit-Unified-5h-Reset":             {"1777500000"},
		"Anthropic-Ratelimit-Unified-5h-Utilization":       {"0.0"},
		"Anthropic-Ratelimit-Unified-Representative-Claim": {"five_hour"},
		"Anthropic-Ratelimit-Unified-Reset":                {"1777500000"},
		// Hypothetical future top-level headers ending in known field
		// suffixes. None of these should produce a windows[slug] entry.
		"Anthropic-Ratelimit-Unified-Overage-Reset":           {"1777800000"},
		"Anthropic-Ratelimit-Unified-Foo-Status":              {"someValue"},
		"Anthropic-Ratelimit-Unified-Bar-Utilization":         {"0.42"},
		"Anthropic-Ratelimit-Unified-Baz-Surpassed-Threshold": {"0.9"},
	}
	RecordAnthropicRateLimit(testAuth(authID), headers, fixturePinnedNow())

	hint, ok := cliproxyauth.GetAnthropicRateLimitHint(authID)
	if !ok || !hint.Known {
		t.Fatal("expected hint to be set with Known=true")
	}

	// Only the real "5h" window should be present.
	if len(hint.Windows) != 1 {
		t.Fatalf("expected exactly 1 window (5h), got %d: %v", len(hint.Windows), hint.Windows)
	}
	if _, ok := hint.Windows["5h"]; !ok {
		t.Fatal("expected real 5h window to be parsed")
	}
	for _, ghost := range []string{"overage", "foo", "bar", "baz"} {
		if _, ok := hint.Windows[ghost]; ok {
			t.Errorf("found fabricated window %q — slug regex gate failed", ghost)
		}
	}

	// The unknown headers must still round-trip via RawHeaders so operators
	// can observe schema drift.
	rawChecks := map[string]string{
		"anthropic-ratelimit-unified-overage-reset":           "1777800000",
		"anthropic-ratelimit-unified-foo-status":              "someValue",
		"anthropic-ratelimit-unified-bar-utilization":         "0.42",
		"anthropic-ratelimit-unified-baz-surpassed-threshold": "0.9",
	}
	for k, want := range rawChecks {
		if got := hint.RawHeaders[k]; got != want {
			t.Errorf("raw_headers[%s]=%q want %q", k, got, want)
		}
	}
}

// TestRecordAnthropicRateLimit_FutureTierWindowsAccepted asserts that the
// slug regex doesn't reject *future* legitimate window tiers. Anthropic could
// introduce new windows like 30d, 1h, 12h, 7d_haiku, etc.; all must continue
// to flow through the per-window fallback.
func TestRecordAnthropicRateLimit_FutureTierWindowsAccepted(t *testing.T) {
	const authID = "claude-test-future-windows@example.com"
	resetAnthropicHint(t, authID)

	headers := http.Header{
		// Existing tiers (regression check).
		"Anthropic-Ratelimit-Unified-5h-Utilization":        {"0.1"},
		"Anthropic-Ratelimit-Unified-7d-Utilization":        {"0.2"},
		"Anthropic-Ratelimit-Unified-7d_opus-Utilization":   {"0.3"},
		"Anthropic-Ratelimit-Unified-7d_sonnet-Utilization": {"0.4"},
		// Hypothetical future tiers — same shape, different numbers/words.
		"Anthropic-Ratelimit-Unified-30d-Utilization":      {"0.5"},
		"Anthropic-Ratelimit-Unified-1h-Utilization":       {"0.6"},
		"Anthropic-Ratelimit-Unified-12h-Utilization":      {"0.7"},
		"Anthropic-Ratelimit-Unified-7d_haiku-Utilization": {"0.8"},
		"Anthropic-Ratelimit-Unified-Status":               {"allowed"},
	}
	RecordAnthropicRateLimit(testAuth(authID), headers, fixturePinnedNow())

	hint, _ := cliproxyauth.GetAnthropicRateLimitHint(authID)
	wantWindows := map[string]float64{
		"5h":        0.1,
		"7d":        0.2,
		"7d_opus":   0.3,
		"7d_sonnet": 0.4,
		"30d":       0.5,
		"1h":        0.6,
		"12h":       0.7,
		"7d_haiku":  0.8,
	}
	if len(hint.Windows) != len(wantWindows) {
		t.Fatalf("expected %d windows, got %d: %v", len(wantWindows), len(hint.Windows), hint.Windows)
	}
	for slug, wantUtil := range wantWindows {
		got, ok := hint.Windows[slug]
		if !ok {
			t.Errorf("missing window %q (slug regex too strict?)", slug)
			continue
		}
		if got.Utilization != wantUtil {
			t.Errorf("Windows[%q].Utilization=%v want %v", slug, got.Utilization, wantUtil)
		}
	}
}

// TestRecordAnthropicRateLimit_HasUtilizationDistinguishesAbsentFromZero
// asserts that a window which lands with Status and Reset but no Utilization
// header keeps HasUtilization=false, so downstream serializers can omit the
// utilization field rather than emit 0.0 — which would be indistinguishable
// from a real zero-utilization reading.
func TestRecordAnthropicRateLimit_HasUtilizationDistinguishesAbsentFromZero(t *testing.T) {
	const authID = "claude-test-utilization-presence@example.com"
	resetAnthropicHint(t, authID)

	headers := http.Header{
		"Anthropic-Ratelimit-Unified-Status":               {"allowed"},
		"Anthropic-Ratelimit-Unified-Representative-Claim": {"five_hour"},
		"Anthropic-Ratelimit-Unified-Reset":                {"1777500000"},
		// 5h window: full triple — status, reset, AND utilization=0.0.
		// HasUtilization should be true because the header was present.
		"Anthropic-Ratelimit-Unified-5h-Status":      {"allowed"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       {"1777500000"},
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.0"},
		// 7d window: status + reset but NO utilization. HasUtilization
		// should be false; the field should not surface as 0.0 to consumers.
		"Anthropic-Ratelimit-Unified-7d-Status": {"allowed"},
		"Anthropic-Ratelimit-Unified-7d-Reset":  {"1777561200"},
	}
	RecordAnthropicRateLimit(testAuth(authID), headers, fixturePinnedNow())

	hint, ok := cliproxyauth.GetAnthropicRateLimitHint(authID)
	if !ok || !hint.Known {
		t.Fatal("expected hint to be set with Known=true")
	}

	w5h, ok := hint.Windows["5h"]
	if !ok {
		t.Fatal("missing 5h window")
	}
	if w5h.Utilization != 0.0 {
		t.Errorf("5h.Utilization=%v want 0.0", w5h.Utilization)
	}
	if !w5h.HasUtilization {
		t.Error("5h.HasUtilization=false want true (header was present with value 0.0)")
	}

	w7d, ok := hint.Windows["7d"]
	if !ok {
		t.Fatal("missing 7d window")
	}
	if w7d.Utilization != 0 {
		t.Errorf("7d.Utilization=%v want 0 (untouched zero value)", w7d.Utilization)
	}
	if w7d.HasUtilization {
		t.Error("7d.HasUtilization=true want false (no -utilization header was sent for this window)")
	}
}

func TestParseAnthropicEpochSeconds(t *testing.T) {
	tests := []struct {
		in   string
		want time.Time
	}{
		{"1777500000", time.Unix(1777500000, 0).UTC()},
		{"  1777500000  ", time.Unix(1777500000, 0).UTC()},
		{"", time.Time{}},
		{"   ", time.Time{}},
		{"not-a-number", time.Time{}},
		{"1777500000.5", time.Time{}}, // strict integer; floats rejected
		// Out-of-range epochs are rejected: time.Time.MarshalJSON refuses
		// to serialize years outside [0001, 9999], so a malicious upstream
		// supplying a huge epoch would otherwise crash any management
		// endpoint that JSON-marshals the parent hint.
		{"99999999999999", time.Time{}},  // year 5138+
		{"-99999999999999", time.Time{}}, // pre-year 0001
	}
	for _, tc := range tests {
		got := parseAnthropicEpochSeconds(tc.in)
		if !got.Equal(tc.want) {
			t.Errorf("parseAnthropicEpochSeconds(%q)=%v want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseAnthropicFloat(t *testing.T) {
	tests := []struct {
		in     string
		want   float64
		wantOK bool
	}{
		{"0.5", 0.5, true},
		{"1.13", 1.13, true},
		// An explicit zero is a real reading and must report ok, so a consumer
		// can tell it apart from absent and from malformed.
		{"0.0", 0, true},
		{"  0.5  ", 0.5, true},
		{"", 0, false},
		{"not-a-number", 0, false},
		// Non-finite values: strconv.ParseFloat accepts these literals
		// without error, but they break downstream JSON serialization and
		// any consumer arithmetic (NaN comparisons, Inf accumulation).
		// Treat as parse failure.
		{"NaN", 0, false},
		{"nan", 0, false},
		{"Inf", 0, false},
		{"+Inf", 0, false},
		{"-Inf", 0, false},
		{"infinity", 0, false},
	}
	for _, tc := range tests {
		got, ok := parseAnthropicFloat(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("parseAnthropicFloat(%q)=(%v,%v) want (%v,%v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

// TestRecordAnthropicRateLimit_SurvivesTokenRefreshButNotRotation is the
// end-to-end guard over the capture → lifecycle → read path, exercised through
// a real Manager rather than by poking the store directly.
//
// Both halves were live defects at some point in this feature's history: an
// unconditional scrub in Manager.Update destroyed valid quota state on every
// routine token refresh, and before the account fingerprint existed a capture
// could outlive the credential it described.
func TestRecordAnthropicRateLimit_SurvivesTokenRefreshButNotRotation(t *testing.T) {
	const authID = "claude-e2e-refresh-vs-rotation@example.com"
	resetAnthropicHint(t, authID)

	manager := cliproxyauth.NewManager(nil, nil, nil)
	ctx := context.Background()

	original := testAuthWithEmail(authID, "account-one@example.com")
	if _, err := manager.Register(ctx, original); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	// A Claude response lands and is captured.
	RecordAnthropicRateLimit(original, realCapture200WarningWithTierWindow(), fixturePinnedNow())
	if _, ok := cliproxyauth.AnthropicRateLimitHintFor(original); !ok {
		t.Fatal("precondition: capture should be readable")
	}

	// Routine OAuth token refresh: same account, new access token.
	refreshed := testAuthWithEmail(authID, "account-one@example.com")
	refreshed.Metadata["access_token"] = "refreshed-token"
	if _, err := manager.Update(ctx, refreshed); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	hint, ok := cliproxyauth.AnthropicRateLimitHintFor(refreshed)
	if !ok {
		t.Fatal("quota state must survive a routine token refresh")
	}
	if hint.Status != "allowed_warning" {
		t.Errorf("Status=%q want %q after refresh", hint.Status, "allowed_warning")
	}

	// Rotation to a different account under the same auth ID.
	rotated := testAuthWithEmail(authID, "account-two@example.com")
	if _, err := manager.Update(ctx, rotated); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if _, ok := cliproxyauth.AnthropicRateLimitHintFor(rotated); ok {
		t.Fatal("the previous account's quota must not be served after rotation")
	}

	// A capture still in flight when the rotation happened lands late, tagged
	// with the old account. It must not resurrect the old quota.
	RecordAnthropicRateLimit(original, realCapture429Rejected(), fixturePinnedNow().Add(time.Second))
	if _, ok := cliproxyauth.AnthropicRateLimitHintFor(rotated); ok {
		t.Fatal("a late capture from the previous account must not be served to the rotated auth")
	}

	// And the rotated account's own capture is served normally.
	RecordAnthropicRateLimit(rotated, realCapture200Allowed(), fixturePinnedNow().Add(2*time.Second))
	hint, ok = cliproxyauth.AnthropicRateLimitHintFor(rotated)
	if !ok {
		t.Fatal("the rotated account must see its own capture")
	}
	if hint.Status != "allowed" {
		t.Errorf("Status=%q want %q for the rotated account", hint.Status, "allowed")
	}
}

// TestRecordAnthropicRateLimit_RetentionBudget pins the per-capture budget.
// The hint is held per auth ID until that auth's next response or a scrub, so
// a single response must not be able to size it: the window-slug pattern's
// `\d+` admits unboundedly many distinct slugs, and nothing in the tree sets
// MaxResponseHeaderBytes / MaxHeaderListSize, leaving Go's 10 MiB defaults as
// the only ceiling on the producing response.
func TestRecordAnthropicRateLimit_RetentionBudget(t *testing.T) {
	authID := "auth-retention-budget"
	resetAnthropicHint(t, authID)

	headers := http.Header{}
	// Far more distinct windows than the budget allows, all matching the slug
	// pattern (1h, 2h, 3h, ...), each contributing two raw headers.
	for i := 1; i <= maxAnthropicWindows*4; i++ {
		slug := strconv.Itoa(i) + "h"
		headers.Set("Anthropic-Ratelimit-Unified-"+slug+"-Status", "allowed")
		headers.Set("Anthropic-Ratelimit-Unified-"+slug+"-Utilization", "0.5")
	}
	// An oversized value on a header that is within the count budget.
	headers.Set("Anthropic-Ratelimit-Unified-Status", strings.Repeat("x", maxAnthropicHeaderValLen*3))

	RecordAnthropicRateLimit(testAuth(authID), headers, fixturePinnedNow())

	hint, ok := cliproxyauth.GetAnthropicRateLimitHint(authID)
	if !ok {
		t.Fatal("GetAnthropicRateLimitHint() ok = false, want true")
	}
	if got := len(hint.RawHeaders); got > maxAnthropicRawHeaders {
		t.Errorf("len(RawHeaders) = %d, want <= %d", got, maxAnthropicRawHeaders)
	}
	if got := len(hint.Windows); got > maxAnthropicWindows {
		t.Errorf("len(Windows) = %d, want <= %d", got, maxAnthropicWindows)
	}
	for name, value := range hint.RawHeaders {
		if len(value) > maxAnthropicHeaderValLen {
			t.Errorf("RawHeaders[%q] length = %d, want <= %d", name, len(value), maxAnthropicHeaderValLen)
		}
	}
}

// TestRecordAnthropicRateLimit_BudgetDoesNotTruncateRealCaptures guards the
// budget against regressing into legitimate traffic: the real fixtures must
// round-trip completely, unaffected by the caps.
func TestRecordAnthropicRateLimit_BudgetDoesNotTruncateRealCaptures(t *testing.T) {
	for name, headers := range map[string]http.Header{
		"200-allowed": realCapture200Allowed(),
	} {
		t.Run(name, func(t *testing.T) {
			authID := "auth-budget-headroom-" + name
			resetAnthropicHint(t, authID)

			RecordAnthropicRateLimit(testAuth(authID), headers, fixturePinnedNow())

			hint, ok := cliproxyauth.GetAnthropicRateLimitHint(authID)
			if !ok {
				t.Fatal("GetAnthropicRateLimitHint() ok = false, want true")
			}
			var unified int
			for canonicalName := range headers {
				if strings.HasPrefix(strings.ToLower(canonicalName), anthropicRateLimitHeaderPrefix) {
					unified++
				}
			}
			if len(hint.RawHeaders) != unified {
				t.Errorf("len(RawHeaders) = %d, want %d — real capture was truncated by the budget", len(hint.RawHeaders), unified)
			}
		})
	}
}

// TestRecordAnthropicRateLimit_MalformedWindowDoesNotDisplaceCarried drives the
// content-free-window case end to end, through the parser that creates it.
//
// A slug is seeded as soon as any header names it, so a response whose only
// `7d_oi` header is an unparseable reset produces a zero-value entry for that
// window. If the hint store treated that as a reported window it would both
// destroy the rich reading captured moments earlier and — having no reset of
// its own — be ineligible to be carried by anything afterwards, so the premium
// window would stay gone until the next premium response.
func TestRecordAnthropicRateLimit_MalformedWindowDoesNotDisplaceCarried(t *testing.T) {
	const authID = "claude-malformed-window-carry@example.com"
	resetAnthropicHint(t, authID)
	auth := testAuth(authID)

	premiumAt := fixturePinnedNow()
	ordinaryAt := premiumAt.Add(4 * time.Minute)
	oiReset := premiumAt.Add(30 * time.Hour)

	premium := http.Header{
		"Anthropic-Ratelimit-Unified-Status":            {"allowed_warning"},
		"Anthropic-Ratelimit-Unified-5h-Status":         {"allowed"},
		"Anthropic-Ratelimit-Unified-5h-Reset":          {strconv.FormatInt(premiumAt.Add(2*time.Hour).Unix(), 10)},
		"Anthropic-Ratelimit-Unified-7d_oi-Status":      {"allowed_warning"},
		"Anthropic-Ratelimit-Unified-7d_oi-Reset":       {strconv.FormatInt(oiReset.Unix(), 10)},
		"Anthropic-Ratelimit-Unified-7d_oi-Utilization": {"0.88"},
	}
	RecordAnthropicRateLimit(auth, premium, premiumAt)

	// The next response mentions 7d_oi only through a header that cannot be
	// parsed, which is exactly how a zero-value window entry gets created.
	ordinary := http.Header{
		"Anthropic-Ratelimit-Unified-Status":      {"allowed"},
		"Anthropic-Ratelimit-Unified-5h-Status":   {"allowed"},
		"Anthropic-Ratelimit-Unified-5h-Reset":    {strconv.FormatInt(ordinaryAt.Add(2*time.Hour).Unix(), 10)},
		"Anthropic-Ratelimit-Unified-7d_oi-Reset": {"garbage"},
	}
	RecordAnthropicRateLimit(auth, ordinary, ordinaryAt)

	hint, ok := cliproxyauth.AnthropicRateLimitHintFor(auth)
	if !ok {
		t.Fatal("AnthropicRateLimitHintFor() ok = false, want true")
	}
	oi, ok := hint.Windows["7d_oi"]
	if !ok {
		t.Fatal("7d_oi missing after a capture whose only 7d_oi header was malformed")
	}
	if oi.Utilization != 0.88 || !oi.HasUtilization || oi.Status != "allowed_warning" {
		t.Errorf("7d_oi = %+v, want the premium capture's reading preserved", oi)
	}
	if !oi.Reset.Equal(oiReset) {
		t.Errorf("7d_oi reset = %v, want %v", oi.Reset, oiReset)
	}
	if !oi.ObservedAt.Equal(premiumAt) {
		t.Errorf("7d_oi ObservedAt = %v, want %v — the surviving reading is the carried one", oi.ObservedAt, premiumAt)
	}
	// The malformed header itself stays visible for forensics.
	if got := hint.RawHeaders["anthropic-ratelimit-unified-7d_oi-reset"]; got != "garbage" {
		t.Errorf("raw_headers lost the malformed value: %q", got)
	}
}
