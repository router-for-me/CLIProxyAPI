//go:build windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

const backgroundCreationFlags = 0x00000008 | 0x00000200

// StartDetachedIfRequested relaunches the current process in detached mode when requested.
// It returns true when parent process should exit immediately.
func StartDetachedIfRequested(enabled bool, args []string) (bool, error) {
	if !enabled {
		return false, nil
	}

	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return false, fmt.Errorf("open dev null: %w", err)
	}
	defer func() {
		_ = devNull.Close()
	}()

	child := exec.Command(os.Args[0], args...)
	child.Stdin = devNull
	child.Stdout = devNull
	child.Stderr = devNull
	child.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: backgroundCreationFlags,
	}

	if err := child.Start(); err != nil {
		return false, fmt.Errorf("start detached child process: %w", err)
	}
	if child.Process == nil {
		return false, fmt.Errorf("detached process started without pid")
	}

	fmt.Printf("CLIProxyAPI detached successfully (pid: %d)\n", child.Process.Pid)
	return true, nil
}
