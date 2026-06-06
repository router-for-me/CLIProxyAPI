//go:build !(linux || darwin || freebsd)

package pluginhost

import "fmt"

type symbolLoader interface {
	Open(path string) (symbolLookup, error)
}

type symbolLookup interface {
	Lookup(name string) (any, error)
}

type unsupportedLoader struct{}

func (unsupportedLoader) Open(path string) (symbolLookup, error) {
	return nil, fmt.Errorf("go plugin loading is not supported on this platform")
}

func defaultSymbolLoader() symbolLoader {
	return unsupportedLoader{}
}
