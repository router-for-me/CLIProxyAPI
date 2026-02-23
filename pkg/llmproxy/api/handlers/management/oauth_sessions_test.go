package management

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteOAuthCallbackFile_WritesInsideAuthDir(t *testing.T) {
	authDir := t.TempDir()
	state := "safe-state-123"

	filePath, err := WriteOAuthCallbackFile(authDir, "claude", state, "code-1", "")
	if err != nil {
		t.Fatalf("WriteOAuthCallbackFile failed: %v", err)
	}

	authDirAbs, err := filepath.Abs(authDir)
	if err != nil {
		t.Fatalf("resolve auth dir: %v", err)
	}
	filePathAbs, err := filepath.Abs(filePath)
	if err != nil {
		t.Fatalf("resolve callback path: %v", err)
	}
	prefix := authDirAbs + string(os.PathSeparator)
	if filePathAbs != authDirAbs && !strings.HasPrefix(filePathAbs, prefix) {
		t.Fatalf("callback path escaped auth dir: %q", filePathAbs)
	}

	content, err := os.ReadFile(filePathAbs)
	if err != nil {
		t.Fatalf("read callback file: %v", err)
	}
	var payload oauthCallbackFilePayload
	if err := json.Unmarshal(content, &payload); err != nil {
		t.Fatalf("unmarshal callback file: %v", err)
	}
	if payload.State != state {
		t.Fatalf("unexpected state: got %q want %q", payload.State, state)
	}
}

func TestSanitizeOAuthCallbackPath_RejectsInjectedFileName(t *testing.T) {
	_, err := sanitizeOAuthCallbackPath(t.TempDir(), "../escape.oauth")
	if err == nil {
		t.Fatal("expected error for injected callback file name")
	}
}
