//go:build !windows

package qoder

// RegisterURIHandler is a no-op on non-Windows platforms.
// On Linux/macOS, the qoder:// protocol would need xdg-open or other platform-specific handling.
// For now, users on non-Windows platforms should paste the callback URL manually.
func RegisterURIHandler(callbackPort int) func() {
	return func() {}
}

// UnregisterURIHandler is a no-op on non-Windows platforms.
func UnregisterURIHandler() {}
