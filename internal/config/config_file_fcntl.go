//go:build aix || solaris

package config

import (
	"errors"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

var configFileGate sync.Mutex

func lockLocalConfigFile(bool) func() {
	// POSIX record locks are owned by the process. Closing ANY descriptor for
	// the inode releases them, so even shared reads must serialize open/close.
	configFileGate.Lock()
	return configFileGate.Unlock
}

func lockConfigFile(file *os.File, exclusive bool) error {
	mode := int16(unix.F_RDLCK)
	if exclusive {
		mode = unix.F_WRLCK
	}
	return fcntlConfigFile(file, unix.F_SETLKW, mode)
}

func unlockConfigFile(file *os.File) error {
	return fcntlConfigFile(file, unix.F_SETLK, unix.F_UNLCK)
}

func fcntlConfigFile(file *os.File, cmd int, mode int16) error {
	lock := unix.Flock_t{Type: mode, Whence: 0, Start: 0, Len: 0}
	for {
		err := unix.FcntlFlock(file.Fd(), cmd, &lock)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func isPlatformConfigReadOnly(error) bool { return false }
