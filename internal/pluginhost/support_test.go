package pluginhost

import (
	"runtime"
	"testing"
)

func TestSupportPluginHeaderValueReportsWindowsPluginSupport(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows uses the DLL plugin loader")
	}
	if got := SupportPluginHeaderValue(); got != "1" {
		t.Fatalf("SupportPluginHeaderValue() = %q, want 1 on Windows", got)
	}
}
