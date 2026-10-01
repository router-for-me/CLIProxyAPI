package store

import (
	"context"
	"testing"
	"time"
)

func TestOnTheFlyLogRecordListPurge(t *testing.T) {
	pg := newTestPostgresStore(t, "test_litellm_onthefly_log")
	defer pg.Close()
	ensureMigrated(t, pg)
	ctx := context.Background()
	s := NewOnTheFlyLogStore(pg)
	if s == nil {
		t.Fatal("NewOnTheFlyLogStore returned nil")
	}

	// Seed a distinctive older row to prove ordering + retention.
	if err := s.RecordOnTheFly(ctx, OnTheFlyLogEvent{
		OccurredAt: time.Now().Add(-48 * time.Hour), Outcome: OnTheFlyOutcomeError,
		KeyPrefix: "sk-old", KeyID: "h-old", UserID: "u-old", Source: "authorization",
		LatencyMs: 3, ErrorMessage: "boom",
	}); err != nil {
		t.Fatalf("RecordOnTheFly(old): %v", err)
	}
	if err := s.RecordOnTheFly(ctx, OnTheFlyLogEvent{
		Outcome: OnTheFlyOutcomeSynced, KeyPrefix: "sk-new", KeyID: "h-new",
		UserID: "u-new", Source: "x-api-key", LatencyMs: 12,
	}); err != nil {
		t.Fatalf("RecordOnTheFly(new): %v", err)
	}

	all, err := s.ListOnTheFlyLog(ctx, OnTheFlyLogFilter{}, 10)
	if err != nil {
		t.Fatalf("ListOnTheFlyLog: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("len = %d; want 2", len(all))
	}
	if all[0].KeyPrefix != "sk-new" {
		t.Fatalf("first row = %q; want newest first", all[0].KeyPrefix)
	}
	if all[0].Outcome != OnTheFlyOutcomeSynced || all[0].KeyID != "h-new" {
		t.Fatalf("newest row mismatch: %+v", all[0])
	}

	filtered, err := s.ListOnTheFlyLog(ctx, OnTheFlyLogFilter{Outcome: OnTheFlyOutcomeError}, 10)
	if err != nil {
		t.Fatalf("ListOnTheFlyLog(filtered): %v", err)
	}
	if len(filtered) != 1 || filtered[0].ErrorMessage != "boom" {
		t.Fatalf("filtered = %+v; want one error row with message", filtered)
	}

	// Retention purge removes only the 48h-old row.
	deleted, err := s.PurgeOnTheFlyLogBefore(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("PurgeOnTheFlyLogBefore: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("purged = %d; want 1", deleted)
	}
	remaining, _ := s.ListOnTheFlyLog(ctx, OnTheFlyLogFilter{}, 10)
	if len(remaining) != 1 {
		t.Fatalf("remaining = %d; want 1", len(remaining))
	}

	cleared, err := s.PurgeOnTheFlyLog(ctx)
	if err != nil {
		t.Fatalf("PurgeOnTheFlyLog: %v", err)
	}
	if cleared != 1 {
		t.Fatalf("cleared = %d; want 1", cleared)
	}
}
