//go:build linux || darwin || freebsd

package pluginstore

import (
	"os/exec"
	"syscall"
)

func configureAuthCommandCancellation(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		if errKill := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); errKill == nil {
			return nil
		}
		return command.Process.Kill()
	}
}
