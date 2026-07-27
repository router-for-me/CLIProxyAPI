package policy

import (
	"context"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

func TestModelMatchesExact(t *testing.T) {
	if !modelMatches("gpt-4o", "gpt-4o") {
		t.Fatal("exact match should succeed")
	}
	if modelMatches("gpt-4o", "gpt-4o-mini") {
		t.Fatal("non-exact match without wildcard should fail")
	}
}

func TestModelMatchesWildcard(t *testing.T) {
	if !modelMatches("gpt-4*", "gpt-4o") {
		t.Errorf("gpt-4* should match gpt-4o")
	}
	if !modelMatches("gpt-4*", "gpt-4o-mini") {
		t.Errorf("gpt-4* should match gpt-4o-mini")
	}
	if modelMatches("gpt-4*", "claude-3") {
		t.Errorf("gpt-4* should not match claude-3")
	}
}

func TestAllowedEmptyPolicy(t *testing.T) {
	p := Policy{}
	if !Allowed(p, "any-model") {
		t.Fatal("empty policy should allow everything")
	}
}

func TestModelCoveredByAllowed(t *testing.T) {
	cases := []struct {
		name   string
		allow  []string
		model  string
		expect bool
	}{
		{"empty_allow_means_all", nil, "deepseek-v4-pro", true},
		{"exact_match", []string{"deepseek-v4-pro", "gpt-4o"}, "deepseek-v4-pro", true},
		{"not_in_list", []string{"deepseek-v4-pro", "gpt-4o"}, "claude-3", false},
		{"wildcard_covers", []string{"gpt-4*", "deepseek-v4-pro"}, "gpt-4o-mini", true},
		{"wildcard_no_match", []string{"gpt-4*"}, "claude-3", false},
		{"empty_model_rejected", []string{"gpt-4o"}, "", false},
		{"empty_model_rejected_when_all", nil, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ModelCoveredByAllowed(tc.allow, tc.model)
			if got != tc.expect {
				t.Fatalf("ModelCoveredByAllowed(%v, %q) = %v, want %v", tc.allow, tc.model, got, tc.expect)
			}
		})
	}
}

func TestAllowedWhitelist(t *testing.T) {
	p := Policy{AllowedModels: []string{"gpt-4o", "claude-3"}}
	if !Allowed(p, "gpt-4o") {
		t.Error("whitelisted model should be allowed")
	}
	if Allowed(p, "gemini-pro") {
		t.Error("non-whitelisted model should be blocked")
	}
}

func TestAllowedBlacklistTakesPrecedence(t *testing.T) {
	p := Policy{
		AllowedModels: []string{"gpt-4o", "claude-3"},
		BlockedModels: []string{"claude-*"},
	}
	if !Allowed(p, "gpt-4o") {
		t.Error("gpt-4o should be allowed")
	}
	if Allowed(p, "claude-3") {
		t.Error("claude-3 should be blocked despite whitelist")
	}
}

func TestAllowedEmptyModelDefers(t *testing.T) {
	p := Policy{AllowedModels: []string{"gpt-4o"}}
	// No model context: defer (return true so middleware can re-evaluate).
	if !Allowed(p, "") {
		t.Fatal("empty model should defer (return true)")
	}
}

func TestModelVisible(t *testing.T) {
	cases := []struct {
		name    string
		allowed []string
		blocked []string
		model   string
		expect  bool
	}{
		{"both_empty_means_all", nil, nil, "deepseek-v4-pro", true},
		{"allowed_exact_match", []string{"gpt-4o", "claude-3"}, nil, "gpt-4o", true},
		{"allowed_no_match", []string{"gpt-4o"}, nil, "claude-3", false},
		{"wildcard_match", []string{"gpt-4*"}, nil, "gpt-4o-mini", true},
		{"wildcard_no_match", []string{"gpt-4*"}, nil, "claude-3", false},
		{"blocked_takes_precedence", []string{"gpt-4o"}, []string{"gpt-*"}, "gpt-4o", false},
		{"blocked_wildcard_drops_only_matching", []string{}, []string{"gpt-*"}, "claude-3", true},
		{"empty_model_kept_visible", nil, nil, "", true},
		{"empty_model_kept_visible_even_with_blocked", nil, []string{"gpt-*"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ModelVisible(tc.allowed, tc.blocked, tc.model)
			if got != tc.expect {
				t.Fatalf("ModelVisible(allowed=%v, blocked=%v, %q) = %v, want %v",
					tc.allowed, tc.blocked, tc.model, got, tc.expect)
			}
		})
	}
}

