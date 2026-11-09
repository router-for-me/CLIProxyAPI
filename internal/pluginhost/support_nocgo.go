//go:build !cgo && !windows

package pluginhost

// Plugin loading requires cgo on non-Windows platforms.
const supportPluginValue = "0"
