package observability

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestShortContextRateCatalog(t *testing.T) {
	want := map[string]modelRates{
		"gpt-5.6-sol":    {input: 5, cacheRead: 0.5, cacheWrite: 6.25, output: 30},
		"gpt-5.6-terra":  {input: 2.5, cacheRead: 0.25, cacheWrite: 3.125, output: 15},
		"gpt-5.6-luna":   {input: 1, cacheRead: 0.1, cacheWrite: 1.25, output: 6},
		"claude-fable-5": {input: 10, cacheRead: 1, cacheWrite: 12.5, output: 50},
	}
	for model, wantRates := range want {
		if got := shortContextRates[model]; got != wantRates {
			t.Fatalf("rates for %s = %+v, want %+v", model, got, wantRates)
		}
	}
}

func TestLongContextRateCatalog(t *testing.T) {
	want := map[string]modelRates{
		"gpt-5.6-sol":   {input: 10, cacheRead: 1, cacheWrite: 12.5, output: 45},
		"gpt-5.6-terra": {input: 5, cacheRead: 0.5, cacheWrite: 6.25, output: 22.5},
		"gpt-5.6-luna":  {input: 2, cacheRead: 0.2, cacheWrite: 2.5, output: 9},
	}
	for model, wantRates := range want {
		if got := longContextRates[model]; got != wantRates {
			t.Fatalf("long-context rates for %s = %+v, want %+v", model, got, wantRates)
		}
	}
}

func TestTrackerNormalizesClaudeAndCodexInputAndEstimatedCost(t *testing.T) {
	tests := []struct {
		name      string
		record    usage.Record
		wantInput int64
		wantCost  float64
	}{
		{
			name: "claude input excludes cache buckets upstream",
			record: usage.Record{
				Provider: "claude",
				Model:    "claude-fable-5",
				Detail: usage.Detail{
					InputTokens:           100,
					OutputTokens:          5,
					CacheReadTokens:       20,
					CacheCreationTokens:   10,
					CacheCreation5mTokens: 10,
					CacheTelemetryPresent: true,
				},
			},
			wantInput: 130,
			wantCost:  0.001395,
		},
		{
			name: "codex input already includes cache buckets upstream",
			record: usage.Record{
				Provider: "codex",
				Model:    "gpt-5.6-sol(xhigh)",
				Detail: usage.Detail{
					InputTokens:           100,
					OutputTokens:          5,
					CacheReadTokens:       20,
					CacheCreationTokens:   10,
					CacheTelemetryPresent: true,
				},
			},
			wantInput: 100,
			wantCost:  0.0005725,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := NewTracker(5)
			tracker.HandleUsage(context.Background(), tt.record)
			snapshot := tracker.Snapshot()
			if snapshot.InputTokens != tt.wantInput {
				t.Fatalf("input tokens = %d, want %d", snapshot.InputTokens, tt.wantInput)
			}
			if math.Abs(snapshot.EstimatedCostUSD-tt.wantCost) > 1e-12 {
				t.Fatalf("estimated cost = %.12f, want %.12f", snapshot.EstimatedCostUSD, tt.wantCost)
			}
			if snapshot.PricedRequests != 1 || snapshot.UnpricedRequests != 0 {
				t.Fatalf("priced/unpriced = %d/%d, want 1/0", snapshot.PricedRequests, snapshot.UnpricedRequests)
			}
		})
	}
}

