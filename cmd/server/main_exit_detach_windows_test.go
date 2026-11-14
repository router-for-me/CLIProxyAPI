//go:build windows

package main

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// detachFromTerminal starts the helper without a console, so a TUI cannot open
// CONIN$ even when the tests run in one.
func detachFromTerminal(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
}