func TestSlidingWindowAllowAndIncrement(t *testing.T) {
	w := newSlidingWindow()
	key := "k1"
	// Limit=2 should allow 2 then deny.
	if !w.AllowAndIncrement(key, 2, time.Minute) {
		t.Fatal("first request should be allowed")
	}
	if !w.AllowAndIncrement(key, 2, time.Minute) {
		t.Fatal("second request should be allowed")
	}
	if w.AllowAndIncrement(key, 2, time.Minute) {
		t.Fatal("third request should be denied")
	}
}

func TestSlidingWindowZeroLimitAllows(t *testing.T) {
	w := newSlidingWindow()
	// limit <= 0 means "no throttling".
	for i := 0; i < 10; i++ {
		if !w.AllowAndIncrement("k", 0, time.Minute) {
			t.Fatalf("request %d should be allowed with 0 limit", i)
		}
	}
}

func TestSlidingWindowNewBucketResets(t *testing.T) {
	w := newSlidingWindow()
	// Manually inject a stale entry from "last hour".
	bucket := time.Now().Truncate(time.Hour).Add(-time.Hour)
	w.counters["k"] = &windowEntry{windowStart: bucket, count: 100}
	// A new AllowAndIncrement should observe a fresh bucket.
	if !w.AllowAndIncrement("k", 1, time.Hour) {
		t.Fatal("stale bucket should be replaced, allowing the request")
	}
}

func TestSlidingWindowForgetAndReset(t *testing.T) {
	w := newSlidingWindow()
	_ = w.AllowAndIncrement("a", 5, time.Minute)
	_ = w.AllowAndIncrement("b", 5, time.Minute)
	w.Forget("a")
	if w.SnapshotCurrent("a", time.Minute) != 0 {
		t.Fatal("Forget should reset the counter for that key")
	}
	if w.SnapshotCurrent("b", time.Minute) != 1 {
		t.Fatal("Forget should not affect other keys")
	}
	w.Reset()
	if w.SnapshotCurrent("b", time.Minute) != 0 {
		t.Fatal("Reset should drop all counters")
	}
}

func TestSlidingWindowCleanup(t *testing.T) {
	w := newSlidingWindow()
	now := time.Now().Truncate(time.Minute)
	// Inject two entries: one fresh, one old.
	w.counters["fresh"] = &windowEntry{windowStart: now, count: 5}
	w.counters["old"] = &windowEntry{windowStart: now.Add(-10 * time.Minute), count: 99}
	w.Cleanup(5 * time.Minute)
	if _, ok := w.counters["fresh"]; !ok {
		t.Error("Cleanup should retain fresh entries")
	}
	if _, ok := w.counters["old"]; ok {
		t.Error("Cleanup should drop stale entries")
	}
}

func TestHourlyWindow(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 34, 56, 0, time.UTC)
	start, end := hourlyWindow(now)
	if start != time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC) {
		t.Errorf("hourly start = %v; want 12:00:00", start)
	}
	if end != start.Add(time.Hour) {
		t.Errorf("hourly end = %v; want start+1h", end)
	}
}

func TestWeeklyWindowMondayAnchor(t *testing.T) {
	// 2026-07-19 is a Sunday. Week should start Monday 2026-07-13.
	now := time.Date(2026, 7, 19, 23, 59, 0, 0, time.UTC)
	start, end := weeklyWindow(now)
	wantStart := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	if !start.Equal(wantStart) {
		t.Errorf("weekly start = %v; want %v", start, wantStart)
	}
	if !end.Equal(wantStart.Add(7 * 24 * time.Hour)) {
		t.Errorf("weekly end = %v; want start+7d", end)
	}
}

