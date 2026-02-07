package usage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveLocalUsageDBPath(t *testing.T) {
	authDir := filepath.Join(t.TempDir(), "auth")

	t.Setenv("PGSTORE_LOCAL_PATH", filepath.Join(t.TempDir(), "pglocal"))
	got := resolveLocalUsageDBPath(authDir)
	want := filepath.Join(getEnvOrFatal(t, "PGSTORE_LOCAL_PATH"), defaultLocalUsageFileName)
	if got != want {
		t.Fatalf("unexpected local db path: got %q want %q", got, want)
	}

	t.Setenv("PGSTORE_LOCAL_PATH", filepath.Join(t.TempDir(), "custom.db"))
	got = resolveLocalUsageDBPath(authDir)
	want = getEnvOrFatal(t, "PGSTORE_LOCAL_PATH")
	if got != want {
		t.Fatalf("unexpected db file path: got %q want %q", got, want)
	}

	t.Setenv("PGSTORE_LOCAL_PATH", "")
	got = resolveLocalUsageDBPath(authDir)
	want = filepath.Join(authDir, defaultLocalUsageFileName)
	if got != want {
		t.Fatalf("unexpected fallback db path: got %q want %q", got, want)
	}
}

func TestSQLiteUsageStoreReset(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "sqlite", "usage.db")

	store, err := newSQLiteUsageStoreAtPath(dbPath)
	if err != nil {
		t.Fatalf("newSQLiteUsageStoreAtPath failed: %v", err)
	}
	defer store.Close()

	err = store.Insert(ctx, UsageRecord{
		APIKey:      "api-1",
		Model:       "model-1",
		Source:      "source-1",
		AuthIndex:   "0",
		Failed:      false,
		RequestedAt: time.Now(),
		TotalTokens: 10,
	})
	if err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	details, err := store.GetDetails(ctx, 0, 10)
	if err != nil {
		t.Fatalf("GetDetails before reset failed: %v", err)
	}
	if len(details) != 1 {
		t.Fatalf("unexpected detail count before reset: got %d want 1", len(details))
	}

	if err = store.Reset(ctx); err != nil {
		t.Fatalf("Reset failed: %v", err)
	}

	details, err = store.GetDetails(ctx, 0, 10)
	if err != nil {
		t.Fatalf("GetDetails after reset failed: %v", err)
	}
	if len(details) != 0 {
		t.Fatalf("unexpected detail count after reset: got %d want 0", len(details))
	}
}

func getEnvOrFatal(t *testing.T, key string) string {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		t.Fatalf("expected env %q to be set", key)
	}
	return value
}
