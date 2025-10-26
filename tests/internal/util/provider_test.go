package util_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
)

// This test ensures glm-* models are routed exclusively to provider "zhipu",
// even if other providers (e.g., iflow) also register the same model ID.
func TestGetProviderName_GLM_OnlyZhipu(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	// Simulate registrations: zhipu and iflow both register glm-4.6
	reg.RegisterClient("zhipu-client", "zhipu", registry.GetZhipuModels())
	reg.RegisterClient("iflow-client", "iflow", registry.GetIFlowModels())

	provs := util.GetProviderName("glm-4.6")
	if len(provs) != 1 || provs[0] != "zhipu" {
		t.Fatalf("expected only [zhipu], got %#v", provs)
	}
}
