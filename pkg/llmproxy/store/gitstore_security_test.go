package store

import (
	"path/filepath"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestResolveDeletePath_RejectsTraversalAndAbsolute(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	s := &GitTokenStore{}
	s.SetBaseDir(baseDir)

	if _, err := s.resolveDeletePath("../outside.json"); err == nil {
		t.Fatalf("expected traversal id to be rejected")
	}
	if _, err := s.resolveDeletePath(filepath.Join(baseDir, "nested", "token.json")); err == nil {
		t.Fatalf("expected absolute id to be rejected")
	}
}

func TestResolveDeletePath_ReturnsPathInsideBaseDir(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	s := &GitTokenStore{}
	s.SetBaseDir(baseDir)

	path, err := s.resolveDeletePath("nested/token.json")
	if err != nil {
		t.Fatalf("resolveDeletePath failed: %v", err)
	}
	rel, err := filepath.Rel(baseDir, path)
	if err != nil {
		t.Fatalf("filepath.Rel failed: %v", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("resolved path escaped base dir: %s", path)
	}
}

func TestResolveAuthPath_RejectsTraversalPath(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	s := &GitTokenStore{}
	s.SetBaseDir(baseDir)

	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"path": "../escape.json"},
		ID:         "ignored",
	}
	if _, err := s.resolveAuthPath(auth); err == nil {
		t.Fatalf("expected traversal path to be rejected")
	}
}

func TestResolveAuthPath_UsesManagedDirAndRejectsOutsidePath(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	s := &GitTokenStore{}
	s.SetBaseDir(baseDir)

	outside := filepath.Join(baseDir, "..", "outside.json")
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"path": outside},
		ID:         "ignored",
	}
	if _, err := s.resolveAuthPath(auth); err == nil {
		t.Fatalf("expected outside absolute path to be rejected")
	}
}

func TestResolveAuthPath_AppendsBaseDirForRelativeFileName(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	s := &GitTokenStore{}
	s.SetBaseDir(baseDir)

	auth := &cliproxyauth.Auth{
		FileName: "providers/team/provider.json",
	}
	got, err := s.resolveAuthPath(auth)
	if err != nil {
		t.Fatalf("resolveAuthPath failed: %v", err)
	}
	rel, err := filepath.Rel(baseDir, got)
	if err != nil {
		t.Fatalf("filepath.Rel failed: %v", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("resolved path escaped auth directory: %s", got)
	}
}
