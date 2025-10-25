package e2e

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

// E2E-lite: registering a Copilot client in the global registry should expose Copilot-only models.
func TestModels_ContainsCopilotWhenAuthRegistered(t *testing.T) {
	t.Parallel()

	reg := registry.GetGlobalRegistry()
	const clientID = "copilot:e2e"
	reg.RegisterClient(clientID, "copilot", registry.GetCopilotModels())
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})

	providers := reg.GetModelProviders("gpt-5-mini")
	found := false
	for _, p := range providers {
		if p == "copilot" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected copilot provider for gpt-5-mini, got %v", providers)
	}
}
