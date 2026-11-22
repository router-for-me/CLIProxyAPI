// Package main is a well-behaved Go plugin that is only used to exercise the
// host's Go-native loader against a real plugin.Open.
//
// The plugin deliberately does not export New and does not import the host
// module: a Go plugin has to be built with an identical dependency graph as
// the host binary, and the host module is not importable from a nested
// testdata module. The loader tests inject the New symbol through the lookup
// seam, so the plugin only has to initialize cleanly and export a marker.
package main

// Marker proves that the plugin built and loaded. It is never called by the
// host; the loader resolves the injected New symbol instead.
func Marker() string { return "ok" }

// main is never called: the plugin package initializer is the only part of
// the plugin that runs in the host process.
func main() {}
