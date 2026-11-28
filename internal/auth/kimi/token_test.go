package kimi

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/MoeForge/moego/json"
)

func TestSaveTokenToFile_ReplacesAtomically(t *testing.T) {
	tempDir := t.TempDir()
	authFilePath := filepath.Join(tempDir, "kimi-atomic.json")
	initialRaw := []byte(`{"type":"kimi","access_token":"old-access","refresh_token":"old-refresh"}`)
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

	storage := &KimiTokenStorage{
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
		TokenType:    "Bearer",
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
	if len(entries) != 1 || entries[0].Name() != "kimi-atomic.json" {
		t.Fatalf("unexpected files left in auth dir: %v", entries)
	}
}
