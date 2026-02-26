package store

import (
	"path/filepath"
	"strings"
	"testing"

	cliproxyauth "github.com/kooshapari/cliproxyapi-plusplus/v6/sdk/cliproxy/auth"
)

func TestObjectResolveAuthPathRejectsTraversalFromAttributes(t *testing.T) {
	t.Parallel()

	store := &ObjectTokenStore{authDir: filepath.Join(t.TempDir(), "auths")}
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"path": "../escape.json"},
	}
	if _, err := store.resolveAuthPath(auth); err == nil {
		t.Fatalf("expected traversal path rejection")
	}
}

func TestObjectResolveAuthPathRejectsAbsoluteOutsideAuthDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := &ObjectTokenStore{authDir: filepath.Join(root, "auths")}
	outside := filepath.Join(root, "..", "outside.json")
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"path": outside},
	}
	if _, err := store.resolveAuthPath(auth); err == nil {
		t.Fatalf("expected outside absolute path rejection")
	}
}

func TestObjectResolveDeletePathConstrainsToAuthDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	authDir := filepath.Join(root, "auths")
	store := &ObjectTokenStore{authDir: authDir}

	got, err := store.resolveDeletePath("team/provider")
	if err != nil {
		t.Fatalf("resolve delete path: %v", err)
	}
	if !strings.HasSuffix(got, filepath.Join("team", "provider.json")) {
		t.Fatalf("expected .json suffix, got %s", got)
	}
	rel, err := filepath.Rel(authDir, got)
	if err != nil {
		t.Fatalf("relative path: %v", err)
	}
	if strings.HasPrefix(rel, "..") || rel == "." {
		t.Fatalf("path escaped auth directory: %s", got)
	}
}
