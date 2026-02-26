package store

import (
	"path/filepath"
	"strings"
	"testing"

	cliproxyauth "github.com/kooshapari/cliproxyapi-plusplus/v6/sdk/cliproxy/auth"
)

func TestPostgresResolveAuthPathRejectsTraversalFromFileName(t *testing.T) {
	t.Parallel()

	store := &PostgresStore{authDir: filepath.Join(t.TempDir(), "auths")}
	auth := &cliproxyauth.Auth{FileName: "../escape.json"}
	if _, err := store.resolveAuthPath(auth); err == nil {
		t.Fatalf("expected traversal path rejection")
	}
}

func TestPostgresResolveAuthPathRejectsAbsoluteOutsideAuthDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := &PostgresStore{authDir: filepath.Join(root, "auths")}
	outside := filepath.Join(root, "..", "outside.json")
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"path": outside}}
	if _, err := store.resolveAuthPath(auth); err == nil {
		t.Fatalf("expected outside absolute path rejection")
	}
}

func TestPostgresResolveDeletePathConstrainsToAuthDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	authDir := filepath.Join(root, "auths")
	store := &PostgresStore{authDir: authDir}

	got, err := store.resolveDeletePath("team/provider.json")
	if err != nil {
		t.Fatalf("resolve delete path: %v", err)
	}
	rel, err := filepath.Rel(authDir, got)
	if err != nil {
		t.Fatalf("relative path: %v", err)
	}
	if strings.HasPrefix(rel, "..") || rel == "." {
		t.Fatalf("path escaped auth directory: %s", got)
	}
}
