//go:build darwin

package zai

import "golang.org/x/sys/unix"

// zcodeOsVersion mirrors node's os.version() on macOS (the kern.version
// sysctl), which the official client reports as X-Os-Version.
func zcodeOsVersion() string {
	version, errSysctl := unix.Sysctl("kern.version")
	if errSysctl != nil {
		return ""
	}
	return printableASCII(version)
}
