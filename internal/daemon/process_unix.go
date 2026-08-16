//go:build !windows

package daemon

import (
	"errors"
	"os/exec"
	"syscall"
)

func configureBackgroundProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func processRunning(pid int) bool {
	if pid <= 1 {
		return false
	}
	errSignal := syscall.Kill(pid, 0)
	return errSignal == nil || errors.Is(errSignal, syscall.EPERM)
}
