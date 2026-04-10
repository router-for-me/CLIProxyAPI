package usage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestFileSnapshotStoreRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "usage-statistics.json")
	store := NewFileSnapshotStore(path)
	snapshot := StatisticsSnapshot{
		TotalRequests: 3,
		SuccessCount:  2,
		FailureCount:  1,
		TotalTokens:   42,
		APIs: map[string]APISnapshot{
			"gemini": {
				TotalRequests: 3,
				TotalTokens:   42,
				Models: map[string]ModelSnapshot{
					"gemini-2.5-pro": {
						TotalRequests: 3,
						TotalTokens:   42,
						Details: []RequestDetail{{
							Timestamp: time.Unix(1700000000, 0).UTC(),
							Source:    "test@example.com",
							Tokens:    TokenStats{InputTokens: 20, OutputTokens: 22, TotalTokens: 42},
						}},
					},
				},
			},
		},
	}
	if err := store.Save(context.Background(), snapshot); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.TotalRequests != snapshot.TotalRequests || loaded.TotalTokens != snapshot.TotalTokens {
		t.Fatalf("loaded snapshot mismatch: got %+v want %+v", loaded, snapshot)
	}
	if got := loaded.APIs["gemini"].Models["gemini-2.5-pro"].Details[0].Source; got != "test@example.com" {
		t.Fatalf("detail source = %q", got)
	}
}

func TestResolvePersistencePath(t *testing.T) {
	got := ResolvePersistencePath("/tmp/config/config.yaml", "usage-statistics.json")
	want := "/tmp/config/usage-statistics.json"
	if got != want {
		t.Fatalf("ResolvePersistencePath() = %q, want %q", got, want)
	}
}

func TestPersistenceManagerLoadAndFlush(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "usage-statistics.json")
	store := NewFileSnapshotStore(path)
	seed := StatisticsSnapshot{
		TotalRequests: 1,
		SuccessCount:  1,
		TotalTokens:   7,
		APIs: map[string]APISnapshot{
			"gemini": {
				TotalRequests: 1,
				TotalTokens:   7,
				Models: map[string]ModelSnapshot{
					"m": {
						TotalRequests: 1,
						TotalTokens:   7,
						Details:       []RequestDetail{{Timestamp: time.Unix(1700000100, 0).UTC(), Source: "seed", Tokens: TokenStats{TotalTokens: 7}}},
					},
				},
			},
		},
	}
	if err := store.Save(context.Background(), seed); err != nil {
		t.Fatalf("seed Save() error = %v", err)
	}

	stats := NewRequestStatistics()
	manager := NewPersistenceManager(store, stats)
	if err := manager.Load(context.Background()); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := stats.Snapshot().TotalRequests; got != 1 {
		t.Fatalf("restored total_requests = %d, want 1", got)
	}

	stats.Record(context.Background(), coreusage.Record{Provider: "gemini", Model: "m2", Source: "live", RequestedAt: time.Now().UTC(), Failed: false, Detail: coreusage.Detail{TotalTokens: 11}})
	manager.MarkDirty()
	if err := manager.Flush(context.Background()); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() after flush error = %v", err)
	}
	if loaded.TotalRequests != 2 {
		t.Fatalf("persisted total_requests = %d, want 2", loaded.TotalRequests)
	}
}

func TestUpdateAPIStatsBoundsDetails(t *testing.T) {
	stats := NewRequestStatistics()
	bucket := &apiStats{Models: make(map[string]*modelStats)}
	for i := 0; i < defaultMaxRequestDetails+25; i++ {
		stats.updateAPIStats(bucket, "model", RequestDetail{Timestamp: time.Unix(int64(i), 0).UTC(), Source: "src"})
	}
	got := len(bucket.Models["model"].Details)
	if got != defaultMaxRequestDetails {
		t.Fatalf("details len = %d, want %d", got, defaultMaxRequestDetails)
	}
	if first := bucket.Models["model"].Details[0].Timestamp.Unix(); first != 25 {
		t.Fatalf("first retained timestamp = %d, want 25", first)
	}
}

func TestRequestStatisticsRestoreSnapshotPreservesAggregates(t *testing.T) {
	stats := NewRequestStatistics()
	snapshot := StatisticsSnapshot{
		TotalRequests: 1500,
		SuccessCount:  1490,
		FailureCount:  10,
		TotalTokens:   9000,
		APIs: map[string]APISnapshot{
			"gemini": {
				TotalRequests: 1500,
				TotalTokens:   9000,
				Models: map[string]ModelSnapshot{
					"gemini-2.5-pro": {
						TotalRequests: 1500,
						TotalTokens:   9000,
						Details: []RequestDetail{
							{Timestamp: time.Unix(1700000000, 0).UTC(), Source: "tail-1", Tokens: TokenStats{TotalTokens: 6}},
							{Timestamp: time.Unix(1700000001, 0).UTC(), Source: "tail-2", Tokens: TokenStats{TotalTokens: 7}},
						},
					},
				},
			},
		},
		RequestsByDay:  map[string]int64{"2025-01-01": 1500},
		RequestsByHour: map[string]int64{"10": 1500},
		TokensByDay:    map[string]int64{"2025-01-01": 9000},
		TokensByHour:   map[string]int64{"10": 9000},
	}

	stats.RestoreSnapshot(snapshot)
	restored := stats.Snapshot()
	if restored.TotalRequests != 1500 || restored.TotalTokens != 9000 || restored.FailureCount != 10 {
		t.Fatalf("restored snapshot mismatch: got %+v", restored)
	}
	if got := restored.APIs["gemini"].Models["gemini-2.5-pro"].TotalRequests; got != 1500 {
		t.Fatalf("restored model total_requests = %d, want 1500", got)
	}
	if got := len(restored.APIs["gemini"].Models["gemini-2.5-pro"].Details); got != 2 {
		t.Fatalf("restored details len = %d, want 2", got)
	}
}

func TestPersistenceManagerFlushRetainsDirtyOnSaveFailure(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{Provider: "gemini", Model: "m", Source: "live", RequestedAt: time.Now().UTC(), Detail: coreusage.Detail{TotalTokens: 3}})
	store := &failingSnapshotStore{path: "/tmp/failing", err: errors.New("boom")}
	manager := NewPersistenceManager(store, stats)
	manager.MarkDirty()

	if err := manager.Flush(context.Background()); err == nil {
		t.Fatal("expected Flush() error")
	}
	if !manager.dirty {
		t.Fatal("expected dirty flag to remain set after save failure")
	}
}

func TestFileSnapshotStoreMissingFile(t *testing.T) {
	store := NewFileSnapshotStore(filepath.Join(t.TempDir(), "missing.json"))
	snapshot, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() missing file error = %v", err)
	}
	if snapshot.TotalRequests != 0 {
		t.Fatalf("expected empty snapshot, got %+v", snapshot)
	}
}

func TestFileSnapshotStoreCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(path, []byte("{"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	store := NewFileSnapshotStore(path)
	if _, err := store.Load(context.Background()); err == nil {
		t.Fatal("expected error for corrupt snapshot file")
	}
}

type failingSnapshotStore struct {
	path string
	err  error
}

func (s *failingSnapshotStore) Load(ctx context.Context) (StatisticsSnapshot, error) {
	return StatisticsSnapshot{}, nil
}

func (s *failingSnapshotStore) Save(ctx context.Context, snapshot StatisticsSnapshot) error {
	return s.err
}

func (s *failingSnapshotStore) Path() string {
	return s.path
}