func TestTrackerExposesEffortAndCostComponents(t *testing.T) {
	tracker := NewTracker(2)
	tracker.HandleUsage(context.Background(), usage.Record{
		Provider:        "codex",
		Model:           "gpt-5.6-sol",
		ReasoningEffort: "XHIGH",
		Detail: usage.Detail{
			InputTokens:           100,
			OutputTokens:          5,
			CacheReadTokens:       20,
			CacheCreationTokens:   10,
			CacheTelemetryPresent: true,
		},
	})
	tracker.HandleUsage(context.Background(), usage.Record{
		Provider:        "claude",
		Model:           "claude-fable-5",
		ReasoningEffort: "high",
		Detail: usage.Detail{
			InputTokens:           100,
			OutputTokens:          5,
			CacheReadTokens:       20,
			CacheCreationTokens:   10,
			CacheCreation5mTokens: 10,
			CacheTelemetryPresent: true,
		},
	})

	events := tracker.Snapshot().RecentEvents
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	codex := events[0]
	if codex.ReasoningEffort != "xhigh" {
		t.Fatalf("Codex effort = %q, want xhigh", codex.ReasoningEffort)
	}
	// This record has a provider-confirmed write: 70 ordinary input tokens, 20
	// discounted reads, and 10 write-priced tokens.
	for label, values := range map[string][2]float64{
		"Codex input":  {codex.EstimatedInputCostUSD, 0.00035},
		"Codex output": {codex.EstimatedOutputCostUSD, 0.00015},
		"Codex cache":  {codex.EstimatedCacheCostUSD, 0.0000725},
		"Codex total":  {codex.EstimatedCostUSD, 0.0005725},
	} {
		if math.Abs(values[0]-values[1]) > 1e-12 {
			t.Fatalf("%s cost = %.12f, want %.12f", label, values[0], values[1])
		}
	}

	claude := events[1]
	for label, values := range map[string][2]float64{
		"Claude input":  {claude.EstimatedInputCostUSD, 0.001},
		"Claude output": {claude.EstimatedOutputCostUSD, 0.00025},
		"Claude cache":  {claude.EstimatedCacheCostUSD, 0.000145},
		"Claude total":  {claude.EstimatedCostUSD, 0.001395},
	} {
		if math.Abs(values[0]-values[1]) > 1e-12 {
			t.Fatalf("%s cost = %.12f, want %.12f", label, values[0], values[1])
		}
	}
}

func TestTrackerUsesEstimatedCodexCacheWriteForDisplayOnly(t *testing.T) {
	tracker := NewTracker(1)
	tracker.HandleUsage(context.Background(), usage.Record{
		Provider: "codex",
		Model:    "gpt-5.6-sol",
		Detail: usage.Detail{
			InputTokens:                    100,
			OutputTokens:                   5,
			CacheReadTokens:                20,
			EstimatedCacheCreationTokens:   80,
			CacheCreationEstimateAvailable: true,
			CacheTelemetryPresent:          true,
		},
	})

	event := tracker.Snapshot().RecentEvents[0]
	if event.CacheWriteTokens != 80 || !event.CacheWriteEstimated {
		t.Fatalf("cache write tokens/estimated = %d/%t, want 80/true", event.CacheWriteTokens, event.CacheWriteEstimated)
	}
	// The estimated write remains ordinary uncached OpenAI input for pricing.
	if got, want := event.EstimatedInputCostUSD, 0.0004; math.Abs(got-want) > 1e-12 {
		t.Fatalf("input cost = %.12f, want %.12f", got, want)
	}
	if got, want := event.EstimatedCacheCostUSD, 0.00001; math.Abs(got-want) > 1e-12 {
		t.Fatalf("cache cost = %.12f, want %.12f", got, want)
	}
	if got, want := event.EstimatedCostUSD, 0.00056; math.Abs(got-want) > 1e-12 {
		t.Fatalf("total cost = %.12f, want %.12f", got, want)
	}
}

