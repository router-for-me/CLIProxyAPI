//go:build windows

package browser

import (
	"errors"
	"os"
	"os/exec"
)

// configureCommandProcessGroup is a no-op on Windows.
func configureCommandProcessGroup(_ *exec.Cmd) {}

// killCommandProcessGroup terminates the child process.
func killCommandProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	if errKill := cmd.Process.Kill(); errKill != nil && !errors.Is(errKill, os.ErrProcessDone) {
		return errKill
	}
	return nil
}
