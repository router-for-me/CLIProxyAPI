//go:build cgo || windows

package pluginhost

// Windows loads plugins through syscall.LoadDLL and does not require cgo.
const supportPluginValue = "1"
