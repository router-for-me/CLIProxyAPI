package thinking

import (
	"bytes"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

type testPluginProviderApplier struct {
	marker []byte
}

func (a *testPluginProviderApplier) Apply(body []byte, config ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	return bytes.Clone(a.marker), nil
}

func TestUnregisterPluginProviderGenerationDoesNotRemoveReplacement(t *testing.T) {
	ClearPluginProviders()
	t.Cleanup(ClearPluginProviders)

	old := &testPluginProviderApplier{marker: []byte("old")}
	replacement := &testPluginProviderApplier{marker: []byte("replacement")}
	if !RegisterPluginProviderGeneration("privacy", "v1", "test-provider", 0, old) {
		t.Fatal("failed to register old plugin provider generation")
	}
	ClearPluginProviders()
	if !RegisterPluginProviderGeneration("privacy", "v2", "test-provider", 0, replacement) {
		t.Fatal("failed to register replacement plugin provider generation")
	}

	UnregisterPluginProvidersGeneration("privacy", "v1")
	if got := GetProviderApplier("test-provider"); got != replacement {
		t.Fatalf("provider after stale unregister = %T %p, want replacement %p", got, got, replacement)
	}
}
