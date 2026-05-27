package usage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestPersistencePlugin_JSON_SaveAndRestore(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "usage.json")

	stats := NewRequestStatistics()
	plugin := NewPersistencePlugin(stats, PersistenceConfig{
		Enabled: true,
		Backend: "json",
		Path:    dbPath,
	})
	defer plugin.Stop()

	// Record some usage
	stats.Record(context.Background(), coreusage.Record{
		APIKey:          "test-key",
		Model:           "gpt-5.4",
		ReasoningEffort: "high",
		RequestedAt:     time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:         100,
			OutputTokens:        50,
			CacheReadTokens:     30,
			CacheCreationTokens: 20,
			TotalTokens:         150,
		},
	})

	// Flush to disk
	if err := plugin.Flush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatalf("expected file to exist at %s", dbPath)
	}

	// Read and verify content
	data, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read file failed: %v", err)
	}

	var snapshot StatisticsSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if snapshot.TotalRequests != 1 {
		t.Fatalf("total_requests = %d, want 1", snapshot.TotalRequests)
	}

	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}

	if details[0].Tokens.CacheReadTokens != 30 {
		t.Fatalf("cache_read_tokens = %d, want 30", details[0].Tokens.CacheReadTokens)
	}

	if details[0].ReasoningEffort != "high" {
		t.Fatalf("reasoning_effort = %q, want %q", details[0].ReasoningEffort, "high")
	}
}

func TestPersistencePlugin_JSON_RestoreOnStartup(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "usage.json")

	// Create initial data
	initialStats := NewRequestStatistics()
	initialStats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  100,
			OutputTokens: 50,
			TotalTokens:  150,
		},
	})

	// Save initial data
	initialPlugin := NewPersistencePlugin(initialStats, PersistenceConfig{
		Enabled: true,
		Backend: "json",
		Path:    dbPath,
	})
	if err := initialPlugin.Flush(); err != nil {
		t.Fatalf("initial flush failed: %v", err)
	}
	initialPlugin.Stop()

	// Create new stats and restore
	restoredStats := NewRequestStatistics()
	restorePlugin := NewPersistencePlugin(restoredStats, PersistenceConfig{
		Enabled: true,
		Backend: "json",
		Path:    dbPath,
	})
	defer restorePlugin.Stop()

	if err := restorePlugin.Restore(); err != nil {
		t.Fatalf("restore failed: %v", err)
	}

	snapshot := restoredStats.Snapshot()
	if snapshot.TotalRequests != 1 {
		t.Fatalf("total_requests = %d, want 1", snapshot.TotalRequests)
	}

	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
}

func TestPersistencePlugin_DisabledNoOp(t *testing.T) {
	stats := NewRequestStatistics()
	plugin := NewPersistencePlugin(stats, PersistenceConfig{
		Enabled: false,
	})
	defer plugin.Stop()

	// Should not error
	if err := plugin.Flush(); err != nil {
		t.Fatalf("flush should be no-op when disabled: %v", err)
	}

	if err := plugin.Restore(); err != nil {
		t.Fatalf("restore should be no-op when disabled: %v", err)
	}
}

func TestPersistencePlugin_AutoFlush(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "usage.json")

	stats := NewRequestStatistics()
	plugin := NewPersistencePlugin(stats, PersistenceConfig{
		Enabled:       true,
		Backend:       "json",
		Path:          dbPath,
		FlushInterval: 100 * time.Millisecond,
	})
	defer plugin.Stop()

	// Record usage
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Now(),
		Detail: coreusage.Detail{
			InputTokens:  100,
			OutputTokens: 50,
			TotalTokens:  150,
		},
	})

	// Wait for auto-flush
	time.Sleep(200 * time.Millisecond)

	// Verify file exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatalf("expected file to exist after auto-flush")
	}
}
