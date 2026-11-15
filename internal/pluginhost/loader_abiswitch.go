//go:build cgo && (linux || darwin || freebsd)

package pluginhost

import "strings"

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
func (abiSwitchLoader) loaderFor(host *Host) pluginLoader {
	if strings.EqualFold(host.pluginABIMode(), "go") {
		return newGoPluginLoader()
	}
	return dynamicLibraryLoader{}
}

func (l abiSwitchLoader) Open(file pluginFile, host *Host) (pluginClient, error) {
	return l.loaderFor(host).Open(file, host)
}
