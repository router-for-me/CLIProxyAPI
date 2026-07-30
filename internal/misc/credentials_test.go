//go:build unix

package misc

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestCreateSecretFile checks that secret files end up owner-only regardless of
// the process umask, and that a file already on disk with a wider mode is
// tightened rather than left as-is.
func TestCreateSecretFile(t *testing.T) {
	defer syscall.Umask(syscall.Umask(0o002))
	dir := t.TempDir()

	cases := []struct {
		name    string
		prepare os.FileMode // 0 means the file does not exist yet
	}{
		{name: "fresh"},
		{name: "existing-group-readable", prepare: 0o664},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".json")
			if tc.prepare != 0 {
				if err := os.WriteFile(path, []byte("{}"), tc.prepare); err != nil {
					t.Fatalf("prepare: %v", err)
				}
				if err := os.Chmod(path, tc.prepare); err != nil {
					t.Fatalf("prepare chmod: %v", err)
				}
			}

			f, err := CreateSecretFile(path)
			if err != nil {
				t.Fatalf("CreateSecretFile: %v", err)
			}
			if err = f.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}

			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			if got := info.Mode().Perm(); got != SecretFileMode {
				t.Fatalf("mode = %o, want %o", got, SecretFileMode)
			}
		})
	}
}
