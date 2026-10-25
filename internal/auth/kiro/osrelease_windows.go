//go:build windows

package kiro

import (
	"fmt"

	log "github.com/sirupsen/logrus"
	"golang.org/x/sys/windows/registry"
)

// detectOSRelease reads the Windows version reported by the Kiro IDE user
// agent, for example "10.0.26300".
func detectOSRelease() string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return "10.0.0"
	}
	defer func() {
		if errClose := key.Close(); errClose != nil {
			log.WithError(errClose).Debug("kiro: close OS version registry key failed")
		}
	}()
	major, _, errMajor := key.GetIntegerValue("CurrentMajorVersionNumber")
	minor, _, errMinor := key.GetIntegerValue("CurrentMinorVersionNumber")
	build, _, errBuild := key.GetStringValue("CurrentBuildNumber")
	if errBuild != nil {
		build, _, errBuild = key.GetStringValue("CurrentBuild")
	}
	if errMajor != nil || errMinor != nil || errBuild != nil || build == "" {
		return "10.0.0"
	}
	return fmt.Sprintf("%d.%d.%s", major, minor, build)
}
