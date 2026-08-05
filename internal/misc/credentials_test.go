//go:build unix

package misc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenFileForSecureRewrite(t *testing.T) {
	t.Run("creates a new file with mode 0600", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "new-credentials.json")

		file, err := OpenFileForSecureRewrite(path)
		if err != nil {
			t.Fatalf("OpenFileForSecureRewrite() error = %v", err)
		}
		if errClose := file.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat() error = %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("file mode = %o, want 0600", got)
		}
	})

	t.Run("restricts an existing file to mode 0600", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "existing-mode-credentials.json")
		if err := os.WriteFile(path, []byte("old credentials"), 0o664); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		if err := os.Chmod(path, 0o664); err != nil {
			t.Fatalf("Chmod() error = %v", err)
		}

		file, err := OpenFileForSecureRewrite(path)
		if err != nil {
			t.Fatalf("OpenFileForSecureRewrite() error = %v", err)
		}
		if errClose := file.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat() error = %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("file mode = %o, want 0600", got)
		}
	})
}
