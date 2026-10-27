//go:build unix

package browser

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureCommandProcessGroup puts the child in its own process group so a
// timeout can reap the browser helper and all of its descendants.
func configureCommandProcessGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killCommandProcessGroup terminates the whole child process group.
func killCommandProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if errKill := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); errKill != nil {
		if errors.Is(errKill, syscall.ESRCH) {
			return nil
		}
		if errProcess := cmd.Process.Kill(); errProcess != nil && !errors.Is(errProcess, os.ErrProcessDone) {
			return errProcess
		}
	}
	return nil
}
