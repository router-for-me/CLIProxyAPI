package usageaggregate

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func newTestAggregator() *aggregator {
	return &aggregator{
		byCred: make(map[string]*credentialEntry),
		byProv: make(map[string]*entry),
	}
}

func subsetRecord(provider, authID, authIndex string, input, cacheRead, cacheWrite, output, reasoning, total int64) coreusage.Record {
	return coreusage.Record{
		Provider:  provider,
		AuthID:    authID,
		AuthIndex: authIndex,
		Detail: coreusage.Detail{
			TokenBreakdown: coreusage.NewSubsetTokenBreakdown(input, cacheRead, cacheWrite, output, reasoning, total),
		},
	}
}

func TestAggregatorSumsBreakdownPerCredential(t *testing.T) {
	a := newTestAggregator()

	a.HandleUsage(context.Background(), subsetRecord("Codex", "primary.json", "idx-1", 100, 40, 10, 50, 20, 150))
	a.HandleUsage(context.Background(), subsetRecord("codex", "primary.json", "idx-1", 200, 0, 0, 25, 5, 225))
	a.HandleUsage(context.Background(), subsetRecord("codex", "other.json", "idx-2", 10, 0, 0, 5, 0, 15))

	snapshot := a.snapshot()

	if snapshot.Requests != 3 {
		t.Fatalf("requests = %d, want 3", snapshot.Requests)
	}
	if snapshot.Breakdown.TotalTokens != 390 {
		t.Fatalf("total tokens = %d, want 390", snapshot.Breakdown.TotalTokens)
	}
	if snapshot.Breakdown.Input.CacheReadTokens != 40 {
		t.Fatalf("cache read tokens = %d, want 40", snapshot.Breakdown.Input.CacheReadTokens)
	}
	if snapshot.Breakdown.Output.ReasoningTokens != 25 {
		t.Fatalf("reasoning tokens = %d, want 25", snapshot.Breakdown.Output.ReasoningTokens)
	}
	if snapshot.Breakdown.Quality != coreusage.TokenAccountingQualityComplete {
		t.Fatalf("quality = %q, want complete", snapshot.Breakdown.Quality)
	}

	if len(snapshot.Credentials) != 2 {
		t.Fatalf("credentials = %d, want 2", len(snapshot.Credentials))
	}
	// Sorted by total tokens descending: primary (375) before other (15).
	primary := snapshot.Credentials[0]
	if primary.AuthID != "primary.json" || primary.AuthIndex != "idx-1" {
		t.Fatalf("unexpected primary identity: %+v", primary)
	}
	if primary.Provider != "codex" {
		t.Fatalf("provider = %q, want normalized codex", primary.Provider)
	}
	if primary.Requests != 2 {
		t.Fatalf("primary requests = %d, want 2", primary.Requests)
	}
	if primary.Breakdown.TotalTokens != 375 {
		t.Fatalf("primary total tokens = %d, want 375", primary.Breakdown.TotalTokens)
	}
	if primary.Breakdown.Input.TotalTokens != 300 {
		t.Fatalf("primary input tokens = %d, want 300", primary.Breakdown.Input.TotalTokens)
	}
	if primary.Breakdown.Output.NonReasoningTokens != 50 {
		t.Fatalf("primary non-reasoning tokens = %d, want 50", primary.Breakdown.Output.NonReasoningTokens)
	}

	if len(snapshot.Providers) != 1 || snapshot.Providers[0].Provider != "codex" {
		t.Fatalf("providers = %+v, want single codex entry", snapshot.Providers)
	}
	if snapshot.Providers[0].Breakdown.TotalTokens != 390 {
		t.Fatalf("provider total tokens = %d, want 390", snapshot.Providers[0].Breakdown.TotalTokens)
	}
}

func TestAggregatorSkipsFailedAndEmptyRecords(t *testing.T) {
	a := newTestAggregator()

	failed := subsetRecord("codex", "primary.json", "idx-1", 100, 0, 0, 50, 0, 150)
	failed.Failed = true
	a.HandleUsage(context.Background(), failed)

	a.HandleUsage(context.Background(), coreusage.Record{
		Provider: "codex",
		Detail:   coreusage.Detail{TokenBreakdown: coreusage.NewSubsetTokenBreakdown(0, 0, 0, 0, 0, 0)},
	})

	snapshot := a.snapshot()
	if snapshot.Requests != 0 || len(snapshot.Credentials) != 0 || len(snapshot.Providers) != 0 {
		t.Fatalf("expected empty snapshot, got %+v", snapshot)
	}
}

