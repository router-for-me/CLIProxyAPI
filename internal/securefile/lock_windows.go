//go:build windows

package securefile

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

// WithLock obtains an exclusive file lock on lockPath (creating it if needed),
// runs fn, and then releases the lock. It retries until timeout.
func WithLock(lockPath string, timeout time.Duration, fn func() error) error {
	if lockPath == "" {
		return fmt.Errorf("securefile: lock path is empty")
	}
	if fn == nil {
		return fmt.Errorf("securefile: lock fn is nil")
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if err := EnsurePrivateDir(filepath.Dir(lockPath)); err != nil {
		return err
	}

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_ = f.Chmod(0o600)

	handle := windows.Handle(f.Fd())
	var overlapped windows.Overlapped

	deadline := time.Now().Add(timeout)
	for {
		errLock := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
		if errLock == nil {
			break
		}
		if errLock != windows.ERROR_LOCK_VIOLATION && errLock != windows.ERROR_SHARING_VIOLATION {
			return errLock
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("securefile: timed out acquiring lock: %s", lockPath)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer func() {
		_ = windows.UnlockFileEx(handle, 0, 1, 0, &overlapped)
	}()
	return fn()
}
