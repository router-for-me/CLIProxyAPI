//go:build linux || darwin

package selfupdate

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

const platformSupported = true

// dupListenerFD duplicates the base listener's socket (close-on-exec for now, so no child process
// started before the restart inherits it).
func dupListenerFD(ln *gateListener) (int, error) {
	if ln == nil {
		return -1, errors.New("no listener")
	}
	sc, ok := ln.Listener.(syscall.Conn)
	if !ok {
		return -1, errors.New("listener does not expose its socket")
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return -1, err
	}
	newFD := -1
	var errDup error
	if errCtl := raw.Control(func(fd uintptr) {
		newFD, errDup = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 3)
	}); errCtl != nil {
		return -1, errCtl
	}
	if errDup != nil {
		return -1, errDup
	}
	return newFD, nil
}

// clearCloexec lets fd survive exec.
func clearCloexec(fd int) error {
	_, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0)
	return err
}

func closeFD(fd int) { _ = unix.Close(fd) }

func execBinary(path string, args, env []string) error {
	return syscall.Exec(path, args, env)
}

func isWritableDir(dir string) error {
	f, err := os.CreateTemp(dir, ".proxx-write-test-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}
