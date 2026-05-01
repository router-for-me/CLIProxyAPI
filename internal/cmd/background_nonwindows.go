//go:build !windows

package cmd

import "fmt"

// StartDetachedIfRequested relaunches the current process in detached mode when requested.
// Non-Windows platforms do not support this relaunch mode.
func StartDetachedIfRequested(enabled bool, args []string) (bool, error) {
	_ = args
	if enabled {
		return false, fmt.Errorf("--background is currently supported on Windows only")
	}
	return false, nil
}
