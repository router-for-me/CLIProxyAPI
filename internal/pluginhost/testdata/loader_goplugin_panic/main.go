// Package main is a Go plugin whose package initializer panics.
//
// The plugin exists to check the host's Go-native loader against the real
// plugin.Open: the panic has to be recovered and reported as an ordinary
// load error, and the path must never be opened again.
//
// The plugin deliberately does not import the host module. A Go plugin has
// to be built with an identical dependency graph as the host binary, and the
// host module is not importable from a nested testdata module.
package main

// init panics while the plugin package is initialized, which happens inside
// plugin.Open before any exported symbol is resolved.
func init() {
	panic("goplugin test plugin panics during initialization")
}

// main is never called: the plugin package initializer is the only part of
// the plugin that runs in the host process.
func main() {}
