package store

import (
	"context"
	"path/filepath"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestObjectTokenStoreRejectsAuthPathsOutsideAuthDir(t *testing.T) {
	authDir := t.TempDir()
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "token.json")

	store := &ObjectTokenStore{authDir: authDir}

	if _, err := store.resolveAuthPath(&cliproxyauth.Auth{
		ID:         "outside",
		Attributes: map[string]string{"path": outsidePath},
	}); err == nil {
		t.Fatal("resolveAuthPath accepted absolute path outside auth dir")
	}
	if _, err := store.resolveAuthPath(&cliproxyauth.Auth{
		ID:       "traversal",
		FileName: "../token.json",
	}); err == nil {
		t.Fatal("resolveAuthPath accepted path traversal")
	}
	if _, err := store.resolveDeletePath(outsidePath); err == nil {
		t.Fatal("resolveDeletePath accepted absolute path outside auth dir")
	}
	if err := store.uploadAuth(context.Background(), outsidePath); err == nil {
		t.Fatal("uploadAuth accepted absolute path outside auth dir")
	}
	if err := store.deleteAuthObject(context.Background(), outsidePath); err == nil {
		t.Fatal("deleteAuthObject accepted absolute path outside auth dir")
	}
}

func TestObjectTokenStoreAcceptsNestedAuthPathInsideAuthDir(t *testing.T) {
	authDir := t.TempDir()
	store := &ObjectTokenStore{authDir: authDir}

	got, err := store.resolveAuthPath(&cliproxyauth.Auth{
		ID:       "nested",
		FileName: "team/token",
	})
	if err != nil {
		t.Fatalf("resolveAuthPath returned error: %v", err)
	}
	want := filepath.Join(authDir, "team", "token.json")
	if got != want {
		t.Fatalf("resolveAuthPath = %q, want %q", got, want)
	}
}
