//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"errors"
	"os"
	"syscall"
)

func lockShadowCoordFile(f *os.File, exclusive bool, nonBlocking bool) error {
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	if nonBlocking {
		how |= syscall.LOCK_NB
	}
	for {
		errFlock := syscall.Flock(int(f.Fd()), how)
		if !errors.Is(errFlock, syscall.EINTR) {
			return errFlock
		}
	}
}

func unlockShadowCoordFile(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

func publishShadowPlugin(tmpPath, targetPath string) error {
	if errLink := os.Link(tmpPath, targetPath); errLink != nil {
		return errLink
	}
	_ = os.Remove(tmpPath)
	return nil
}

func removeShadowPlugin(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
}

func removeShadowPluginDir(dir string) {
	_ = os.RemoveAll(dir)
}

func shadowPluginProcessAlive(pid int) bool {
	// Only ESRCH proves the process is gone; any other outcome keeps the
	// directory so a live process never loses its resident plugin images.
	errSignal := syscall.Kill(pid, 0)
	return !errors.Is(errSignal, syscall.ESRCH)
}
