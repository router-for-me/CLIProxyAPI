package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileTokenStore_List_PriorityFromMetadata(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.json"), []byte(`{"type":"gemini","priority":3}`), 0o600); err != nil {
		t.Fatalf("write a.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.json"), []byte(`{"type":"gemini","priority":10}`), 0o600); err != nil {
		t.Fatalf("write b.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c.json"), []byte(`{"type":"gemini"}`), 0o600); err != nil {
		t.Fatalf("write c.json: %v", err)
	}

	store := NewFileTokenStore()
	store.SetBaseDir(dir)
	list, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	byName := make(map[string]map[string]string, len(list))
	for _, a := range list {
		if a == nil {
			continue
		}
		byName[a.FileName] = a.Attributes
	}

	if got := byName["a.json"]["priority"]; got != "3" {
		t.Fatalf("a.json priority = %q, want %q", got, "3")
	}
	if got := byName["b.json"]["priority"]; got != "3" {
		t.Fatalf("b.json priority = %q, want %q", got, "3")
	}
	if _, ok := byName["c.json"]["priority"]; ok {
		t.Fatalf("c.json priority unexpectedly present")
	}
}
