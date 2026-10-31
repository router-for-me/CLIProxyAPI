//go:build !windows

package codex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
)

func acquireInterProcessLockFileContext(ctx context.Context, authFilePath string) (func(), error) {
	lockPath := canonicalLockPath(authFilePath) + ".lock"
	f, errOpen := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if errOpen != nil {
		return nil, fmt.Errorf("failed to open credential lock file %s: %w", lockPath, errOpen)
	}
	if errFlock := waitForFileLock(ctx, func() (bool, error) {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EINTR) {
			return false, nil
		}
		return err == nil, err
	}); errFlock != nil {
		_ = f.Close()
		return nil, fmt.Errorf("failed to acquire flock on %s: %w", lockPath, errFlock)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
