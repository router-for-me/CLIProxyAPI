//go:build windows

package daemon

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

const stillActive = 259

func configureBackgroundProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
	}
}

func processRunning(pid int) bool {
	if pid <= 1 {
		return false
	}
	handle, errOpen := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if errOpen != nil {
		return errOpen == windows.ERROR_ACCESS_DENIED
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var exitCode uint32
	if errExitCode := windows.GetExitCodeProcess(handle, &exitCode); errExitCode != nil {
		return true
	}
	return exitCode == stillActive
}
