package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "metrics.json")

	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "test-model",
		RequestedAt: time.Now(),
		Detail:      coreusage.Detail{TotalTokens: 100, InputTokens: 50, OutputTokens: 50},
	})

	persister := &MetricsPersister{stats: stats, filePath: filePath}
	if err := persister.SaveToFile(); err != nil {
		t.Fatalf("SaveToFile() error = %v", err)
	}

	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("metrics file not created: %v", err)
	}

	newStats := NewRequestStatistics()
	newPersister := &MetricsPersister{stats: newStats, filePath: filePath}
	if err := newPersister.LoadFromFile(); err != nil {
		t.Fatalf("LoadFromFile() error = %v", err)
	}

	snap := newStats.Snapshot()
	if snap.TotalRequests != 1 {
		t.Errorf("expected 1 request, got %d", snap.TotalRequests)
	}
	if snap.TotalTokens != 100 {
		t.Errorf("expected 100 tokens, got %d", snap.TotalTokens)
	}
	if len(snap.APIs) != 1 {
		t.Errorf("expected 1 API key in snapshot, got %d", len(snap.APIs))
	}
	if apiSnap, ok := snap.APIs["test-key"]; !ok {
		t.Error("expected test-key in APIs")
	} else if apiSnap.TotalTokens != 100 {
		t.Errorf("expected 100 tokens for test-key, got %d", apiSnap.TotalTokens)
	}
	if len(snap.RequestsByDay) != 1 {
		t.Errorf("expected 1 day entry, got %d", len(snap.RequestsByDay))
	}
	if len(snap.RequestsByHour) != 1 {
		t.Errorf("expected 1 hour entry, got %d", len(snap.RequestsByHour))
	}
}

func TestLoadNonExistentFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "nonexistent.json")

	stats := NewRequestStatistics()
	persister := &MetricsPersister{stats: stats, filePath: filePath}
	err := persister.LoadFromFile()
	if err != nil {
		t.Errorf("expected no error for missing file, got %v", err)
	}
}

func TestLoadCorruptFile(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "metrics.json")
	if err := os.WriteFile(filePath, []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}

	stats := NewRequestStatistics()
	persister := &MetricsPersister{stats: stats, filePath: filePath}
	err := persister.LoadFromFile()
	if err == nil {
		t.Error("expected error for corrupt JSON")
	}
}

func TestAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "metrics.json")

	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "initial-key",
		Model:       "test-model",
		RequestedAt: time.Now(),
		Detail:      coreusage.Detail{TotalTokens: 42},
	})
	persister := &MetricsPersister{stats: stats, filePath: filePath}
	if err := persister.SaveToFile(); err != nil {
		t.Fatal(err)
	}

	originalData, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}

	persister.filePath = filepath.Join(dir, "nonexistent", "metrics.json")

	err = persister.SaveToFile()
	if err == nil {
		t.Error("expected SaveToFile to fail when parent dir doesn't exist")
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("original file should still exist after failed save: %v", err)
	}
	if string(data) != string(originalData) {
		t.Error("original file content changed after failed atomic write")
	}
}

func TestAutoSaveTick(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "metrics.json")

	stats := NewRequestStatistics()
	persister := NewMetricsPersister(stats, filePath)

	persister.Start(50 * time.Millisecond)

	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "test-model",
		RequestedAt: time.Now(),
		Detail:      coreusage.Detail{TotalTokens: 50},
	})

	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	var saved bool
	for !saved {
		select {
		case <-tick.C:
			if _, err := os.Stat(filePath); err == nil {
				saved = true
			}
		case <-deadline:
			t.Fatal("metrics file not created within 2s")
		}
	}

	persister.Stop()

	snap := stats.Snapshot()
	if snap.TotalRequests != 1 {
		t.Errorf("expected 1 request, got %d", snap.TotalRequests)
	}
}

func TestStopSavesOnShutdown(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "metrics.json")

	stats := NewRequestStatistics()
	persister := NewMetricsPersister(stats, filePath)

	persister.Start(1 * time.Hour)

	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "test-model",
		RequestedAt: time.Now(),
		Detail:      coreusage.Detail{TotalTokens: 200},
	})

	persister.Stop()

	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("expected metrics file after Stop(), got error: %v", err)
	}

	newStats := NewRequestStatistics()
	newPersister := NewMetricsPersister(newStats, filePath)
	if err := newPersister.LoadFromFile(); err != nil {
		t.Fatalf("LoadFromFile() error = %v", err)
	}

	snap := newStats.Snapshot()
	if snap.TotalRequests != 1 {
		t.Errorf("expected 1 request after reload, got %d", snap.TotalRequests)
	}
	if snap.TotalTokens != 200 {
		t.Errorf("expected 200 tokens after reload, got %d", snap.TotalTokens)
	}
}

func TestNilPersisterMethods(t *testing.T) {
	var p *MetricsPersister

	if err := p.SaveToFile(); err != nil {
		t.Errorf("SaveToFile on nil should return nil, got %v", err)
	}
	if err := p.LoadFromFile(); err != nil {
		t.Errorf("LoadFromFile on nil should return nil, got %v", err)
	}
	p.Start(5 * time.Minute)
	p.Stop()
}

func TestMergeSnapshotDedupOnLoad(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "metrics.json")

	exactTime := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)

	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "dedup-key",
		Model:       "test-model",
		RequestedAt: exactTime,
		Detail:      coreusage.Detail{TotalTokens: 100},
	})

	persister := &MetricsPersister{stats: stats, filePath: filePath}
	if err := persister.SaveToFile(); err != nil {
		t.Fatal(err)
	}

	newStats := NewRequestStatistics()
	newStats.Record(context.Background(), coreusage.Record{
		APIKey:      "dedup-key",
		Model:       "test-model",
		RequestedAt: exactTime,
		Detail:      coreusage.Detail{TotalTokens: 100},
	})

	newPersister := NewMetricsPersister(newStats, filePath)
	if err := newPersister.LoadFromFile(); err != nil {
		t.Fatalf("LoadFromFile() error = %v", err)
	}

	snap := newStats.Snapshot()
	if snap.TotalRequests != 1 {
		t.Errorf("expected 1 request after dedup load, got %d", snap.TotalRequests)
	}
}
