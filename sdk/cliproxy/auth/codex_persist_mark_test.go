package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// PersistCodexAuthFile marks the local write itself, as soon as the file holds the
// new content (or already did), before any bookkeeping that follows the write.
func TestPersistCodexAuthFileMarksWriteOnceFileHoldsContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex.json")
	var marks int
	var atMark map[string]any
	ctx := context.WithValue(context.Background(), authWriteMarkKey{}, func() {
		marks++
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read file at mark: %v", err)
			return
		}
		atMark = nil
		if err := json.Unmarshal(data, &atMark); err != nil {
			t.Errorf("parse file at mark: %v", err)
		}
	})
	persist := func(prefix string) {
		t.Helper()
		auth := &Auth{
			ID:       "codex.json",
			Provider: "codex",
			Metadata: map[string]any{"type": "codex", "prefix": prefix},
		}
		handled, err := PersistCodexAuthFile(ctx, path, auth)
		if !handled || err != nil {
			t.Fatalf("PersistCodexAuthFile() handled=%v err=%v", handled, err)
		}
	}

	persist("pfx-1")
	if marks != 1 || atMark["prefix"] != "pfx-1" {
		t.Fatalf("after write: marks=%d file at mark=%v, want 1 mark with pfx-1 on disk", marks, atMark)
	}
	// Unchanged content skips the write but is still the saved state.
	persist("pfx-1")
	if marks != 2 || atMark["prefix"] != "pfx-1" {
		t.Fatalf("after no-op: marks=%d file at mark=%v, want 2 marks with pfx-1 on disk", marks, atMark)
	}
	persist("pfx-2")
	if marks != 3 || atMark["prefix"] != "pfx-2" {
		t.Fatalf("after rewrite: marks=%d file at mark=%v, want 3 marks with pfx-2 on disk", marks, atMark)
	}
}
