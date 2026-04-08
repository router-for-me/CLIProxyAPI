package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestSaveSnapshotToFileAndLoadSnapshotFromFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "usage-statistics.json")
	want := StatisticsSnapshot{
		TotalRequests: 1,
		SuccessCount:  1,
		FailureCount:  0,
		TotalTokens:   30,
		APIs: map[string]APISnapshot{
			"test-key": {
				TotalRequests: 1,
				TotalTokens:   30,
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						TotalRequests: 1,
						TotalTokens:   30,
						Details: []RequestDetail{{
							Timestamp: time.Date(2026, 4, 8, 8, 0, 0, 0, time.UTC),
							LatencyMs: 1200,
							Source:    "codex",
							AuthIndex: "codex:0",
							Tokens: TokenStats{
								InputTokens:  10,
								OutputTokens: 20,
								TotalTokens:  30,
							},
						}},
					},
				},
			},
		},
	}

	if err := SaveSnapshotToFile(path, want); err != nil {
		t.Fatalf("SaveSnapshotToFile returned error: %v", err)
	}

	got, err := LoadSnapshotFromFile(path)
	if err != nil {
		t.Fatalf("LoadSnapshotFromFile returned error: %v", err)
	}

	if got.TotalRequests != want.TotalRequests {
		t.Fatalf("total_requests = %d, want %d", got.TotalRequests, want.TotalRequests)
	}
	if got.TotalTokens != want.TotalTokens {
		t.Fatalf("total_tokens = %d, want %d", got.TotalTokens, want.TotalTokens)
	}
	if len(got.APIs["test-key"].Models["gpt-5.4"].Details) != 1 {
		t.Fatalf("details len = %d, want 1", len(got.APIs["test-key"].Models["gpt-5.4"].Details))
	}
}

func TestRequestStatisticsEnablePersistenceRestoresExistingSnapshot(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "usage-statistics.json")
	want := StatisticsSnapshot{
		TotalRequests: 1,
		SuccessCount:  1,
		TotalTokens:   30,
		APIs: map[string]APISnapshot{
			"test-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp: time.Date(2026, 4, 8, 8, 0, 0, 0, time.UTC),
							Tokens: TokenStats{
								InputTokens:  10,
								OutputTokens: 20,
								TotalTokens:  30,
							},
						}},
					},
				},
			},
		},
	}
	if err := SaveSnapshotToFile(path, want); err != nil {
		t.Fatalf("SaveSnapshotToFile returned error: %v", err)
	}

	stats := NewRequestStatistics()
	if err := stats.EnablePersistence(path); err != nil {
		t.Fatalf("EnablePersistence returned error: %v", err)
	}

	got := stats.Snapshot()
	if got.TotalRequests != want.TotalRequests {
		t.Fatalf("total_requests = %d, want %d", got.TotalRequests, want.TotalRequests)
	}
	if got.TotalTokens != want.TotalTokens {
		t.Fatalf("total_tokens = %d, want %d", got.TotalTokens, want.TotalTokens)
	}
}

func TestRequestStatisticsFlushPersistenceWritesMergedSnapshot(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "usage-statistics.json")
	stats := NewRequestStatistics()
	if err := stats.EnablePersistence(path); err != nil {
		t.Fatalf("EnablePersistence returned error: %v", err)
	}

	result := stats.MergeSnapshot(StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp: time.Date(2026, 4, 8, 8, 0, 0, 0, time.UTC),
							Tokens: TokenStats{
								InputTokens:  10,
								OutputTokens: 20,
								TotalTokens:  30,
							},
						}},
					},
				},
			},
		},
	})
	if result.Added != 1 {
		t.Fatalf("added = %d, want 1", result.Added)
	}

	if err := stats.FlushPersistence(); err != nil {
		t.Fatalf("FlushPersistence returned error: %v", err)
	}

	got, err := LoadSnapshotFromFile(path)
	if err != nil {
		t.Fatalf("LoadSnapshotFromFile returned error: %v", err)
	}
	if got.TotalRequests != 1 {
		t.Fatalf("total_requests = %d, want 1", got.TotalRequests)
	}
}

func TestRequestStatisticsRecordPersistsSnapshotAsync(t *testing.T) {
	t.Parallel()

	previousEnabled := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() {
		SetStatisticsEnabled(previousEnabled)
	})

	path := filepath.Join(t.TempDir(), "usage-statistics.json")
	stats := NewRequestStatistics()
	if err := stats.EnablePersistence(path); err != nil {
		t.Fatalf("EnablePersistence returned error: %v", err)
	}

	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "persisted-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 4, 8, 9, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  12,
			OutputTokens: 18,
			TotalTokens:  30,
		},
	})

	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot, err := LoadSnapshotFromFile(path)
		if err == nil {
			modelSnapshot := snapshot.APIs["persisted-key"].Models["gpt-5.4"]
			if snapshot.TotalRequests >= 1 && len(modelSnapshot.Details) >= 1 {
				return
			}
		}

		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for persisted snapshot at %s", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRequestStatisticsDisablePersistenceStopsWritingToDisk(t *testing.T) {
	t.Parallel()

	previousEnabled := StatisticsEnabled()
	SetStatisticsEnabled(true)
	t.Cleanup(func() {
		SetStatisticsEnabled(previousEnabled)
	})

	path := filepath.Join(t.TempDir(), "usage-statistics.json")
	stats := NewRequestStatistics()
	if err := stats.EnablePersistence(path); err != nil {
		t.Fatalf("EnablePersistence returned error: %v", err)
	}
	stats.DisablePersistence()

	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "disabled-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 4, 8, 9, 30, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  12,
			OutputTokens: 18,
			TotalTokens:  30,
		},
	})

	time.Sleep(300 * time.Millisecond)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected persistence to stay disabled, stat err = %v", err)
	}
}
