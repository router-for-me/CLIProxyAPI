package usage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestFileUsageStoreLoadMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	store := NewFileUsageStore(path)

	snapshot, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if snapshot.TotalRequests != 0 || len(snapshot.APIs) != 0 {
		t.Fatalf("expected empty snapshot, got %+v", snapshot)
	}
}

func TestFileUsageStoreSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	store := NewFileUsageStore(path)

	input := StatisticsSnapshot{
		TotalRequests: 2,
		SuccessCount:  1,
		FailureCount:  1,
		TotalTokens:   42,
		APIs: map[string]APISnapshot{
			"key-a": {
				TotalRequests: 2,
				TotalTokens:   42,
				Models: map[string]ModelSnapshot{
					"model-x": {
						TotalRequests: 2,
						TotalTokens:   42,
						Details: []RequestDetail{{
							Timestamp: time.Unix(1739875200, 0).UTC(),
							Source:    "test",
							AuthIndex: "0",
							Tokens: TokenStats{
								InputTokens:  10,
								OutputTokens: 32,
								TotalTokens:  42,
							},
							Failed: false,
						}},
					},
				},
			},
		},
		RequestsByDay:  map[string]int64{"2026-02-16": 2},
		RequestsByHour: map[string]int64{"08": 2},
		TokensByDay:    map[string]int64{"2026-02-16": 42},
		TokensByHour:   map[string]int64{"08": 42},
	}

	if err := store.Save(context.Background(), input); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	output, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if output.TotalRequests != input.TotalRequests || output.TotalTokens != input.TotalTokens {
		t.Fatalf("unexpected loaded snapshot, got %+v", output)
	}
	if len(output.APIs) != 1 || len(output.APIs["key-a"].Models) != 1 {
		t.Fatalf("expected nested API/model data to round-trip, got %+v", output.APIs)
	}
	if got := len(output.APIs["key-a"].Models["model-x"].Details); got != 1 {
		t.Fatalf("expected 1 detail entry, got %d", got)
	}
}

func TestFileUsageStoreLoadCorruptedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	if err := os.WriteFile(path, []byte("{not-valid-json"), 0o644); err != nil {
		t.Fatalf("failed to seed corrupted file: %v", err)
	}

	store := NewFileUsageStore(path)
	_, err := store.Load(context.Background())
	if err == nil {
		t.Fatal("expected error for corrupted JSON, got nil")
	}
}

func TestFileUsageStoreSaveUsesAtomicRenamePattern(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	store := NewFileUsageStore(path)

	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("failed to seed existing file: %v", err)
	}

	if err := store.Save(context.Background(), StatisticsSnapshot{TotalRequests: 1}); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read final file: %v", err)
	}
	if len(data) == 0 || data[0] != '{' {
		t.Fatalf("expected JSON content, got %q", string(data))
	}

	tmpFiles, err := filepath.Glob(path + ".tmp.*")
	if err != nil {
		t.Fatalf("glob failed: %v", err)
	}
	if len(tmpFiles) != 0 {
		t.Fatalf("expected no temp files left behind, found %v", tmpFiles)
	}
}

func TestStartPeriodicFlushAndStop(t *testing.T) {
	stats := NewRequestStatistics()
	stats.totalRequests = 1

	store := &countingStore{}
	stop := StartPeriodicFlush(context.Background(), stats, store, 10*time.Millisecond)
	t.Cleanup(stop)

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if store.Count() > 0 {
			stop()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("expected periodic flush to save at least once")
}

func TestFileUsageStoreSaveHonorsCanceledContext(t *testing.T) {
	store := NewFileUsageStore(filepath.Join(t.TempDir(), "usage.json"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := store.Save(ctx, StatisticsSnapshot{TotalRequests: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

type countingStore struct {
	mu    sync.Mutex
	count int
}

func (s *countingStore) Load(context.Context) (StatisticsSnapshot, error) {
	return StatisticsSnapshot{}, nil
}

func (s *countingStore) Save(context.Context, StatisticsSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.count++
	return nil
}

func (s *countingStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}
