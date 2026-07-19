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
	// Unknown window type should default to hourly.
	start, _ := windowFor("unknown", time.Now())
	if start.IsZero() {
		t.Fatal("windowFor default should return non-zero start")
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
