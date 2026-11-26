//go:build darwin

package atomicfile

import (
	"errors"

	"golang.org/x/sys/unix"
)

// exchangeFiles atomically swaps the directory entries of a and b. It fails with
// ENOENT, changing nothing, when either path is missing.
func exchangeFiles(a, b string) error {
	return unix.RenamexNp(a, b, unix.RENAME_SWAP)
}

// exchangeUnsupported reports an error meaning the filesystem cannot swap, so the
// caller should fall back to a plain rename.
func exchangeUnsupported(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP)
}

func exchangeSupported() bool { return true }
