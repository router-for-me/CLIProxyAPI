//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package config

import (
	"errors"
	"os"
)

func lockLocalConfigFile(bool) func() { return func() {} }
func lockConfigFile(*os.File, bool) error {
	return errors.New("OS config file locking is not supported on this platform")
}
func unlockConfigFile(*os.File) error     { return nil }
func isPlatformConfigReadOnly(error) bool { return false }