func TestMonthlyWindow(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	start, end := monthlyWindow(now)
	if start != time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC) {
		t.Errorf("monthly start = %v; want 2026-07-01", start)
	}
	if end != time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC) {
		t.Errorf("monthly end = %v; want 2026-08-01", end)
	}
}

func TestWindowForDefault(t *testing.T) {
	// Unknown window type should default to hourly. Anchor is the zero value
	// here, so windowFor falls back to the calendar-aligned hourlyWindow.
	start, _ := windowFor("unknown", time.Time{}, time.Now())
	if start.IsZero() {
		t.Fatal("windowFor default should return non-zero start")
	}
}

func TestAnchoredWindowHourly(t *testing.T) {
	// Anchor is the API key's created_at; windows roll in 1h increments from
	// that instant rather than snapping to the top of the hour.
	anchor := time.Date(2026, 7, 19, 12, 34, 56, 0, time.UTC)
	// now falls in the first window (less than 1h after anchor).
	now := time.Date(2026, 7, 19, 13, 0, 0, 0, time.UTC)
	start, end := windowFor("hourly", anchor, now)
	wantStart := anchor
	wantEnd := anchor.Add(time.Hour)
	if !start.Equal(wantStart) {
		t.Errorf("anchored hourly start = %v; want %v", start, wantStart)
	}
	if !end.Equal(wantEnd) {
		t.Errorf("anchored hourly end = %v; want %v", end, wantEnd)
	}

	// now = anchor + 1h2m lands in the SECOND window [anchor+1h, anchor+2h).
	now2 := anchor.Add(time.Hour + 2*time.Minute)
	start2, end2 := windowFor("hourly", anchor, now2)
	wantStart2 := anchor.Add(time.Hour)
	if !start2.Equal(wantStart2) {
		t.Errorf("anchored hourly[1] start = %v; want %v", start2, wantStart2)
	}
	if !end2.Equal(anchor.Add(2 * time.Hour)) {
		t.Errorf("anchored hourly[1] end = %v; want %v", end2, anchor.Add(2*time.Hour))
	}
}

func TestAnchoredWindowWeekly(t *testing.T) {
	anchor := time.Date(2026, 7, 19, 12, 34, 56, 0, time.UTC)
	// now = anchor + 8d → lands in window #1 [anchor+7d, anchor+14d).
	now := anchor.Add(8 * 24 * time.Hour)
	start, end := windowFor("weekly", anchor, now)
	wantStart := anchor.Add(7 * 24 * time.Hour)
	wantEnd := anchor.Add(14 * 24 * time.Hour)
	if !start.Equal(wantStart) {
		t.Errorf("anchored weekly start = %v; want %v", start, wantStart)
	}
	if !end.Equal(wantEnd) {
		t.Errorf("anchored weekly end = %v; want %v", end, wantEnd)
	}
}

func TestAnchoredWindowMonthly(t *testing.T) {
	anchor := time.Date(2026, 7, 19, 12, 34, 56, 0, time.UTC)
	// Monthly is fixed 30 days from anchor (not a calendar month).
	// now = anchor + 31d → window #1 [anchor+30d, anchor+60d).
	now := anchor.Add(31 * 24 * time.Hour)
	start, end := windowFor("monthly", anchor, now)
	wantStart := anchor.Add(30 * 24 * time.Hour)
	wantEnd := anchor.Add(60 * 24 * time.Hour)
	if !start.Equal(wantStart) {
		t.Errorf("anchored monthly start = %v; want %v", start, wantStart)
	}
	if !end.Equal(wantEnd) {
		t.Errorf("anchored monthly end = %v; want %v", end, wantEnd)
	}
}

