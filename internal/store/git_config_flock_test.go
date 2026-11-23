//go:build darwin || linux

package store

import (
	"os"

	"golang.org/x/sys/unix"
)

func setGitTestConfigLock(file *os.File, exclusive bool) error {
	mode := unix.LOCK_UN
	if exclusive {
		mode = unix.LOCK_EX
	}
	return unix.Flock(int(file.Fd()), mode)
}
