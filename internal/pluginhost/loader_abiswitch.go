//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import (
	"strings"

	log "github.com/sirupsen/logrus"
)

// abiSwitchLoader chooses between the C ABI (c-shared) and Go-native
// (plugin.Open) loaders from the plugins.abi configuration.
//
// The default is deliberately the existing C ABI path: every current user
// runs c-shared plugins, so an empty/unknown value must preserve that
// behavior byte-for-byte. Go-native loading is an explicit opt-in for hosts
// where c-shared plugins cannot work (musl firmware rejecting Initial-Exec
// TLS from Go shared libraries).
type abiSwitchLoader struct{}

// loaderFor is the pure selection decision, separated from Open so it can be
// unit-tested without a real plugin file on disk.
//
// An empty value keeps the C ABI loader. An unrecognized value also keeps the
// C ABI loader, but it is reported: on musl the C ABI path cannot load Go
// plugins at all, so a typo would otherwise look like a plugin bug.
func (abiSwitchLoader) loaderFor(host *Host) pluginLoader {
	mode := strings.TrimSpace(host.pluginABIMode())
	if strings.EqualFold(mode, "go") {
		return newGoPluginLoader()
	}
	if mode != "" {
		log.Warnf("pluginhost: unknown plugins.abi %q (supported values: \"\" for the C ABI loader, \"go\" for the Go-native loader); using the C ABI loader", mode)
	}
	return dynamicLibraryLoader{}
}

func (l abiSwitchLoader) Open(file pluginFile, host *Host) (pluginClient, error) {
	return l.loaderFor(host).Open(file, host)
}
