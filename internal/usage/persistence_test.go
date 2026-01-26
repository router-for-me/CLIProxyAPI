package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestPersistencePlugin_SaveAndLoad(t *testing.T) {
	// Create temp directory for test
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "test-usage-stats.json")

	stats := NewRequestStatistics()

	// Create plugin
	cfg := PersistenceConfig{
		Enabled:  true,
		FilePath: filePath,
		Interval: 1 * time.Hour, // Long interval, we'll manually save
	}
	plugin := NewPersistencePlugin(cfg, stats)

	// Record some data
	stats.Record(context.Background(), mockRecord("api1", "model1", 100))
	stats.Record(context.Background(), mockRecord("api1", "model1", 200))
	stats.Record(context.Background(), mockRecord("api2", "model2", 50))

	// Save
	if err := plugin.save(); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Fatal("persistence file was not created")
	}

	// Create new stats and plugin to test load
	stats2 := NewRequestStatistics()
	plugin2 := NewPersistencePlugin(PersistenceConfig{
		Enabled:  true,
		FilePath: filePath,
		Interval: 1 * time.Hour,
	}, stats2)

	// Load
	if err := plugin2.load(); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	// Verify data was loaded
	snapshot := stats2.Snapshot()
	if snapshot.TotalRequests != 3 {
		t.Errorf("expected 3 total requests, got %d", snapshot.TotalRequests)
	}
	if snapshot.TotalTokens != 350 {
		t.Errorf("expected 350 total tokens, got %d", snapshot.TotalTokens)
	}
}

func TestPersistencePlugin_LoadNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "nonexistent.json")

	stats := NewRequestStatistics()
	plugin := NewPersistencePlugin(PersistenceConfig{
		Enabled:  true,
		FilePath: filePath,
		Interval: 1 * time.Hour,
	}, stats)

	// Load should succeed (no error) for nonexistent file
	if err := plugin.load(); err != nil {
		t.Fatalf("load of nonexistent file should not error: %v", err)
	}
}

func TestPersistencePlugin_AtomicWrite(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "atomic-test.json")

	stats := NewRequestStatistics()
	stats.Record(context.Background(), mockRecord("api1", "model1", 100))

	plugin := NewPersistencePlugin(PersistenceConfig{
		Enabled:  true,
		FilePath: filePath,
		Interval: 1 * time.Hour,
	}, stats)

	// Save
	if err := plugin.save(); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	// Verify no temp file left behind
	tmpPath := filePath + ".tmp"
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Error("temp file should not exist after successful save")
	}
}

func TestPersistencePlugin_StartStop(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "lifecycle-test.json")

	stats := NewRequestStatistics()
	stats.Record(context.Background(), mockRecord("api1", "model1", 100))

	plugin := NewPersistencePlugin(PersistenceConfig{
		Enabled:  true,
		FilePath: filePath,
		Interval: 100 * time.Millisecond,
	}, stats)

	ctx, cancel := context.WithCancel(context.Background())
	plugin.Start(ctx)

	// Wait for at least one periodic save
	time.Sleep(250 * time.Millisecond)

	// Stop
	cancel()
	plugin.Stop()

	// Verify file was created
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Fatal("persistence file should exist after stop")
	}
}

func TestPersistencePlugin_MergeDeduplication(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "dedup-test.json")

	stats := NewRequestStatistics()
	record := mockRecord("api1", "model1", 100)
	stats.Record(context.Background(), record)

	plugin := NewPersistencePlugin(PersistenceConfig{
		Enabled:  true,
		FilePath: filePath,
		Interval: 1 * time.Hour,
	}, stats)

	// Save
	if err := plugin.save(); err != nil {
		t.Fatalf("save failed: %v", err)
	}

	// Load into same stats (should deduplicate)
	if err := plugin.load(); err != nil {
		t.Fatalf("load failed: %v", err)
	}

	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 1 {
		t.Errorf("expected 1 request after dedup, got %d", snapshot.TotalRequests)
	}
}

func mockRecord(apiKey, model string, tokens int64) coreusage.Record {
	return coreusage.Record{
		APIKey:      apiKey,
		Model:       model,
		RequestedAt: time.Now(),
		Detail: coreusage.Detail{
			InputTokens:  tokens / 2,
			OutputTokens: tokens / 2,
			TotalTokens:  tokens,
		},
	}
}
