package misc

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// Each operation uses real temporary files; only the named fault is injected.
func TestAtomicWritePrivateOperationFailures(t *testing.T) {
	for _, stage := range []string{"create", "chmod", "write", "short-write", "file-sync", "file-close", "rename", "dir-open", "dir-sync", "dir-close"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "credential.json")
			if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			fault := errors.New("injected storage failure")
			ops := privateWriteOpsDefault()
			switch stage {
			case "create":
				ops.create = func(string, string) (*os.File, error) { return nil, fault }
			case "chmod":
				ops.chmod = func(*os.File, os.FileMode) error { return fault }
			case "write":
				ops.write = func(f *os.File, p []byte) (int, error) {
					n, e := f.Write(p[:1])
					if e != nil {
						return n, e
					}
					return n, fault
				}
			case "short-write":
				ops.write = func(f *os.File, p []byte) (int, error) { return f.Write(p[:1]) }
			case "file-sync":
				ops.syncFile = func(*os.File) error { return fault }
			case "file-close":
				ops.closeFile = func(f *os.File) error { _ = f.Close(); return fault }
			case "rename":
				ops.rename = func(string, string) error { return fault }
			case "dir-open":
				ops.openDir = func(string) (*os.File, error) { return nil, fault }
			case "dir-sync":
				ops.syncDir = func(*os.File) error { return fault }
			case "dir-close":
				ops.closeDir = func(f *os.File) error { _ = f.Close(); return fault }
			}
			err := atomicWritePrivate(path, []byte("new"), ops)
			expected := fault
			if stage == "short-write" {
				expected = io.ErrShortWrite
			}
			if !errors.Is(err, expected) {
				t.Fatalf("error=%v want %v", err, expected)
			}
			got, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			want := "old"
			if stage == "dir-open" || stage == "dir-sync" || stage == "dir-close" {
				want = "new"
			}
			if string(got) != want {
				t.Fatalf("contents=%q want %q", got, want)
			}
			entries, e := os.ReadDir(dir)
			if e != nil {
				t.Fatal(e)
			}
			if len(entries) != 1 {
				t.Fatalf("temporary file leaked: %v", entries)
			}
		})
	}
}
