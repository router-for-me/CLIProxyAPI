package pluginhost

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestPluginABIModeNilConfigReturnsEmpty(t *testing.T) {
	// Loader selection happens before any config may be applied; a nil
	// runtimeConfig must resolve to the empty mode (C ABI default), never panic.
	h := &Host{}
	if got := h.pluginABIMode(); got != "" {
		t.Fatalf("pluginABIMode() = %q, want empty for nil config", got)
	}
}

func TestPluginABIModeReadsConfig(t *testing.T) {
	h := &Host{runtimeConfig: &config.Config{}}
	h.runtimeConfig.Plugins.ABI = "go"
	if got := h.pluginABIMode(); got != "go" {
		t.Fatalf("pluginABIMode() = %q, want %q", got, "go")
	}
}

func TestPluginABIModeTrimsWhitespace(t *testing.T) {
	// The switch loader compares case-insensitively after trimming; this pins
	// the trimming so a value like " go " still selects the Go path.
	h := &Host{runtimeConfig: &config.Config{}}
	h.runtimeConfig.Plugins.ABI = "  go  "
	if got := h.pluginABIMode(); got != "go" {
		t.Fatalf("pluginABIMode() = %q, want trimmed %q", got, "go")
	}
}