func TestAggregatorDegradesQualityOnInconsistentInput(t *testing.T) {
	a := newTestAggregator()

	a.HandleUsage(context.Background(), subsetRecord("claude", "a.json", "i1", 10, 0, 0, 5, 0, 15))

	inconsistent := coreusage.TokenBreakdown{
		SchemaVersion:      coreusage.TokenAccountingSchemaVersion,
		Quality:            coreusage.TokenAccountingQualityInconsistent,
		TotalTokens:        99,
		UnclassifiedTokens: 99,
	}
	a.HandleUsage(context.Background(), coreusage.Record{
		Provider: "claude",
		AuthID:   "a.json",
		Detail:   coreusage.Detail{TokenBreakdown: inconsistent},
	})

	snapshot := a.snapshot()
	if snapshot.Breakdown.Quality != coreusage.TokenAccountingQualityInconsistent {
		t.Fatalf("quality = %q, want inconsistent", snapshot.Breakdown.Quality)
	}
	if snapshot.Breakdown.TotalTokens != 114 {
		t.Fatalf("total tokens = %d, want 114", snapshot.Breakdown.TotalTokens)
	}
}

// TestSnapshotReceivesPublishedRecords verifies the end-to-end wiring the
// management endpoint depends on: a record published through the shared usage
// manager reaches the plugin registered by this package's init and shows up in
// the global Snapshot.
func TestSnapshotReceivesPublishedRecords(t *testing.T) {
	const authID = "wiring-test.json"

	before := Snapshot()
	// input 12 + output 6 = 18 total; reasoning is a subset of output.
	coreusage.PublishRecord(context.Background(), subsetRecord("codex", authID, "wire-1", 12, 4, 1, 6, 2, 18))

	// PublishRecord hands the record to an asynchronous worker, so poll with a
	// bound instead of sleeping for a fixed duration.
	var after UsageSnapshot
	for i := 0; i < 2000; i++ {
		after = Snapshot()
		if after.Requests >= before.Requests+1 {
			break
		}
		runtime.Gosched()
	}

	if after.Requests < before.Requests+1 {
		t.Fatalf("requests = %d, want >= %d", after.Requests, before.Requests+1)
	}
	if after.Breakdown.TotalTokens != before.Breakdown.TotalTokens+18 {
		t.Fatalf("total tokens = %d, want %d", after.Breakdown.TotalTokens, before.Breakdown.TotalTokens+18)
	}
	if after.Breakdown.Output.ReasoningTokens != before.Breakdown.Output.ReasoningTokens+2 {
		t.Fatalf("reasoning tokens = %d, want %d", after.Breakdown.Output.ReasoningTokens, before.Breakdown.Output.ReasoningTokens+2)
	}

	var found *CredentialUsage
	for i := range after.Credentials {
		if after.Credentials[i].AuthID == authID {
			found = &after.Credentials[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("credential %q missing from snapshot: %+v", authID, after.Credentials)
	}
	if found.Breakdown.TotalTokens != 18 || found.Requests != 1 {
		t.Fatalf("unexpected credential aggregate: %+v", found)
	}
}

// TestRestoreFromPersistedSnapshot verifies the JSON round-trip used by the
// single-file usage state, and that restoring replaces the aggregate.
func TestRestoreFromPersistedSnapshot(t *testing.T) {
	original := Snapshot()
	defer Restore(original)

	breakdown := coreusage.NewSubsetTokenBreakdown(12, 4, 1, 6, 2, 18)
	snapshot := UsageSnapshot{
		Requests:  3,
		Breakdown: breakdown,
		Providers: []ProviderUsage{{Provider: "codex", Requests: 3, Breakdown: breakdown}},
		Credentials: []CredentialUsage{{
			Provider: "Codex", AuthID: "primary.json", AuthIndex: "idx-1",
			Requests: 3, Breakdown: breakdown,
		}},
	}

	data, errMarshal := json.Marshal(snapshot)
	if errMarshal != nil {
		t.Fatalf("marshal snapshot: %v", errMarshal)
	}
	var decoded UsageSnapshot
	if errUnmarshal := json.Unmarshal(data, &decoded); errUnmarshal != nil {
		t.Fatalf("unmarshal snapshot: %v", errUnmarshal)
	}

	before := Revision()
	Restore(decoded)
	if Revision() <= before {
		t.Fatal("restore must advance the revision")
	}

	got := Snapshot()
	if got.Requests != 3 || got.Breakdown.TotalTokens != 18 {
		t.Fatalf("requests = %d, total tokens = %d, want 3 and 18", got.Requests, got.Breakdown.TotalTokens)
	}
	if got.Breakdown.Output.ReasoningTokens != 2 {
		t.Fatalf("reasoning tokens = %d, want 2", got.Breakdown.Output.ReasoningTokens)
	}
	if len(got.Providers) != 1 || got.Providers[0].Provider != "codex" {
		t.Fatalf("providers = %+v", got.Providers)
	}
	if len(got.Credentials) != 1 || got.Credentials[0].AuthID != "primary.json" {
		t.Fatalf("credentials = %+v", got.Credentials)
	}
}
