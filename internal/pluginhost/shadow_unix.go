//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"errors"
	"os"
	"syscall"
)

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
