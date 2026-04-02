package logging

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnforceLogDirSizeLimitDeletesOldest(t *testing.T) {
	dir := t.TempDir()

	writeLogFile(t, filepath.Join(dir, "old.log"), 60, time.Unix(1, 0))
	writeLogFile(t, filepath.Join(dir, "mid.log"), 60, time.Unix(2, 0))
	protected := filepath.Join(dir, "main.log")
	writeLogFile(t, protected, 60, time.Unix(3, 0))

	deleted, err := enforceLogDirSizeLimit(dir, 120, protected)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 deleted file, got %d", deleted)
	}

	if _, err := os.Stat(filepath.Join(dir, "old.log")); !os.IsNotExist(err) {
		t.Fatalf("expected old.log to be removed, stat error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mid.log")); err != nil {
		t.Fatalf("expected mid.log to remain, stat error: %v", err)
	}
	if _, err := os.Stat(protected); err != nil {
		t.Fatalf("expected protected main.log to remain, stat error: %v", err)
	}
}

func TestEnforceLogDirSizeLimitSkipsProtected(t *testing.T) {
	dir := t.TempDir()

	protected := filepath.Join(dir, "main.log")
	writeLogFile(t, protected, 200, time.Unix(1, 0))
	writeLogFile(t, filepath.Join(dir, "other.log"), 50, time.Unix(2, 0))

	deleted, err := enforceLogDirSizeLimit(dir, 100, protected)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 deleted file, got %d", deleted)
	}

	if _, err := os.Stat(protected); err != nil {
		t.Fatalf("expected protected main.log to remain, stat error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "other.log")); !os.IsNotExist(err) {
		t.Fatalf("expected other.log to be removed, stat error: %v", err)
	}
}

func TestEnforceRequestLogRetentionDeletesExpiredAndExcess(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	old := filepath.Join(dir, "v1-responses-old.log")
	mid := filepath.Join(dir, "v1-responses-mid.log")
	newest := filepath.Join(dir, "v1-responses-new.log")
	mainLog := filepath.Join(dir, "main.log")
	mainRotated := filepath.Join(dir, "main-2026-04-03T10-00-00.log")

	writeLogFile(t, old, 10, now.Add(-72*time.Hour))
	writeLogFile(t, mid, 10, now.Add(-12*time.Hour))
	writeLogFile(t, newest, 10, now.Add(-1*time.Hour))
	writeLogFile(t, mainLog, 10, now.Add(-72*time.Hour))
	writeLogFile(t, mainRotated, 10, now.Add(-72*time.Hour))

	deleted, err := enforceRequestLogRetention(dir, 1, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("expected 2 deleted files, got %d", deleted)
	}

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("expected expired request log removed, stat err=%v", err)
	}
	if _, err := os.Stat(mid); !os.IsNotExist(err) {
		t.Fatalf("expected excess request log removed, stat err=%v", err)
	}
	if _, err := os.Stat(newest); err != nil {
		t.Fatalf("expected newest request log kept, stat err=%v", err)
	}
	if _, err := os.Stat(mainLog); err != nil {
		t.Fatalf("expected main.log kept, stat err=%v", err)
	}
	if _, err := os.Stat(mainRotated); err != nil {
		t.Fatalf("expected rotated main log kept, stat err=%v", err)
	}
}

func writeLogFile(t *testing.T, path string, size int, modTime time.Time) {
	t.Helper()

	data := make([]byte, size)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("set times: %v", err)
	}
}
