package usage

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSnapshotStoreLoadSaveAndMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	store := NewSnapshotStore(path)

	if store.Path() != path {
		t.Fatalf("Path() = %q, want %q", store.Path(), path)
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	defaultPath := DefaultSnapshotPath(configPath)
	wantDefaultPath := filepath.Join(filepath.Dir(configPath), "usage-statistics.json")
	if defaultPath != wantDefaultPath {
		t.Fatalf("DefaultSnapshotPath() = %q, want %q", defaultPath, wantDefaultPath)
	}
	defaultStore := NewSnapshotStore("")
	if defaultStore.Path() != DefaultSnapshotPath("") {
		t.Fatalf("NewSnapshotStore empty path = %q, want %q", defaultStore.Path(), DefaultSnapshotPath(""))
	}

	missing, err := store.Load()
	if err != nil {
		t.Fatalf("Load() missing file error = %v, want nil", err)
	}
	if missing.TotalRequests != 0 || len(missing.APIs) != 0 {
		t.Fatalf("missing snapshot = %+v, want empty", missing)
	}

	timestamp := time.Date(2026, 4, 1, 9, 30, 0, 0, time.UTC)
	want := StatisticsSnapshot{
		TotalRequests: 1,
		SuccessCount:  1,
		TotalTokens:   42,
		APIs: map[string]APISnapshot{
			"api-key": {
				TotalRequests: 1,
				TotalTokens:   42,
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						TotalRequests: 1,
						TotalTokens:   42,
						Details: []RequestDetail{{
							Timestamp: timestamp,
							Provider:  "openai",
							Model:     "gpt-5.4",
							APIKey:    "api-key",
							Tokens: TokenStats{
								InputTokens:  12,
								OutputTokens: 30,
								TotalTokens:  42,
							},
						}},
					},
				},
			},
		},
		Providers: map[string]ProviderSnapshot{
			"openai": {TotalRequests: 1, TotalTokens: 42},
		},
		Models: map[string]SummarySnapshot{
			"gpt-5.4": {TotalRequests: 1, TotalTokens: 42},
		},
		RequestsByDay:  map[string]int64{"2026-04-01": 1},
		RequestsByHour: map[string]int64{"09": 1},
		TokensByDay:    map[string]int64{"2026-04-01": 42},
		TokensByHour:   map[string]int64{"09": 42},
	}

	if err := store.Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() saved file error = %v", err)
	}
	if got.TotalRequests != want.TotalRequests || got.TotalTokens != want.TotalTokens {
		t.Fatalf("loaded totals = requests %d tokens %d, want requests %d tokens %d", got.TotalRequests, got.TotalTokens, want.TotalRequests, want.TotalTokens)
	}
	if got.APIs["api-key"].Models["gpt-5.4"].Details[0].Provider != "openai" {
		t.Fatalf("loaded provider = %q, want openai", got.APIs["api-key"].Models["gpt-5.4"].Details[0].Provider)
	}
	if got.Providers["openai"].TotalTokens != 42 {
		t.Fatalf("loaded provider tokens = %d, want 42", got.Providers["openai"].TotalTokens)
	}

	if err := os.WriteFile(path, []byte("{malformed"), 0o600); err != nil {
		t.Fatalf("write malformed file: %v", err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatalf("Load() malformed file error = nil, want error")
	} else if !errors.Is(err, ErrMalformedSnapshot) {
		t.Fatalf("Load() malformed file error = %v, want ErrMalformedSnapshot", err)
	}

	afterMalformed, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read malformed file after Load: %v", err)
	}
	if string(afterMalformed) != "{malformed" {
		t.Fatalf("Load() changed malformed file to %q", string(afterMalformed))
	}
}

type countingSnapshotWriter struct {
	mu       sync.Mutex
	count    int
	snapshot StatisticsSnapshot
}

func (w *countingSnapshotWriter) Save(snapshot StatisticsSnapshot) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.count++
	w.snapshot = snapshot
	return nil
}

func (w *countingSnapshotWriter) state() (int, StatisticsSnapshot) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.count, w.snapshot
}

func TestSnapshotSaverCoalescesWrites(t *testing.T) {
	writer := &countingSnapshotWriter{}
	saver := NewSnapshotSaver(writer, 10*time.Millisecond)
	t.Cleanup(func() {
		if err := saver.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	saver.SaveSoon(StatisticsSnapshot{TotalRequests: 1})
	saver.SaveSoon(StatisticsSnapshot{TotalRequests: 2})
	saver.SaveSoon(StatisticsSnapshot{TotalRequests: 3})

	time.Sleep(50 * time.Millisecond)

	count, snapshot := writer.state()
	if count != 1 {
		t.Fatalf("SaveSoon() wrote %d snapshots, want 1", count)
	}
	if snapshot.TotalRequests != 3 {
		t.Fatalf("SaveSoon() saved total requests %d, want latest snapshot with 3", snapshot.TotalRequests)
	}
}
