//go:build windows

package codex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modkernel32      = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = modkernel32.NewProc("LockFileEx")
	procUnlockFileEx = modkernel32.NewProc("UnlockFileEx")
)

const (
	lockfileExclusiveLock   = 0x00000002
	lockfileFailImmediately = 0x00000001
)

func acquireInterProcessLockFileContext(ctx context.Context, authFilePath string) (func(), error) {
	lockPath := canonicalLockPath(authFilePath) + ".lock"
	f, errOpen := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if errOpen != nil {
		return nil, fmt.Errorf("failed to open credential lock file %s: %w", lockPath, errOpen)
	}
	errLock := waitForFileLock(ctx, func() (bool, error) {
		var overlapped syscall.Overlapped
		r1, _, err := procLockFileEx.Call(f.Fd(), uintptr(lockfileExclusiveLock|lockfileFailImmediately), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
		if r1 != 0 {
			return true, nil
		}
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return false, nil
		}
		return false, err
	})
	if errLock != nil {
		_ = f.Close()
		return nil, fmt.Errorf("failed to lock credential file %s: %w", lockPath, errLock)
	}
	return func() {
		var ov syscall.Overlapped
		_, _, _ = procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&ov)))
		_ = f.Close()
	}, nil
}
