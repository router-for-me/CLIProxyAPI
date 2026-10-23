//go:build linux

package zai

import (
	"bytes"

	"golang.org/x/sys/unix"
)

// zcodeOsVersion mirrors node's os.version() on Linux (the uname version
// field), which the official client reports as X-Os-Version.
func zcodeOsVersion() string {
	var uts unix.Utsname
	if errUname := unix.Uname(&uts); errUname != nil {
		return ""
	}
	return printableASCII(utsVersion(uts.Version))
}

func utsVersion(field [65]byte) string {
	raw := field[:]
	if idx := bytes.IndexByte(raw, 0); idx >= 0 {
		raw = raw[:idx]
	}
	return string(raw)
}
