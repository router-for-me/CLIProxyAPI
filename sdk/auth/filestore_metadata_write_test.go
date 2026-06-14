package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAuthMetadataFile_WritesAtomicallyWithoutTempLeftover(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codex.json")
	// Seed an existing file to ensure it is replaced, not appended to.
	if err := os.WriteFile(path, []byte(`{"old":true}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	meta := map[string]any{"type": "codex", "access_token": "tok"}
	if err := writeAuthMetadataFile(path, meta); err != nil {
		t.Fatalf("writeAuthMetadataFile: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal %q: %v", string(raw), err)
	}
	if got["access_token"] != "tok" || got["type"] != "codex" {
		t.Fatalf("unexpected content: %s", string(raw))
	}
	if _, stale := got["old"]; stale {
		t.Fatalf("old content not replaced: %s", string(raw))
	}

	// The atomic rename must not leave the sibling temp file behind.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file should not remain after atomic write (err=%v)", err)
	}
}
