//go:build windows

package atomicfile

import (
	"errors"
	"syscall"
)

// Windows error codes for a full disk (ERROR_HANDLE_DISK_FULL, ERROR_DISK_FULL),
// an exhausted quota (ERROR_DISK_QUOTA_EXCEEDED) and an I/O error
// (ERROR_IO_DEVICE).
const (
	errorHandleDiskFull    syscall.Errno = 39
	errorDiskFull          syscall.Errno = 112
	errorDiskQuotaExceeded syscall.Errno = 1295
	errorIODevice          syscall.Errno = 1117
)

// capacityError reports a full disk, an exhausted quota or an I/O error. Writing in place
// cannot do better than the atomic path then: it would truncate the target first
// and could leave it empty or partial.
func capacityError(err error) bool {
	return errors.Is(err, errorHandleDiskFull) || errors.Is(err, errorDiskFull) || errors.Is(err, errorDiskQuotaExceeded) ||
		errors.Is(err, errorIODevice)
}
