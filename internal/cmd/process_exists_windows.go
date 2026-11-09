//go:build windows

package cmd

import (
	"errors"

	"golang.org/x/sys/windows"
)

const (
	windowsStillActive = 259
	maxProcessID       = uint64(1<<32 - 1)
)

func processExists(pid int) (bool, error) {
	handle, errOpen := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if errOpen != nil {
		if errors.Is(errOpen, windows.ERROR_INVALID_PARAMETER) {
			return false, nil
		}
		if errors.Is(errOpen, windows.ERROR_ACCESS_DENIED) {
			return true, nil
		}
		return false, errOpen
	}
	defer windows.CloseHandle(handle)

	var exitCode uint32
	if errExitCode := windows.GetExitCodeProcess(handle, &exitCode); errExitCode != nil {
		return false, errExitCode
	}
	return exitCode == windowsStillActive, nil
}
