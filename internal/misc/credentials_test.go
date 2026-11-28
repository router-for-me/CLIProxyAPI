package misc

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMergeMetadata(t *testing.T) {
	source := map[string]any{
		"type":         "codex",
		"access_token": "token-123",
	}
	metadata := map[string]any{
		"disabled":   false,
		"email":      "test@example.com",
		"prefix":     "custom-prefix",
		"websockets": false,
		"note":       "custom note",
	}

	result, err := MergeMetadata(source, metadata)
	if err != nil {
		t.Fatalf("MergeMetadata() error = %v", err)
	}

	if result["type"] != "codex" {
		t.Errorf("type = %v, want codex", result["type"])
	}
	if result["access_token"] != "token-123" {
		t.Errorf("access_token = %v, want token-123", result["access_token"])
	}
	if result["disabled"] != false {
		t.Errorf("disabled = %v, want false", result["disabled"])
	}
	if result["email"] != "test@example.com" {
		t.Errorf("email = %v, want test@example.com", result["email"])
	}
	if result["prefix"] != "custom-prefix" {
		t.Errorf("prefix = %v, want custom-prefix", result["prefix"])
	}
	if result["websockets"] != false {
		t.Errorf("websockets = %v, want false", result["websockets"])
	}
	if result["note"] != "custom note" {
		t.Errorf("note = %v, want custom note", result["note"])
	}
}

func TestWriteFileAtomic_SyncsTempFileBeforeCloseAndRename(t *testing.T) {
	tempDir := t.TempDir()
	targetPath := filepath.Join(tempDir, "auth.json")
	initialRaw := []byte(`{"refresh_token":"old-refresh"}`)
	if errWrite := os.WriteFile(targetPath, initialRaw, 0o600); errWrite != nil {
		t.Fatalf("seed target file: %v", errWrite)
	}

	origSync := syncAtomicTempFile
	t.Cleanup(func() {
		syncAtomicTempFile = origSync
	})

	syncCalls := 0
	syncAtomicTempFile = func(file *os.File) error {
		syncCalls++

		if strings.HasSuffix(strings.ToLower(file.Name()), ".json") {
			t.Errorf("temp file %q must not end with .json", file.Name())
		}

		currentTarget, errReadTarget := os.ReadFile(targetPath)
		if errReadTarget != nil {
			t.Errorf("read target during sync: %v", errReadTarget)
		} else if string(currentTarget) != string(initialRaw) {
			t.Errorf("target content during sync = %q, want %q", currentTarget, initialRaw)
		}

		tmpRaw, errReadTmp := os.ReadFile(file.Name())
		if errReadTmp != nil {
			t.Errorf("read temp during sync: %v", errReadTmp)
		} else if string(tmpRaw) != `{"refresh_token":"new-refresh"}` {
			t.Errorf("temp content during sync = %q, want new payload", tmpRaw)
		}

		return origSync(file)
	}

	newRaw := []byte(`{"refresh_token":"new-refresh"}`)
	if errWrite := WriteFileAtomic(targetPath, newRaw, 0o600); errWrite != nil {
		t.Fatalf("WriteFileAtomic() error = %v", errWrite)
	}
	if syncCalls != 1 {
		t.Fatalf("sync call count = %d, want 1", syncCalls)
	}

	savedRaw, errRead := os.ReadFile(targetPath)
	if errRead != nil {
		t.Fatalf("read target after write: %v", errRead)
	}
	if string(savedRaw) != string(newRaw) {
		t.Fatalf("target content = %q, want %q", savedRaw, newRaw)
	}
	if runtime.GOOS != "windows" {
		info, errStat := os.Stat(targetPath)
		if errStat != nil {
			t.Fatalf("stat target: %v", errStat)
		}
		if gotPerm := info.Mode().Perm(); gotPerm != 0o600 {
			t.Fatalf("target perm = %o, want 0600", gotPerm)
		}
	}

	entries, errReadDir := os.ReadDir(tempDir)
	if errReadDir != nil {
		t.Fatalf("read dir: %v", errReadDir)
	}
	if len(entries) != 1 || entries[0].Name() != "auth.json" {
		t.Fatalf("unexpected files in dir: %v", entries)
	}
}

func TestWriteFileAtomic_SyncFailurePreservesExistingAndCleansTempFile(t *testing.T) {
	tempDir := t.TempDir()
	targetPath := filepath.Join(tempDir, "auth.json")
	initialRaw := []byte(`{"refresh_token":"keep-me"}`)
	if errWrite := os.WriteFile(targetPath, initialRaw, 0o600); errWrite != nil {
		t.Fatalf("seed target file: %v", errWrite)
	}

	origSync := syncAtomicTempFile
	t.Cleanup(func() {
		syncAtomicTempFile = origSync
	})

	syncErr := errors.New("simulated disk full")
	syncAtomicTempFile = func(_ *os.File) error {
		return syncErr
	}

	errWrite := WriteFileAtomic(targetPath, []byte(`{"refresh_token":"lost"}`), 0o600)
	if !errors.Is(errWrite, syncErr) {
		t.Fatalf("WriteFileAtomic() error = %v, want %v", errWrite, syncErr)
	}

	currentRaw, errRead := os.ReadFile(targetPath)
	if errRead != nil {
		t.Fatalf("read target after failed write: %v", errRead)
	}
	if string(currentRaw) != string(initialRaw) {
		t.Fatalf("target content after sync failure = %q, want %q", currentRaw, initialRaw)
	}

	entries, errReadDir := os.ReadDir(tempDir)
	if errReadDir != nil {
		t.Fatalf("read dir: %v", errReadDir)
	}
	if len(entries) != 1 || entries[0].Name() != "auth.json" {
		t.Fatalf("temp file leaked after sync failure: %v", entries)
	}
}

func TestWriteFileAtomic_CreatesNewFileAndParentDir(t *testing.T) {
	targetPath := filepath.Join(t.TempDir(), "nested", "new-auth.json")
	payload := []byte(`{"type":"test"}`)

	if errWrite := WriteFileAtomic(targetPath, payload, 0o600); errWrite != nil {
		t.Fatalf("WriteFileAtomic() error = %v", errWrite)
	}

	savedRaw, errRead := os.ReadFile(targetPath)
	if errRead != nil {
		t.Fatalf("read new target: %v", errRead)
	}
	if string(savedRaw) != string(payload) {
		t.Fatalf("new target content = %q, want %q", savedRaw, payload)
	}
	if runtime.GOOS != "windows" {
		info, errStat := os.Stat(targetPath)
		if errStat != nil {
			t.Fatalf("stat new target: %v", errStat)
		}
		if gotPerm := info.Mode().Perm(); gotPerm != 0o600 {
			t.Fatalf("new target perm = %o, want 0600", gotPerm)
		}
	}
}

func TestWriteFileAtomic_EmptyPathReturnsError(t *testing.T) {
	if errWrite := WriteFileAtomic("   ", []byte("{}"), 0o600); errWrite == nil {
		t.Fatal("WriteFileAtomic() with empty path succeeded, want error")
	}
}
