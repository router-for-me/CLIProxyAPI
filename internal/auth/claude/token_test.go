package claude

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/MoeForge/moego/json"
)

func TestSaveTokenToFile_PreservesCustomMetadata(t *testing.T) {
	tempDir := t.TempDir()
	authFilePath := filepath.Join(tempDir, "claude-test.json")

	storage := &ClaudeTokenStorage{
		Type:         "claude",
		Email:        "user@example.com",
		AccessToken:  "new-claude-access",
		RefreshToken: "new-claude-refresh",
		Expire:       "2026-12-31T23:59:59Z",
		LastRefresh:  "2026-04-14T12:00:00Z",
	}
	storage.SetMetadata(map[string]any{
		"disabled":  false,
		"prefix":    "claude-prefix",
		"note":      "claude custom note",
		"proxy_url": "http://proxy:8080",
		"weight":    float64(5),
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

	if saved["access_token"] != "new-claude-access" {
		t.Errorf("access_token = %v, want new-claude-access", saved["access_token"])
	}
	if saved["prefix"] != "claude-prefix" {
		t.Errorf("prefix = %v, want claude-prefix", saved["prefix"])
	}
	if saved["note"] != "claude custom note" {
		t.Errorf("note = %v, want claude custom note", saved["note"])
	}
	if saved["proxy_url"] != "http://proxy:8080" {
		t.Errorf("proxy_url = %v, want http://proxy:8080", saved["proxy_url"])
	}
	if saved["weight"] != float64(5) {
		t.Errorf("weight = %v, want 5", saved["weight"])
	}
}

func TestSaveTokenToFile_ReplacesAtomically(t *testing.T) {
	tempDir := t.TempDir()
	authFilePath := filepath.Join(tempDir, "claude-atomic.json")
	initialRaw := []byte(`{"type":"claude","access_token":"old-access","refresh_token":"old-refresh"}`)
	if errWrite := os.WriteFile(authFilePath, initialRaw, 0o600); errWrite != nil {
		t.Fatalf("seed auth file: %v", errWrite)
	}

	var beforeInfo os.FileInfo
	var oldFile *os.File
	if runtime.GOOS != "windows" {
		var errStatBefore error
		beforeInfo, errStatBefore = os.Stat(authFilePath)
		if errStatBefore != nil {
			t.Fatalf("stat before save: %v", errStatBefore)
		}
		var errOpenOld error
		oldFile, errOpenOld = os.Open(authFilePath)
		if errOpenOld != nil {
			t.Fatalf("open existing auth file: %v", errOpenOld)
		}
		defer func() {
			if errClose := oldFile.Close(); errClose != nil {
				t.Errorf("close old file handle: %v", errClose)
			}
		}()
	}

	storage := &ClaudeTokenStorage{
		Email:        "user@example.com",
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
	}
	if errSave := storage.SaveTokenToFile(authFilePath); errSave != nil {
		t.Fatalf("SaveTokenToFile() error = %v", errSave)
	}

	afterInfo, errStatAfter := os.Stat(authFilePath)
	if errStatAfter != nil {
		t.Fatalf("stat after save: %v", errStatAfter)
	}
	if runtime.GOOS != "windows" {
		if os.SameFile(beforeInfo, afterInfo) {
			t.Fatalf("SaveTokenToFile() overwrote existing auth file in place instead of atomic rename")
		}
		if gotPerm := afterInfo.Mode().Perm(); gotPerm != 0o600 {
			t.Fatalf("saved auth file mode = %o, want 0600", gotPerm)
		}
		oldContent, errReadOld := io.ReadAll(oldFile)
		if errReadOld != nil {
			t.Fatalf("read old file handle: %v", errReadOld)
		}
		if string(oldContent) != string(initialRaw) {
			t.Fatalf("existing file handle saw mutated content %q, want original %q", oldContent, initialRaw)
		}
	}

	savedRaw, errRead := os.ReadFile(authFilePath)
	if errRead != nil {
		t.Fatalf("read saved auth file: %v", errRead)
	}
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(savedRaw, &saved); errUnmarshal != nil {
		t.Fatalf("unmarshal saved auth file: %v", errUnmarshal)
	}
	if saved["access_token"] != "new-access" || saved["refresh_token"] != "new-refresh" {
		t.Fatalf("saved tokens = %v, want new-access/new-refresh", saved)
	}

	entries, errReadDir := os.ReadDir(tempDir)
	if errReadDir != nil {
		t.Fatalf("read temp dir: %v", errReadDir)
	}
	if len(entries) != 1 || entries[0].Name() != "claude-atomic.json" {
		t.Fatalf("unexpected files left in auth dir: %v", entries)
	}
}
