package store

import (
	"path/filepath"
	"testing"
)

func TestObjectTokenStoreResolveDeletePathRejectsEscapeInputs(t *testing.T) {
	t.Parallel()

	store := &ObjectTokenStore{authDir: t.TempDir()}
	absolute := filepath.Join(t.TempDir(), "outside.json")
	cases := []string{
		"../outside.json",
		absolute,
	}
	for _, id := range cases {
		if _, err := store.resolveDeletePath(id); err == nil {
			t.Fatalf("expected id %q to be rejected", id)
		}
	}
}

func TestObjectTokenStoreResolveDeletePathReturnsManagedJSONPath(t *testing.T) {
	t.Parallel()

	authDir := t.TempDir()
	store := &ObjectTokenStore{authDir: authDir}

	got, err := store.resolveDeletePath("nested/provider")
	if err != nil {
		t.Fatalf("resolve delete path: %v", err)
	}
	want := filepath.Join(authDir, "nested", "provider.json")
	if got != want {
		t.Fatalf("resolve delete path mismatch: got %q want %q", got, want)
	}
}
