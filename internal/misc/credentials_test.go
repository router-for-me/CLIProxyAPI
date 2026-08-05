//go:build unix

package misc

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestOpenFileForSecureRewrite(t *testing.T) {
	previousUmask := unix.Umask(0o002)
	defer unix.Umask(previousUmask)

	t.Run("creates a new file with mode 0600 despite umask", func(t *testing.T) {
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

	t.Run("truncates existing contents", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "existing-content-credentials.json")
		if err := os.WriteFile(path, []byte("old credentials"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		file, err := OpenFileForSecureRewrite(path)
		if err != nil {
			t.Fatalf("OpenFileForSecureRewrite() error = %v", err)
		}
		if errClose := file.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}

		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
		if len(contents) != 0 {
			t.Errorf("file contents = %q, want empty", contents)
		}
	})

	t.Run("returns a descriptor that writes normally", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "writable-credentials.json")

		file, err := OpenFileForSecureRewrite(path)
		if err != nil {
			t.Fatalf("OpenFileForSecureRewrite() error = %v", err)
		}
		if _, errWrite := file.WriteString("replacement credentials"); errWrite != nil {
			t.Fatalf("WriteString() error = %v", errWrite)
		}
		if errClose := file.Close(); errClose != nil {
			t.Fatalf("Close() error = %v", errClose)
		}

		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
		if got, want := string(contents), "replacement credentials"; got != want {
			t.Errorf("file contents = %q, want %q", got, want)
		}
	})
}
