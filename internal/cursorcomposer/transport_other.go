//go:build !darwin

package cursorcomposer

func macCursorAPIBackendBase() string  { return "" }
func macCursorAPIChatEndpoint() string { return "" }
func macCursorAPIClientVersion() string {
	return ""
}
