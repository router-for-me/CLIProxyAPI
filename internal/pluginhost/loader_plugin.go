//go:build linux || darwin || freebsd

package pluginhost

import "plugin"

type symbolLoader interface {
	Open(path string) (symbolLookup, error)
}

type symbolLookup interface {
	Lookup(name string) (any, error)
}

type goPluginLoader struct{}

func (goPluginLoader) Open(path string) (symbolLookup, error) {
	opened, errOpen := plugin.Open(path)
	if errOpen != nil {
		return nil, errOpen
	}
	return goPluginLookup{plugin: opened}, nil
}

type goPluginLookup struct {
	plugin *plugin.Plugin
}

func (l goPluginLookup) Lookup(name string) (any, error) {
	return l.plugin.Lookup(name)
}

func defaultSymbolLoader() symbolLoader {
	return goPluginLoader{}
}
