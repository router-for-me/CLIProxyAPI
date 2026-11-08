//go:build windows

package codex

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	modkernel32      = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = modkernel32.NewProc("LockFileEx")
	procUnlockFileEx = modkernel32.NewProc("UnlockFileEx")
)

const lockfileExclusiveLock = 0x00000002

func acquireInterProcessLockFile(authFilePath string) (func(), error) {
	lockPath := canonicalLockPath(authFilePath) + ".lock"
	f, errOpen := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if errOpen != nil {
		return nil, fmt.Errorf("failed to open credential lock file %s: %w", lockPath, errOpen)
	}
	var overlapped syscall.Overlapped
	r1, _, errLock := procLockFileEx.Call(
		f.Fd(),
		uintptr(lockfileExclusiveLock),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if r1 == 0 {
		_ = f.Close()
		return nil, fmt.Errorf("failed to lock credential file %s: %w", lockPath, errLock)
	}
	return func() {
		var ov syscall.Overlapped
		_, _, _ = procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&ov)))
		_ = f.Close()
	}, nil
}
