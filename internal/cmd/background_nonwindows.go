//go:build !windows

package cmd

// StartDetachedIfRequested relaunches the current process in detached mode when requested.
// Non-Windows platforms currently do not relaunch.
func StartDetachedIfRequested(enabled bool, args []string) (bool, error) {
	_ = args
	return false, nil
}
