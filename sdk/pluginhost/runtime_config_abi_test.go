package pluginhost

import "testing"

// TestRuntimeConfigToInternalConfigCarriesABI verifies that the public
// runtime configuration can select the Go-native plugin loader. Without this
// the SDK surface could never enable it, because the internal config would
// always carry an empty ABI value.
func TestRuntimeConfigToInternalConfigCarriesABI(t *testing.T) {
	internal := runtimeConfigToInternalConfig(RuntimeConfig{
		Enabled: true,
		Dir:     "plugins",
		ABI:     "go",
	})
	if got := internal.Plugins.ABI; got != "go" {
		t.Fatalf("internal Plugins.ABI = %q, want %q", got, "go")
	}
}

// TestRuntimeConfigToInternalConfigDefaultABI verifies that an unset ABI stays
// empty so the default C ABI loader keeps being selected.
func TestRuntimeConfigToInternalConfigDefaultABI(t *testing.T) {
	internal := runtimeConfigToInternalConfig(RuntimeConfig{Enabled: true, Dir: "plugins"})
	if got := internal.Plugins.ABI; got != "" {
		t.Fatalf("internal Plugins.ABI = %q, want empty", got)
	}
}
