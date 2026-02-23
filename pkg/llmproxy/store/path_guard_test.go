package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestObjectTokenStoreSaveRejectsPathOutsideAuthDir(t *testing.T) {
	t.Parallel()

	authDir := filepath.Join(t.TempDir(), "auths")
	store := &ObjectTokenStore{authDir: authDir}
	outside := filepath.Join(t.TempDir(), "outside.json")
	auth := &cliproxyauth.Auth{
		ID:       "outside",
		Disabled: true,
		Attributes: map[string]string{
			"path": outside,
		},
	}

	_, err := store.Save(context.Background(), auth)
	if err == nil {
		t.Fatal("expected error for path outside managed auth directory")
	}
	if !strings.Contains(err.Error(), "path escapes managed directory") {
		t.Fatalf("expected managed directory error, got: %v", err)
	}
}

func TestGitTokenStoreSaveRejectsPathOutsideAuthDir(t *testing.T) {
	t.Parallel()

	baseDir := filepath.Join(t.TempDir(), "repo", "auths")
	store := NewGitTokenStore("", "", "")
	store.SetBaseDir(baseDir)
	outside := filepath.Join(t.TempDir(), "outside.json")
	auth := &cliproxyauth.Auth{
		ID: "outside",
		Attributes: map[string]string{
			"path": outside,
		},
		Metadata: map[string]any{"type": "test"},
	}

	_, err := store.Save(context.Background(), auth)
	if err == nil {
		t.Fatal("expected error for path outside managed auth directory")
	}
	if !strings.Contains(err.Error(), "path escapes managed directory") {
		t.Fatalf("expected managed directory error, got: %v", err)
	}
}
