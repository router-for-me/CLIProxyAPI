// Package atomicfile provides a shared atomic write helper: content lands in
// a temp file in the target directory, is fsynced, then renamed over the
// target so readers never observe a partial write. The Windows-retry on
// rename mirrors the pattern proven in internal/pluginstore/install.go.
package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// renameRetryCount / renameRetryDelay handle the transient
// "Access is denied"/"file in use" Windows returns when another handle has
// the target open. On other platforms a single failure returns immediately.
const (
	renameRetryCount = 10
	renameRetryDelay = 20 * time.Millisecond
)

// Write atomically replaces path with data using the given file mode. The
// temp file inherits the target directory so Rename stays on the same
// filesystem.
func Write(path string, data []byte, mode os.FileMode) error {
	if path == "" {
		return errors.New("atomicfile: path is empty")
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".atomicfile-*")
	if err != nil {
		return fmt.Errorf("atomicfile: create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup; a successful Rename invalidates tmpName so the
	// deferred Remove becomes a harmless no-op in that path.
	defer func() { _ = os.Remove(tmpName) }()

	wrote := 0
	for wrote < len(data) {
		n, errWrite := tmp.Write(data[wrote:])
		if errWrite != nil {
			_ = tmp.Close()
			return fmt.Errorf("atomicfile: write %s: %w", path, errWrite)
		}
		wrote += n
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("atomicfile: sync %s: %w", path, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("atomicfile: close %s: %w", path, err)
	}
	if err = os.Chmod(tmpName, mode); err != nil {
		return fmt.Errorf("atomicfile: chmod %s: %w", tmpName, err)
	}
	if err = renameWithRetry(tmpName, path); err != nil {
		return fmt.Errorf("atomicfile: rename onto %s: %w", path, err)
	}
	return nil
}

func renameWithRetry(from, to string) error {
	var err error
	for attempt := 0; attempt < renameRetryCount; attempt++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		if runtime.GOOS != "windows" {
			// The transient lock Windows hits does not occur on other
			// platforms; fail immediately rather than retrying.
			return err
		}
		time.Sleep(renameRetryDelay)
	}
	return err
}