func TestTrackerDistinguishesCacheMissFromMissingTelemetry(t *testing.T) {
	tracker := NewTracker(5)
	tracker.HandleUsage(context.Background(), usage.Record{
		Provider: "codex",
		Model:    "gpt-5.6-terra",
		Detail: usage.Detail{
			InputTokens:           10,
			CacheTelemetryPresent: true,
		},
	})
	tracker.HandleUsage(context.Background(), usage.Record{
		Provider: "other",
		Model:    "unpriced-model",
		Detail:   usage.Detail{InputTokens: 10},
	})

	snapshot := tracker.Snapshot()
	if snapshot.CacheMisses != 1 || snapshot.CacheUnknown != 1 || snapshot.CacheHits != 0 {
		t.Fatalf("cache hit/miss/unknown = %d/%d/%d, want 0/1/1", snapshot.CacheHits, snapshot.CacheMisses, snapshot.CacheUnknown)
	}
	if !snapshot.RecentEvents[0].CacheMiss || snapshot.RecentEvents[0].CacheOutcome != CacheOutcomeMiss {
		t.Fatalf("explicit zero cache telemetry = %+v, want miss", snapshot.RecentEvents[0])
	}
	if snapshot.RecentEvents[1].CacheMiss || snapshot.RecentEvents[1].CacheOutcome != CacheOutcomeUnknown {
		t.Fatalf("omitted cache telemetry = %+v, want unknown", snapshot.RecentEvents[1])
	}
}

func TestTrackerReportsLowCacheReuseWithoutChangingHitMissSemantics(t *testing.T) {
	tracker := NewTracker(4)
	tracker.HandleUsage(context.Background(), usage.Record{
		Provider: "codex",
		Model:    "gpt-5.6-sol",
		Detail: usage.Detail{
			InputTokens:           300_000,
			CacheReadTokens:       17_920,
			CacheTelemetryPresent: true,
		},
	})
	tracker.HandleUsage(context.Background(), usage.Record{
		Provider: "claude",
		Model:    "claude-fable-5",
		Detail: usage.Detail{
			InputTokens:           20_000,
			CacheReadTokens:       80_000,
			CacheTelemetryPresent: true,
		},
	})

	snapshot := tracker.Snapshot()
	if snapshot.CacheHits != 2 || snapshot.CacheMisses != 0 || snapshot.CacheLowReuseRequests != 1 {
		t.Fatalf("cache hits/misses/low-reuse = %d/%d/%d, want 2/0/1", snapshot.CacheHits, snapshot.CacheMisses, snapshot.CacheLowReuseRequests)
	}
	lowReuse := snapshot.RecentEvents[0]
	if lowReuse.CacheOutcome != CacheOutcomeHit || lowReuse.CacheMiss || !lowReuse.CacheLowReuse {
		t.Fatalf("low-reuse event classification = %+v, want hit + low reuse without strict miss", lowReuse)
	}
	if lowReuse.UncachedInputTokens != 282_080 {
		t.Fatalf("uncached input = %d, want 282080", lowReuse.UncachedInputTokens)
	}
	if got, want := lowReuse.CacheReadPercent, float64(17_920)*100/300_000; math.Abs(got-want) > 1e-12 {
		t.Fatalf("cache read percent = %.12f, want %.12f", got, want)
	}
	healthy := snapshot.RecentEvents[1]
	if healthy.InputTokens != 100_000 || healthy.UncachedInputTokens != 20_000 || healthy.CacheLowReuse {
		t.Fatalf("healthy Claude cache coverage = %+v, want 100K total, 20K uncached, not low reuse", healthy)
	}
}

func TestTrackerCountsStrictCacheMissAsLowReuse(t *testing.T) {
	tracker := NewTracker(1)
	tracker.HandleUsage(context.Background(), usage.Record{
		Provider: "codex",
		Model:    "gpt-5.6-sol",
		Detail: usage.Detail{
			InputTokens:           10_000,
			CacheTelemetryPresent: true,
		},
	})

	snapshot := tracker.Snapshot()
	if snapshot.CacheMisses != 1 || snapshot.CacheLowReuseRequests != 1 {
		t.Fatalf("cache misses/low reuse = %d/%d, want 1/1", snapshot.CacheMisses, snapshot.CacheLowReuseRequests)
	}
	if !snapshot.RecentEvents[0].CacheMiss || !snapshot.RecentEvents[0].CacheLowReuse {
		t.Fatalf("strict miss event = %+v, want miss and low reuse", snapshot.RecentEvents[0])
	}
}

