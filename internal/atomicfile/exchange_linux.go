//go:build linux

package atomicfile

import (
	"errors"

	"golang.org/x/sys/unix"
)

// exchangeFiles atomically swaps the directory entries of a and b. It fails with
// ENOENT, changing nothing, when either path is missing.
func exchangeFiles(a, b string) error {
	return unix.Renameat2(unix.AT_FDCWD, a, unix.AT_FDCWD, b, unix.RENAME_EXCHANGE)
}

// exchangeUnsupported reports an error meaning the kernel or filesystem cannot
// exchange, so the caller should fall back to a plain rename.
func exchangeUnsupported(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP)
}

func exchangeSupported() bool { return true }
