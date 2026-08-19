//go:build !cgo && !windows

package pluginhost

import "fmt"

type unsupportedLoader struct{}

func (unsupportedLoader) Open(file pluginFile, host *Host) (pluginClient, error) {
	return nil, fmt.Errorf("standard dynamic library plugin loading requires cgo on this platform: %s", file.Path)
}

func defaultPluginLoader() pluginLoader {
	return unsupportedLoader{}
}
