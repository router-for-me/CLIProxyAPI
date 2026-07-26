package misc

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteCredentialFileAtomicReplacesExistingPrivately(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codex.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed credential file: %v", err)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatalf("make credential file read-only: %v", err)
	}

	want := []byte("new credential data")
	if err := WriteCredentialFileAtomic(path, want); err != nil {
		t.Fatalf("WriteCredentialFileAtomic() error: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read credential file: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("credential file = %q, want %q", got, want)
	}
	assertPrivateCredentialMode(t, path)
	assertNoCredentialTempFiles(t, dir, "codex.json")
}

func TestWriteCredentialFileAtomicPreservesExistingOnReplaceFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codex.json")
	original := []byte("original credential data")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("seed credential file: %v", err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatalf("loosen credential file permissions: %v", err)
	}

	errForced := errors.New("forced rename failure")
	renameCalls := 0
	err := writeCredentialFileAtomicWithRename(path, []byte("replacement credential data"), func(tempPath, destination string) error {
		renameCalls++
		if filepath.Dir(tempPath) != dir {
			t.Errorf("temporary file directory = %q, want %q", filepath.Dir(tempPath), dir)
		}
		if destination != path {
			t.Errorf("rename destination = %q, want %q", destination, path)
		}
		assertPrivateCredentialMode(t, tempPath)
		return errForced
	})
	if !errors.Is(err, errForced) {
		t.Fatalf("writeCredentialFileAtomicWithRename() error = %v, want %v", err, errForced)
	}
	if renameCalls != 1 {
		t.Fatalf("rename calls = %d, want 1", renameCalls)
	}

	got, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read original credential file: %v", errRead)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("original credential file changed: got %q, want %q", got, original)
	}
	assertPrivateCredentialMode(t, path)
	assertNoCredentialTempFiles(t, dir, "codex.json")
}

func TestTightenCredentialFilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codex.json")
	if err := os.WriteFile(path, []byte("credential data"), 0o600); err != nil {
		t.Fatalf("seed credential file: %v", err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatalf("loosen credential file permissions: %v", err)
	}

	if err := TightenCredentialFilePermissions(path); err != nil {
		t.Fatalf("TightenCredentialFilePermissions() error: %v", err)
	}
	assertPrivateCredentialMode(t, path)
}

func assertPrivateCredentialMode(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat credential file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("credential mode = %o, want 600", got)
	}
}

func assertNoCredentialTempFiles(t *testing.T, dir, name string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "."+name+".tmp-*"))
	if err != nil {
		t.Fatalf("glob credential temp files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("credential temp files were not removed: %v", matches)
	}
}
