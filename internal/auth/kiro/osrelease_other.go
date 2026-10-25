//go:build !windows

package kiro

import (
	"os/exec"
	"strings"
)

// detectOSRelease returns the kernel release the Kiro IDE user agent reports
// on non-Windows platforms, for example Darwin's "24.3.0".
func detectOSRelease() string {
	output, err := exec.Command("uname", "-r").Output()
	if err != nil {
		return "0.0.0"
	}
	release := strings.TrimSpace(string(output))
	if release == "" {
		return "0.0.0"
	}
	return release
}
