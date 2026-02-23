package store

import (
	"path/filepath"
	"strings"
	"testing"
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