func TestTrackerRecordsSafeCompactionResetWithoutChangingRequestTotals(t *testing.T) {
	tracker := NewTracker(2)
	tracker.HandleUsage(context.Background(), usage.Record{
		Provider: "codex",
		Model:    "gpt-5.6-sol",
		Detail:   usage.Detail{InputTokens: 10},
	})
	tracker.RecordCompactionReset(NewCompactionResetDiagnostic(
		"codex",
		"gpt-5.6-sol",
		"Request envelope changed!",
		"session-secret:model:auth-secret",
		"agent-secret",
		"previous-envelope-secret",
		"new-envelope-secret",
	))

	snapshot := tracker.Snapshot()
	if snapshot.Requests != 1 || snapshot.CompactionAttempts != 0 || snapshot.Compactions != 0 || snapshot.CompactionResets != 1 {
		t.Fatalf("requests/attempts/compactions/resets = %d/%d/%d/%d, want 1/0/0/1", snapshot.Requests, snapshot.CompactionAttempts, snapshot.Compactions, snapshot.CompactionResets)
	}
	if snapshot.CacheUnknown != 1 || snapshot.PricedRequests != 1 || snapshot.UnpricedRequests != 0 {
		t.Fatalf("reset altered cache/pricing totals: %+v", snapshot)
	}
	if len(snapshot.RecentEvents) != 2 || snapshot.RecentEvents[1].Operation != operationCompactionReset {
		t.Fatalf("recent events = %+v, want request followed by compaction reset", snapshot.RecentEvents)
	}
	reset := snapshot.RecentEvents[1]
	if reset.ResetReason != "request_envelope_changed" {
		t.Fatalf("reset reason = %q, want request_envelope_changed", reset.ResetReason)
	}
	serialized := strings.Join([]string{reset.LaneID, reset.AgentID, reset.PreviousEnvelopeID, reset.EnvelopeID}, " ")
	for _, secret := range []string{"session-secret", "auth-secret", "agent-secret", "previous-envelope-secret", "new-envelope-secret"} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("safe diagnostic IDs %q expose %q", serialized, secret)
		}
	}
	for name, value := range map[string]string{
		"lane": reset.LaneID, "agent": reset.AgentID, "previous envelope": reset.PreviousEnvelopeID, "envelope": reset.EnvelopeID,
	} {
		if len(value) != 16 {
			t.Fatalf("%s fingerprint length = %d (%q), want 16", name, len(value), value)
		}
	}
}

func TestTrackerDoesNotTreatClaudeCacheCreationAsReadHit(t *testing.T) {
	tracker := NewTracker(1)
	tracker.HandleUsage(context.Background(), usage.Record{
		Provider: "claude",
		Model:    "claude-fable-5",
		Detail: usage.Detail{
			InputTokens:           100,
			OutputTokens:          5,
			CachedTokens:          10, // Claude parser's legacy aggregate fallback.
			CacheCreationTokens:   10,
			CacheCreation5mTokens: 10,
			CacheTelemetryPresent: true,
		},
	})

	snapshot := tracker.Snapshot()
	if snapshot.InputTokens != 110 || snapshot.CacheMisses != 1 || snapshot.CacheHits != 0 {
		t.Fatalf("input/hit/miss = %d/%d/%d, want 110/0/1", snapshot.InputTokens, snapshot.CacheHits, snapshot.CacheMisses)
	}
	if got, want := snapshot.EstimatedCostUSD, 0.001375; math.Abs(got-want) > 1e-12 {
		t.Fatalf("estimated cost = %.12f, want %.12f", got, want)
	}
}

