package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestSQLitePlugin_HandleUsage(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_usage.db")
	plugin, err := NewSQLitePlugin(dbPath)
	if err != nil {
		t.Fatalf("failed to create plugin: %v", err)
	}
	defer plugin.Close()

	record := coreusage.Record{
		Provider:    "openai",
		Model:       "gpt-4",
		APIKey:      "test-key",
		AuthID:      "auth-1",
		AuthIndex:   "0",
		Source:      "test",
		RequestedAt: time.Now(),
		Failed:      false,
		Detail: coreusage.Detail{
			InputTokens:  100,
			OutputTokens: 50,
			TotalTokens:  150,
		},
	}

	plugin.HandleUsage(context.Background(), record)

	count, err := plugin.RecordCount()
	if err != nil {
		t.Fatalf("failed to get record count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 record, got %d", count)
	}
}

func TestSQLitePlugin_RestoreToMemory(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_restore.db")
	plugin, err := NewSQLitePlugin(dbPath)
	if err != nil {
		t.Fatalf("failed to create plugin: %v", err)
	}

	records := []coreusage.Record{
		{
			Provider:    "openai",
			Model:       "gpt-4",
			APIKey:      "key-1",
			RequestedAt: time.Now().Add(-2 * time.Hour),
			Detail:      coreusage.Detail{TotalTokens: 100},
		},
		{
			Provider:    "anthropic",
			Model:       "claude-3",
			APIKey:      "key-2",
			RequestedAt: time.Now().Add(-1 * time.Hour),
			Failed:      true,
			Detail:      coreusage.Detail{TotalTokens: 200},
		},
		{
			Provider:    "openai",
			Model:       "gpt-4",
			APIKey:      "key-1",
			RequestedAt: time.Now(),
			Detail:      coreusage.Detail{TotalTokens: 150},
		},
	}

	for _, r := range records {
		plugin.HandleUsage(context.Background(), r)
	}
	plugin.Close()

	plugin2, err := NewSQLitePlugin(dbPath)
	if err != nil {
		t.Fatalf("failed to reopen plugin: %v", err)
	}
	defer plugin2.Close()

	stats := NewRequestStatistics()
	if err := plugin2.RestoreToMemory(stats); err != nil {
		t.Fatalf("failed to restore: %v", err)
	}

	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 3 {
		t.Errorf("expected 3 total requests, got %d", snapshot.TotalRequests)
	}
	if snapshot.SuccessCount != 2 {
		t.Errorf("expected 2 success, got %d", snapshot.SuccessCount)
	}
	if snapshot.FailureCount != 1 {
		t.Errorf("expected 1 failure, got %d", snapshot.FailureCount)
	}
	if snapshot.TotalTokens != 450 {
		t.Errorf("expected 450 total tokens, got %d", snapshot.TotalTokens)
	}
}

func TestSQLitePlugin_ConcurrentWrites(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_concurrent.db")
	plugin, err := NewSQLitePlugin(dbPath)
	if err != nil {
		t.Fatalf("failed to create plugin: %v", err)
	}
	defer plugin.Close()

	done := make(chan struct{})
	numGoroutines := 10
	recordsPerGoroutine := 100

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			for j := 0; j < recordsPerGoroutine; j++ {
				plugin.HandleUsage(context.Background(), coreusage.Record{
					Provider:    "test",
					Model:       "model",
					APIKey:      "key",
					RequestedAt: time.Now(),
					Detail:      coreusage.Detail{TotalTokens: 10},
				})
			}
			done <- struct{}{}
		}(i)
	}

	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	count, err := plugin.RecordCount()
	if err != nil {
		t.Fatalf("failed to get count: %v", err)
	}
	expected := int64(numGoroutines * recordsPerGoroutine)
	if count != expected {
		t.Errorf("expected %d records, got %d", expected, count)
	}
}

func TestSQLitePlugin_PersistenceAcrossRestarts(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_persistence.db")

	plugin1, err := NewSQLitePlugin(dbPath)
	if err != nil {
		t.Fatalf("failed to create plugin: %v", err)
	}

	plugin1.HandleUsage(context.Background(), coreusage.Record{
		Provider:    "openai",
		Model:       "gpt-4",
		APIKey:      "persistent-key",
		RequestedAt: time.Now(),
		Detail:      coreusage.Detail{TotalTokens: 500},
	})
	plugin1.Close()

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatal("database file should exist after close")
	}

	plugin2, err := NewSQLitePlugin(dbPath)
	if err != nil {
		t.Fatalf("failed to reopen plugin: %v", err)
	}
	defer plugin2.Close()

	count, err := plugin2.RecordCount()
	if err != nil {
		t.Fatalf("failed to get count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 record after restart, got %d", count)
	}
}

func TestSQLitePlugin_NilHandling(t *testing.T) {
	var plugin *SQLitePlugin
	plugin.HandleUsage(context.Background(), coreusage.Record{})
	if err := plugin.RestoreToMemory(nil); err != nil {
		t.Errorf("RestoreToMemory on nil should not error: %v", err)
	}
	if err := plugin.Close(); err != nil {
		t.Errorf("Close on nil should not error: %v", err)
	}
}
