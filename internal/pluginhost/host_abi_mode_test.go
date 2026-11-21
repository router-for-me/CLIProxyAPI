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

func TestPluginABIModeChangedIgnoresCase(t *testing.T) {
	// The loader selection is case-insensitive, so a differently spelled
	// value must not be reported as an ABI change.
	if pluginABIModeChanged("go", "GO") {
		t.Fatal(`plugins.abi "GO" must not be reported as a change of the loaded "go" ABI`)
	}
	if pluginABIModeChanged("", "") {
		t.Fatal("an unchanged plugins.abi must not be reported as a change")
	}
}

func TestPluginABIModeChangedDetectsRealChanges(t *testing.T) {
	if !pluginABIModeChanged("go", "") {
		t.Fatal("switching plugins.abi from \"go\" to the default must be reported")
	}
	if !pluginABIModeChanged("", "go") {
		t.Fatal("switching plugins.abi from the default to \"go\" must be reported")
	}
}