func TestTrackerPricesFableCacheCreationByTTL(t *testing.T) {
	tracker := NewTracker(1)
	tracker.HandleUsage(context.Background(), usage.Record{
		Provider: "claude",
		Model:    "claude-fable-5",
		Detail: usage.Detail{
			InputTokens:           100,
			OutputTokens:          5,
			CacheReadTokens:       20,
			CacheCreationTokens:   30,
			CacheCreation5mTokens: 10,
			CacheCreation1hTokens: 20,
			CacheTelemetryPresent: true,
		},
	})

	snapshot := tracker.Snapshot()
	if got, want := snapshot.EstimatedCostUSD, 0.001795; math.Abs(got-want) > 1e-12 {
		t.Fatalf("estimated cost = %.12f, want %.12f", got, want)
	}
}

func TestTrackerPricesUnknownFableCacheCreationRemainderAtOneHourRate(t *testing.T) {
	record := usage.Record{
		Provider: "claude",
		Model:    "claude-fable-5",
		Detail: usage.Detail{
			CacheCreationTokens:   30,
			CacheCreation5mTokens: 10,
			CacheCreation1hTokens: 10,
		},
	}
	cost, _, _, ok := estimatedCost(record, 0, 30)
	if !ok {
		t.Fatal("estimatedCost() unavailable, want conservative estimate")
	}
	// 10 five-minute tokens at $12.50 plus 10 known and 10 unknown tokens
	// at the one-hour $20 rate.
	if got, want := cost, 0.000525; math.Abs(got-want) > 1e-12 {
		t.Fatalf("estimated cost = %.12f, want %.12f", got, want)
	}
}

func TestTrackerRejectsContradictoryFableCacheCreationBreakdown(t *testing.T) {
	record := usage.Record{
		Provider: "claude",
		Model:    "claude-fable-5",
		Detail: usage.Detail{
			CacheCreationTokens:   5,
			CacheCreation5mTokens: 4,
			CacheCreation1hTokens: 6,
		},
	}
	if _, _, _, ok := estimatedCost(record, 0, 5); ok {
		t.Fatal("estimatedCost() available for contradictory cache creation breakdown")
	}
}

func TestKnownModelFailureWithoutUsageIsUnpriced(t *testing.T) {
	tracker := NewTracker(1)
	tracker.HandleUsage(context.Background(), usage.Record{
		Provider: "codex",
		Model:    "gpt-5.6-sol",
		Failed:   true,
	})

	snapshot := tracker.Snapshot()
	if snapshot.PricedRequests != 0 || snapshot.UnpricedRequests != 1 || snapshot.RecentEvents[0].EstimatedCostAvailable {
		t.Fatalf("pricing completeness = priced %d unpriced %d event available %t, want 0/1/false", snapshot.PricedRequests, snapshot.UnpricedRequests, snapshot.RecentEvents[0].EstimatedCostAvailable)
	}
}

func TestTrackerCountsCompactionAndBoundsRecentEvents(t *testing.T) {
	tracker := NewTracker(2)
	for i := 0; i < 3; i++ {
		operation := ""
		if i == 1 {
			operation = "COMPACTION"
		}
		tracker.HandleUsage(context.Background(), usage.Record{
			Provider:  "codex",
			Model:     "gpt-5.6-luna",
			Operation: operation,
		})
	}
	tracker.HandleUsage(context.Background(), usage.Record{
		Provider:  "codex",
		Model:     "gpt-5.6-luna",
		Operation: "compaction",
		Failed:    true,
	})

	snapshot := tracker.Snapshot()
	if snapshot.Requests != 4 || snapshot.CompactionAttempts != 2 || snapshot.Compactions != 1 {
		t.Fatalf("requests/attempts/successful compactions = %d/%d/%d, want 4/2/1", snapshot.Requests, snapshot.CompactionAttempts, snapshot.Compactions)
	}
	if len(snapshot.RecentEvents) != 2 {
		t.Fatalf("recent events = %d, want 2", len(snapshot.RecentEvents))
	}
	if snapshot.RecentEvents[0].Sequence != 3 || snapshot.RecentEvents[0].Compaction {
		t.Fatalf("oldest retained event = %+v, want sequence 3 inference", snapshot.RecentEvents[0])
	}
	if snapshot.RecentEvents[1].Sequence != 4 || !snapshot.RecentEvents[1].Compaction || !snapshot.RecentEvents[1].Failed {
		t.Fatalf("latest retained event = %+v, want failed compaction attempt", snapshot.RecentEvents[1])
	}
}