func TestAnchoredWindowZeroAnchorFallsBack(t *testing.T) {
	// Zero anchor must fall back to calendar alignment rather than panic.
	now := time.Date(2026, 7, 19, 12, 34, 56, 0, time.UTC)
	start, end := windowFor("hourly", time.Time{}, now)
	if !start.Equal(now.UTC().Truncate(time.Hour)) {
		t.Errorf("zero-anchor hourly start = %v; want %v", start, now.UTC().Truncate(time.Hour))
	}
	if !end.Equal(start.Add(time.Hour)) {
		t.Errorf("zero-anchor hourly end = %v; want start+1h", end)
	}
}

func TestAnchoredWindowClockSkewClampsToFirstWindow(t *testing.T) {
	// now earlier than anchor (clock skew) should clamp to the first window.
	anchor := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	now := anchor.Add(-5 * time.Minute)
	start, end := windowFor("hourly", anchor, now)
	if !start.Equal(anchor) {
		t.Errorf("skew hourly start = %v; want anchor %v", start, anchor)
	}
	if !end.Equal(anchor.Add(time.Hour)) {
		t.Errorf("skew hourly end = %v; want anchor+1h", end)
	}
}

func TestSafePrefixMasksSecret(t *testing.T) {
	out := safePrefix("cpa_supersecret-abcdef")
	// Should expose 4 chars + "..." but never the full secret.
	if len(out) > 10 {
		t.Errorf("safePrefix too long: %q", out)
	}
	if out == "cpa_supersecret-abcdef" {
		t.Errorf("safePrefix leaked the secret: %q", out)
	}
	if safePrefix("ab") != "**" {
		t.Errorf("safePrefix should fully mask short inputs")
	}
}

func TestFromStorePolicyPreservesFields(t *testing.T) {
	rpm := 60
	monthly := 100.0
	sp := storePolicy(rpm, monthly)
	p := fromStorePolicy(sp)
	if *p.RPMLimit != rpm {
		t.Errorf("RPM limit lost: %v", p.RPMLimit)
	}
	if *p.BudgetMonthlyUSD != monthly {
		t.Errorf("monthly budget lost: %v", p.BudgetMonthlyUSD)
	}
}

// storePolicy is a tiny builder used to keep test assertions concise.
func storePolicy(rpm int, monthly float64) store.Policy {
	return store.Policy{
		APIKeyID:         "k1",
		RPMLimit:         &rpm,
		BudgetMonthlyUSD: &monthly,
	}
}

// TestComputeCostFromTokensMirrorsStoreComputeCost guards the budget-window
// cost computation against drift from the flusher's persisted cost_usd. Before
// the fix, Consume always wrote Cost=0 to usage_windows.cost_usd — so the
// dashboard's "Budget Windows" card showed every window at $0 and budget caps
// never fired. computeCostFromTokens must equal store.ComputeCost for the same
// inputs (mirror of the flusher's formula).
func TestComputeCostFromTokensMirrorsStoreComputeCost(t *testing.T) {
	p := store.Pricing{
		InputPer1M: 5.0, OutputPer1M: 15.0, ReasoningPer1M: 10.0,
		CachedInputPer1M: 1.25, CachedReadPer1M: 0.5,
	}
	tokens := TokenCounts{
		Input: 600, Output: 200, Reasoning: 50,
		Cached: 400, CacheCreation: 300,
	}
	got := computeCostFromTokens(p, tokens)
	want := store.ComputeCost(p, tokens.Input, tokens.Output, tokens.Reasoning, tokens.CacheCreation, tokens.Cached)
	if got != want {
		t.Fatalf("computeCostFromTokens = %v, want store.ComputeCost = %v", got, want)
	}
	if got <= 0 {
		t.Fatalf("cost must be > 0 with non-zero tokens + pricing; got %v (regression: budget windows would stay at $0)", got)
	}
}

// TestComputeCostFromTokensZeroPricingIsZero documents the fail-safe: missing
// pricing ⇒ zero cost. checkBudget's `cap > 0` guard then ensures windows with
// no pricing configured do not degenerate into always-reject.
func TestComputeCostFromTokensZeroPricingIsZero(t *testing.T) {
	tokens := TokenCounts{Input: 1000, Output: 500, Cached: 200, CacheCreation: 100}
	if got := computeCostFromTokens(store.Pricing{}, tokens); got != 0 {
		t.Fatalf("zero pricing must yield zero cost; got %v", got)
	}
}

