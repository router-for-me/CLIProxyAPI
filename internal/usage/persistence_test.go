package usage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestPersistenceFlushAndRestore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stats", "usage-statistics.json")

	original := NewRequestStatistics()
	persistence, err := StartPersistence(original, path)
	if err != nil {
		t.Fatalf("StartPersistence() error = %v", err)
	}

	original.Record(context.Background(), coreusage.Record{
		APIKey:      "test-api",
		Model:       "gpt-test",
		RequestedAt: time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 5,
			TotalTokens:  15,
		},
	})
	if err := persistence.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}

	restored := NewRequestStatistics()
	restoredPersistence, err := StartPersistence(restored, path)
	if err != nil {
		t.Fatalf("StartPersistence() restore error = %v", err)
	}
	t.Cleanup(func() {
		_ = restoredPersistence.Stop()
	})

	snapshot := restored.Snapshot()
	if snapshot.TotalRequests != 1 {
		t.Fatalf("TotalRequests = %d, want 1", snapshot.TotalRequests)
	}
	apiSnapshot, ok := snapshot.APIs["test-api"]
	if !ok {
		t.Fatalf("expected API snapshot for test-api")
	}
	modelSnapshot, ok := apiSnapshot.Models["gpt-test"]
	if !ok {
		t.Fatalf("expected model snapshot for gpt-test")
	}
	if modelSnapshot.TotalTokens != 15 {
		t.Fatalf("TotalTokens = %d, want 15", modelSnapshot.TotalTokens)
	}
}