func TestTrackerUsesLongContextTierAbove272K(t *testing.T) {
	tracker := NewTracker(2)
	tracker.HandleUsage(context.Background(), usage.Record{
		Provider: "codex",
		Model:    "gpt-5.6-sol",
		Detail: usage.Detail{
			InputTokens:           300_000,
			OutputTokens:          100,
			CacheReadTokens:       100_000,
			CacheTelemetryPresent: true,
		},
	})
	tracker.HandleUsage(context.Background(), usage.Record{
		Provider: "codex",
		Model:    "gpt-5.6-sol",
		Detail: usage.Detail{
			InputTokens: 272_000,
		},
	})

	snapshot := tracker.Snapshot()
	if got, want := snapshot.RecentEvents[0].EstimatedCostTier, "long"; got != want {
		t.Fatalf("300K cost tier = %q, want %q", got, want)
	}
	if got, want := snapshot.RecentEvents[0].EstimatedCostUSD, 2.1045; math.Abs(got-want) > 1e-12 {
		t.Fatalf("300K estimated cost = %.12f, want %.12f", got, want)
	}
	if got, want := snapshot.RecentEvents[1].EstimatedCostTier, "short"; got != want {
		t.Fatalf("272K cost tier = %q, want %q", got, want)
	}
}

func TestSnapshotAfterReportsCursorAndRetainedEventGap(t *testing.T) {
	tracker := NewTracker(3)
	for i := 0; i < 5; i++ {
		tracker.HandleUsage(context.Background(), usage.Record{Provider: "other", Model: "unpriced"})
	}

	first := tracker.SnapshotAfter(0, 2)
	if first.BootID == "" || first.ProcessID <= 0 {
		t.Fatalf("process identity = boot %q pid %d, want populated", first.BootID, first.ProcessID)
	}
	if first.EarliestSequence != 3 || first.LatestSequence != 5 {
		t.Fatalf("earliest/latest = %d/%d, want 3/5", first.EarliestSequence, first.LatestSequence)
	}
	if !first.EventGap || first.GapFromSequence != 1 || first.GapToSequence != 2 {
		t.Fatalf("gap = %t %d-%d, want true 1-2", first.EventGap, first.GapFromSequence, first.GapToSequence)
	}
	if len(first.RecentEvents) != 2 || first.RecentEvents[0].Sequence != 3 || first.RecentEvents[1].Sequence != 4 || first.NextAfter != 4 {
		t.Fatalf("first page events=%+v next=%d, want sequences 3,4 next 4", first.RecentEvents, first.NextAfter)
	}

	second := tracker.SnapshotAfter(first.NextAfter, 2)
	if second.EventGap || len(second.RecentEvents) != 1 || second.RecentEvents[0].Sequence != 5 || second.NextAfter != 5 {
		t.Fatalf("second page = gap %t events %+v next %d, want sequence 5 without gap", second.EventGap, second.RecentEvents, second.NextAfter)
	}
}

func TestUnknownModelRemainsUnpricedAtLongContext(t *testing.T) {
	tracker := NewTracker(1)
	tracker.HandleUsage(context.Background(), usage.Record{
		Provider: "codex",
		Model:    "unknown-long-context-model",
		Detail:   usage.Detail{InputTokens: 300_000},
	})
	snapshot := tracker.Snapshot()
	if snapshot.PricedRequests != 0 || snapshot.UnpricedRequests != 1 || snapshot.RecentEvents[0].EstimatedCostAvailable {
		t.Fatalf("pricing completeness = priced %d unpriced %d event available %t, want 0/1/false", snapshot.PricedRequests, snapshot.UnpricedRequests, snapshot.RecentEvents[0].EstimatedCostAvailable)
	}
}
