package pluginhost

// SupportPluginHeaderValue reports whether the current binary can load plugins.
func SupportPluginHeaderValue() string {
	return supportPluginValue
}
