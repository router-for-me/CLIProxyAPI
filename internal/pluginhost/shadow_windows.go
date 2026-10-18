//go:build windows

package pluginhost

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func removeShadowPlugin(path string) {
	if path == "" {
		return
	}
	if errRemove := os.Remove(path); errRemove == nil {
		return
	}
	pathPtr, errPath := windows.UTF16PtrFromString(path)
	if errPath != nil {
		return
	}
	_ = windows.MoveFileEx(pathPtr, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT)
}

func removeShadowPluginDir(dir string) {
	if errRemove := os.RemoveAll(dir); errRemove == nil {
		return
	}
	entries, errRead := os.ReadDir(dir)
	if errRead != nil {
		return
	}
	for _, entry := range entries {
		if entry == nil || entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, shadowPluginPrefix) || strings.HasPrefix(name, shadowPluginTempPrefix) {
			removeShadowPlugin(filepath.Join(dir, name))
		}
	}
	_ = os.Remove(dir)
}

func shadowPluginProcessAlive(pid int) bool {
	// Only an invalid parameter proves the process id no longer exists;
	// any other error keeps the directory so a live process never loses
	// its resident plugin images.
	handle, errOpen := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errOpen != nil {
		return !errors.Is(errOpen, windows.ERROR_INVALID_PARAMETER)
	}
	event, errWait := windows.WaitForSingleObject(handle, 0)
	_ = windows.CloseHandle(handle)
	if errWait != nil {
		return true
	}
	return event == uint32(windows.WAIT_TIMEOUT)
}
