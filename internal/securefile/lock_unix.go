//go:build !windows

package securefile

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// WithLock obtains an advisory exclusive lock on lockPath (creating it if needed),
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

	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		// EWOULDBLOCK/EAGAIN indicates the lock is held by another process.
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return fmt.Errorf("securefile: unexpected error acquiring lock on %s: %w", lockPath, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("securefile: timed out acquiring lock: %s", lockPath)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}()
	return fn()
}
