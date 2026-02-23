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

func TestResolveSafeFilePathRejectsEncodedTraversalAndWindowsSeparators(t *testing.T) {
	cases := []string{
		"..%2f..%2fsecret.json",
		"..\\..\\secret.json",
		"..//..%2fsecret.json",
	}
	for _, path := range cases {
		path := path
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			if _, err := ResolveSafeFilePath(path); err == nil {
				t.Fatalf("expected traversal path to be rejected: %q", path)
			}
		})
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
	if _, err := ResolveSafeFilePathInDir(base, "..%2f%2esecret.json"); err == nil {
		t.Fatal("expected encoded traversal payload to be rejected")
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
