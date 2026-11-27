//go:build windows

package atomicfile

import (
	"errors"
	"syscall"
)

// Windows error codes for a full disk (ERROR_HANDLE_DISK_FULL, ERROR_DISK_FULL)
// and an exhausted quota (ERROR_DISK_QUOTA_EXCEEDED).
const (
	errorHandleDiskFull    syscall.Errno = 39
	errorDiskFull          syscall.Errno = 112
	errorDiskQuotaExceeded syscall.Errno = 1295
)

// capacityError reports a full disk or an exhausted quota. Writing in place
// cannot do better than the atomic path then: it would truncate the target first
// and could leave it empty or partial.
func capacityError(err error) bool {
	return errors.Is(err, errorHandleDiskFull) || errors.Is(err, errorDiskFull) || errors.Is(err, errorDiskQuotaExceeded)
}
