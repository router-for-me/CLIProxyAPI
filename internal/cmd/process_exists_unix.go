//go:build !windows

package cmd

import (
	"errors"

	"golang.org/x/sys/unix"
)

const maxProcessID = uint64(1<<31 - 1)

func processExists(pid int) (bool, error) {
	err := unix.Kill(pid, 0)
	if err == nil || errors.Is(err, unix.EPERM) {
		return true, nil
	}
	if errors.Is(err, unix.ESRCH) {
		return false, nil
	}
	return false, err
}
