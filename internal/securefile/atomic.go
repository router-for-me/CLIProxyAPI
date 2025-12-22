package securefile

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// EnsurePrivateDir creates dirPath (and parents) with 0700 permissions.
func EnsurePrivateDir(dirPath string) error {
	if dirPath == "" {
		return fmt.Errorf("securefile: dir path is empty")
	}
	if err := os.MkdirAll(dirPath, 0o700); err != nil {
		return err
	}
	// Best-effort permission hardening. Ignore errors (e.g., non-POSIX FS).
	_ = os.Chmod(dirPath, 0o700)
	return nil
}

// AtomicWriteFile writes data to path using a temp file + rename, and attempts to fsync.
// mode controls the final file permissions.
func AtomicWriteFile(path string, data []byte, mode os.FileMode) error {
	if path == "" {
		return fmt.Errorf("securefile: path is empty")
	}
	dir := filepath.Dir(path)
	if err := EnsurePrivateDir(dir); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	if mode == 0 {
		mode = 0o600
	}
	if err := tmp.Chmod(mode); err != nil {
		// Best-effort: ignore chmod failure on some filesystems.
	}

	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	if err := os.Rename(tmpName, path); err != nil {
		return err
	}

	// Best-effort: ensure final mode.
	_ = os.Chmod(path, mode)
	return nil
}

// ReadFileRawLocked reads the file at path while holding an advisory lock on path+".lock".
func ReadFileRawLocked(path string) ([]byte, error) {
	lockPath := path + ".lock"
	var out []byte
	err := WithLock(lockPath, 10*time.Second, func() error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// WriteFileRawLocked writes data to path using an advisory lock on path+".lock" and atomic replace.
func WriteFileRawLocked(path string, data []byte, mode os.FileMode) error {
	lockPath := path + ".lock"
	return WithLock(lockPath, 10*time.Second, func() error {
		return AtomicWriteFile(path, data, mode)
	})
}
