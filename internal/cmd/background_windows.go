//go:build windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

const detachedProcess = 0x00000008
const backgroundCreationFlags = detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP

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

	executablePath, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("resolve executable path: %w", err)
	}

	child := exec.Command(executablePath, args...)
	child.Stdin = devNull
	child.Stdout = devNull
	child.Stderr = devNull
	child.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: backgroundCreationFlags,
	}

	if err := child.Start(); err != nil {
		return false, fmt.Errorf("start detached child process: %w", err)
	}

	fmt.Printf("CLIProxyAPI detached successfully (pid: %d)\n", child.Process.Pid)
	return true, nil
}
