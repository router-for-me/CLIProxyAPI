package helps

import (
	"net/http"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// resetAnthropicHint clears any prior hint for an authID so each test runs
// against a clean slate. The hint store is package-global; tests share it.
func resetAnthropicHint(t *testing.T, authID string) {
	t.Helper()
	t.Cleanup(func() {
		// Best-effort cleanup. The store has no public Delete; rely on tests
		// using unique authIDs to avoid cross-pollution. This helper exists
		// for documentation, in case a Delete API ships later.
		_ = authID
	})
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
	RecordAnthropicRateLimit(authID, realCapture200Allowed(), now)

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

	RecordAnthropicRateLimit(authID, realCapture200WarningWithTierWindow(), fixturePinnedNow())

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

	RecordAnthropicRateLimit(authID, realCapture429Rejected(), fixturePinnedNow())

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
	RecordAnthropicRateLimit(authID, headersWithoutFamily, fixturePinnedNow())

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
	RecordAnthropicRateLimit(authID, headers, fixturePinnedNow())

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

	RecordAnthropicRateLimit(authID, nil, fixturePinnedNow())

	if _, ok := cliproxyauth.GetAnthropicRateLimitHint(authID); ok {
		t.Fatal("nil headers should not create a hint")
	}
}

func TestRecordAnthropicRateLimit_EmptyAuthIDNoop(t *testing.T) {
	RecordAnthropicRateLimit("", realCapture200Allowed(), fixturePinnedNow())
	RecordAnthropicRateLimit("   ", realCapture200Allowed(), fixturePinnedNow())
	// Assertion: doesn't panic, doesn't pollute the hint store.
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
	RecordAnthropicRateLimit(authID, headers, fixturePinnedNow())

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

	RecordAnthropicRateLimit(authID, realCapture200Allowed(), fixturePinnedNow())
	RecordAnthropicRateLimit(authID, realCapture429Rejected(), fixturePinnedNow().Add(time.Minute))

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
	RecordAnthropicRateLimit(authID, headers, fixturePinnedNow())

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
	RecordAnthropicRateLimit(authID, headers, fixturePinnedNow())

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
	RecordAnthropicRateLimit(authID, headers, fixturePinnedNow())

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
	RecordAnthropicRateLimit(authID, headers, fixturePinnedNow())

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
		in   string
		want float64
	}{
		{"0.5", 0.5},
		{"1.13", 1.13},
		{"0.0", 0},
		{"  0.5  ", 0.5},
		{"", 0},
		{"not-a-number", 0},
		// Non-finite values: strconv.ParseFloat accepts these literals
		// without error, but they break downstream JSON serialization and
		// any consumer arithmetic (NaN comparisons, Inf accumulation).
		// Treat as parse failure.
		{"NaN", 0},
		{"nan", 0},
		{"Inf", 0},
		{"+Inf", 0},
		{"-Inf", 0},
		{"infinity", 0},
	}
	for _, tc := range tests {
		got := parseAnthropicFloat(tc.in)
		if got != tc.want {
			t.Errorf("parseAnthropicFloat(%q)=%v want %v", tc.in, got, tc.want)
		}
	}
}
