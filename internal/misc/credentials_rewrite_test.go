package misc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenFileForSecureRewriteTruncatesAndWritesFromOffsetZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	if errWrite := os.WriteFile(path, []byte("stale credentials that are longer"), 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}

	file, errOpen := OpenFileForSecureRewrite(path)
	if errOpen != nil {
		t.Fatalf("OpenFileForSecureRewrite() error = %v", errOpen)
	}
	if _, errWrite := file.WriteString("replacement"); errWrite != nil {
		_ = file.Close()
		t.Fatalf("WriteString() error = %v", errWrite)
	}
	if errClose := file.Close(); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}

	contents, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	if got, want := string(contents), "replacement"; got != want {
		t.Errorf("file contents = %q, want %q", got, want)
	}
}
