//go:build windows

package pluginhost

import "testing"

func TestSupportPluginHeaderValueOnWindows(t *testing.T) {
	if got := SupportPluginHeaderValue(); got != "1" {
		t.Fatalf("SupportPluginHeaderValue() = %q, want \"1\"", got)
	}
}
