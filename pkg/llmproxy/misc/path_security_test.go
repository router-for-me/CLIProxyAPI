package misc

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSafeFilePathRejectsTraversal(t *testing.T) {
	_, err := ResolveSafeFilePath("/tmp/../escape.json")
	if err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
}

func TestResolveSafeFilePathInDirRejectsSeparatorsAndTraversal(t *testing.T) {
	base := t.TempDir()

	if _, err := ResolveSafeFilePathInDir(base, "..\\escape.json"); err == nil {
		t.Fatal("expected backslash traversal payload to be rejected")
	}
	if _, err := ResolveSafeFilePathInDir(base, "../escape.json"); err == nil {
		t.Fatal("expected slash traversal payload to be rejected")
	}
}

func TestResolveSafeFilePathInDirResolvesInsideBaseDir(t *testing.T) {
	base := t.TempDir()
	path, err := ResolveSafeFilePathInDir(base, "valid.json")
	if err != nil {
		t.Fatalf("expected valid file name: %v", err)
	}
	if !strings.HasPrefix(path, filepath.Clean(base)+string(filepath.Separator)) {
		t.Fatalf("expected resolved path %q under base %q", path, base)
	}
}
