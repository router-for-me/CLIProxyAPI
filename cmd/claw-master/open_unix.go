//go:build !windows

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// openWithoutBlocking opens path for reading; a FIFO there cannot make the open wait.
func openWithoutBlocking(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
