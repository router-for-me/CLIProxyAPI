package codex

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/MoeForge/moego/json"
)

func TestSaveTokenToFile_PreservesCustomMetadata(t *testing.T) {
	tempDir := t.TempDir()
	authFilePath := filepath.Join(tempDir, "codex-test.json")

	storage := &CodexTokenStorage{
		Type:         "codex",
		Email:        "user@example.com",
		AccessToken:  "new-access-token",
		RefreshToken: "new-refresh-token",
		IDToken:      "new-id-token",
		AccountID:    "new-account",
		Expire:       "2026-12-31T23:59:59Z",
		LastRefresh:  "2026-04-14T12:00:00Z",
	}
	storage.SetMetadata(map[string]any{
		"disabled":   false,
		"prefix":     "my-prefix",
		"websockets": false,
		"note":       "my important note",
		"proxy_url":  "http://proxy:8080",
		"weight":     float64(42),
	})

	if errSave := storage.SaveTokenToFile(authFilePath); errSave != nil {
		t.Fatalf("SaveTokenToFile() error = %v", errSave)
	}

	savedRaw, errRead := os.ReadFile(authFilePath)
	if errRead != nil {
		t.Fatalf("os.ReadFile error = %v", errRead)
	}

	var saved map[string]any
	if errUnmarshal := json.Unmarshal(savedRaw, &saved); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal error = %v", errUnmarshal)
	}

	// Verify updated OAuth token fields
	if saved["access_token"] != "new-access-token" {
		t.Errorf("access_token = %v, want new-access-token", saved["access_token"])
	}
	if saved["refresh_token"] != "new-refresh-token" {
		t.Errorf("refresh_token = %v, want new-refresh-token", saved["refresh_token"])
	}
	if saved["id_token"] != "new-id-token" {
		t.Errorf("id_token = %v, want new-id-token", saved["id_token"])
	}
	if saved["account_id"] != "new-account" {
		t.Errorf("account_id = %v, want new-account", saved["account_id"])
	}

	// Verify custom fields in metadata
	if saved["prefix"] != "my-prefix" {
		t.Errorf("prefix = %v, want my-prefix", saved["prefix"])
	}
	if saved["websockets"] != false {
		t.Errorf("websockets = %v, want false", saved["websockets"])
	}
	if saved["note"] != "my important note" {
		t.Errorf("note = %v, want my important note", saved["note"])
	}
	if saved["proxy_url"] != "http://proxy:8080" {
		t.Errorf("proxy_url = %v, want http://proxy:8080", saved["proxy_url"])
	}
	if saved["weight"] != float64(42) {
		t.Errorf("weight = %v, want 42", saved["weight"])
	}
}

func TestWriteJSONFileAtomic_SyncsTempFileBeforeCloseAndRename(t *testing.T) {
	tempDir := t.TempDir()
	authFilePath := filepath.Join(tempDir, "codex-auth.json")
	initialRaw := []byte(`{"refresh_token":"old-refresh"}`)
	if errWrite := os.WriteFile(authFilePath, initialRaw, 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	origSync := syncTempCredentialFile
	t.Cleanup(func() {
		syncTempCredentialFile = origSync
	})

	syncCalls := 0
	syncTempCredentialFile = func(tmpFile *os.File) error {
		syncCalls++

		// Target file must still hold the previous contents when Sync runs (before Rename).
		currentTarget, errReadTarget := os.ReadFile(authFilePath)
		if errReadTarget != nil {
			t.Errorf("os.ReadFile(target) during sync error = %v", errReadTarget)
		} else if string(currentTarget) != string(initialRaw) {
			t.Errorf("target content during sync = %s, want %s", currentTarget, initialRaw)
		}

		// Temp file must already hold the marshaled payload when Sync runs (after Write).
		tmpRaw, errReadTmp := os.ReadFile(tmpFile.Name())
		if errReadTmp != nil {
			t.Errorf("os.ReadFile(tmp) during sync error = %v", errReadTmp)
		} else {
			var pending map[string]any
			if errUnmarshal := json.Unmarshal(tmpRaw, &pending); errUnmarshal != nil {
				t.Errorf("json.Unmarshal(tmp) during sync error = %v", errUnmarshal)
			} else if pending["refresh_token"] != "new-refresh" || pending["access_token"] != "new-access" {
				t.Errorf("temp payload during sync = %v, want new-refresh/new-access", pending)
			}
		}

		// Delegate to the real os.File.Sync to confirm the file handle is still open (before Close).
		return origSync(tmpFile)
	}

	if errWrite := WriteJSONFileAtomic(authFilePath, map[string]any{
		"refresh_token": "new-refresh",
		"access_token":  "new-access",
	}); errWrite != nil {
		t.Fatalf("WriteJSONFileAtomic() error = %v", errWrite)
	}

	if syncCalls != 1 {
		t.Fatalf("sync call count = %d, want 1", syncCalls)
	}

	savedRaw, errRead := os.ReadFile(authFilePath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(savedRaw, &saved); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	if saved["refresh_token"] != "new-refresh" || saved["access_token"] != "new-access" {
		t.Fatalf("saved payload = %v, want new-refresh/new-access", saved)
	}

	if runtime.GOOS != "windows" {
		info, errStat := os.Stat(authFilePath)
		if errStat != nil {
			t.Fatalf("os.Stat() error = %v", errStat)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("file permissions = %o, want 600", perm)
		}
	}

	entries, errEntries := os.ReadDir(tempDir)
	if errEntries != nil {
		t.Fatalf("os.ReadDir() error = %v", errEntries)
	}
	if len(entries) != 1 || entries[0].Name() != "codex-auth.json" {
		t.Fatalf("unexpected leftover files in dir: %v", entries)
	}
}

func TestWriteJSONFileAtomic_SyncErrorCleansUpTempFileAndPreservesExisting(t *testing.T) {
	tempDir := t.TempDir()
	authFilePath := filepath.Join(tempDir, "codex-auth.json")
	initialRaw := []byte(`{"refresh_token":"old-refresh"}`)
	if errWrite := os.WriteFile(authFilePath, initialRaw, 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	origSync := syncTempCredentialFile
	t.Cleanup(func() {
		syncTempCredentialFile = origSync
	})

	errSyncBoom := errors.New("injected sync failure")
	var syncedFile *os.File
	syncTempCredentialFile = func(tmpFile *os.File) error {
		syncedFile = tmpFile
		return errSyncBoom
	}

	errWrite := WriteJSONFileAtomic(authFilePath, map[string]any{
		"refresh_token": "rotated-refresh",
	})
	if !errors.Is(errWrite, errSyncBoom) {
		t.Fatalf("WriteJSONFileAtomic() error = %v, want wrapped %v", errWrite, errSyncBoom)
	}

	if syncedFile == nil {
		t.Fatal("expected sync hook to receive temp file handle")
	}
	if errCloseAgain := syncedFile.Close(); !errors.Is(errCloseAgain, os.ErrClosed) {
		t.Fatalf("temp file handle was not closed after sync failure: second Close() error = %v", errCloseAgain)
	}

	preservedRaw, errRead := os.ReadFile(authFilePath)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	if string(preservedRaw) != string(initialRaw) {
		t.Fatalf("existing credential file modified on sync failure: got %s, want %s", preservedRaw, initialRaw)
	}

	entries, errEntries := os.ReadDir(tempDir)
	if errEntries != nil {
		t.Fatalf("os.ReadDir() error = %v", errEntries)
	}
	if len(entries) != 1 || entries[0].Name() != "codex-auth.json" {
		t.Fatalf("temp file not cleaned up after sync failure; dir entries: %v", entries)
	}
}
