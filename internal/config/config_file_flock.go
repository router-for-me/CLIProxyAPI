//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package config

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockConfigFile(file *os.File, exclusive bool) error {
	mode := unix.LOCK_SH
	if exclusive {
		mode = unix.LOCK_EX
	}
	return flockConfigFile(file, mode)
}

func unlockConfigFile(file *os.File) error {
	return flockConfigFile(file, unix.LOCK_UN)
}

func flockConfigFile(file *os.File, mode int) error {
	for {
		err := unix.Flock(int(file.Fd()), mode)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func isPlatformConfigReadOnly(error) bool { return false }
