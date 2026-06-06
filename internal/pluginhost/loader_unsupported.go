//go:build !cgo && !windows

package pluginhost

import "fmt"

type unsupportedLoader struct{}

func (unsupportedLoader) Open(path string, host *Host) (pluginClient, error) {
	return nil, fmt.Errorf("standard dynamic library plugin loading requires cgo on this platform: %s", path)
}

func defaultPluginLoader() pluginLoader {
	return unsupportedLoader{}
}
