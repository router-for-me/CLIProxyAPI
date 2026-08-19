package pluginhost

// SupportPluginHeaderValue reports whether the current binary was built with CGO enabled.
func SupportPluginHeaderValue() string {
	return supportPluginValue
}
