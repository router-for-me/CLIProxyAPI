package models

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestGPT61SolClientResponsePreservesTemplateCapabilities(t *testing.T) {
	info := registry.LookupModelInfo("gpt-6.1-sol")
	if info == nil {
		t.Fatal("GPT-6.1 Sol registry entry is missing")
	}
	alias := *info
	alias.ID = "pool/gpt-6.1-sol"
	alias.MetadataModelID = info.ID
	r := registry.GetGlobalRegistry()
	const clientID = "gpt61-sol-template-test"
	r.RegisterClient(clientID, "codex", []*registry.ModelInfo{info, &alias})
	t.Cleanup(func() { r.UnregisterClient(clientID) })
	response := BuildResponseForClient(r.GetAvailableModels("openai"), r.GetModelProviders, false, "0.158.0")
	entries := response["models"].([]map[string]any)
	for _, slug := range []string{info.ID, alias.ID} {
		var found map[string]any
		for _, entry := range entries {
			if entry["slug"] == slug {
				found = entry
			}
		}
		if found == nil {
			t.Fatalf("client response is missing %s", slug)
		}
		if found["default_reasoning_level"] != "low" || found["tool_mode"] != "code_mode_only" || found["node_repl_auto_review_required"] != true {
			t.Fatalf("%s lost its reasoning/tool-mode defaults", slug)
		}
		hasUltra := false
		for _, level := range found["supported_reasoning_levels"].([]any) {
			if level.(map[string]any)["effort"] == "ultra" {
				hasUltra = true
			}
		}
		if !hasUltra {
			t.Fatalf("%s lost its client delegation level", slug)
		}
	}
}