// TestUsagePluginForwardsCacheCreationToConsume guards the propagation of
// CacheCreationTokens from the in-memory Record into TokenCounts. Pre-fix the
// plugin populated Cached but not CacheCreation, so cache-write tokens were
// silently dropped from budget cost attribution (Anthropic cache-creation is
// the dominant cost driver for cache-heavy workloads). This test does not need
// a live policy service: it stubs a minimal PolicyService that records the
// tokens it received.
type recordingService struct {
	got   TokenCounts
	calls int
}

func (r *recordingService) Active() bool { return true }
func (r *recordingService) Check(_ context.Context, _, _ string) (Decision, error) {
	return Decision{Allow: true}, nil
}
func (r *recordingService) Consume(_ context.Context, _, _ string, tokens TokenCounts) error {
	r.got = tokens
	r.calls++
	return nil
}
func (r *recordingService) InvalidateKey(_ context.Context, _ string) error { return nil }
func (r *recordingService) InvalidateAll()                                  {}
func (r *recordingService) AcquireParallel(_ context.Context, _ string) (bool, error) {
	return true, nil
}
func (r *recordingService) ReleaseParallel(_ context.Context, _ string) error { return nil }
func (r *recordingService) ResolvedRoutes(_ context.Context, _ string) []store.ModelRoute {
	return nil
}

func (r *recordingService) ResolvedModelLists(_ context.Context, _ string) ([]string, []string) {
	return nil, nil
}

func TestUsagePluginForwardsCacheTokens(t *testing.T) {
	rec := &recordingService{}
	plugin := NewUsagePlugin(rec)
	if plugin == nil {
		t.Fatal("NewUsagePlugin() = nil for non-nil svc")
	}
	record := coreusage.Record{
		APIKey: "cpa_test",
		Model:  "claude-test",
		Detail: coreusage.Detail{
			InputTokens:         500,
			OutputTokens:        200,
			ReasoningTokens:     50,
			CachedTokens:        400, // cache-read
			CacheCreationTokens: 300, // cache-write
		},
	}
	plugin.HandleUsage(context.Background(), record)
	if rec.calls != 1 {
		t.Fatalf("Consume calls = %d, want 1", rec.calls)
	}
	if rec.got.Cached != 400 {
		t.Errorf("Cached = %d, want 400 (cache-read must propagate)", rec.got.Cached)
	}
	if rec.got.CacheCreation != 300 {
		t.Errorf("CacheCreation = %d, want 300 (cache-write must propagate so computeCostFromTokens bills the write surcharge)", rec.got.CacheCreation)
	}
	if rec.got.Input != 500 || rec.got.Output != 200 || rec.got.Reasoning != 50 {
		t.Errorf("token counters lost: %+v", rec.got)
	}
	// Pre-fix the plugin left Cost at zero intentionally; Consume recomputes it
	// from pricing on its side. Assert that contract here so a future change
	// does not silently start double-billing.
	if rec.got.Cost != 0 {
		t.Errorf("Cost = %v, want 0 (plugin must defer cost computation to Consume/ComputeCost)", rec.got.Cost)
	}
}

func TestUsagePluginNoopWhenInactive(t *testing.T) {
	// When the policy service reports Active()==false the plugin must short-
	// circuit without invoking Consume. Construct with a nil svc, which yields
	// a nil plugin (no-op at registration).
	if got := NewUsagePlugin(nil); got != nil {
		t.Fatalf("NewUsagePlugin(nil) = %v, want nil (inactive services must produce no-op plugin)", got)
	}
	// And a plugin with an active service must still call Consume (sanity).
	rec := &recordingService{}
	plugin := NewUsagePlugin(rec)
	if plugin == nil {
		t.Fatal("plugin must not be nil for an active service")
	}
	plugin.HandleUsage(context.Background(), coreusage.Record{APIKey: "cpa_x"})
	if rec.calls != 1 {
		t.Fatalf("expected one Consume call for active service; got %d", rec.calls)
	}
}
