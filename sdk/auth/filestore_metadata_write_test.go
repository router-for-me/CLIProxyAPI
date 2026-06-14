package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestList_HonorsContextCancellationDuringCodexEnrichment(t *testing.T) {
	dir := t.TempDir()
	// A codex auth with an expired subscription forces the backend enrichment
	// path (which would otherwise wait up to 20s on a slow/unreachable host).
	seed := `{"type":"codex","access_token":"a","id_token":"","subscription_active_until":"2000-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, "codex.json"), []byte(seed), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	store := NewFileTokenStore()
	store.SetBaseDir(dir)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	done := make(chan struct{})
	go func() {
		_, _ = store.List(ctx)
		close(done)
	}()

	select {
	case <-done:
		// Returned promptly: the enrichment honored the cancelled context.
	case <-time.After(5 * time.Second):
		t.Fatal("List did not return promptly under a cancelled context (enrichment ignored ctx)")
	}
}
