//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// detachFromTerminal starts the helper in a new session without a controlling
// terminal, so a TUI cannot open /dev/tty even when the tests run in one.
func detachFromTerminal(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
