package models

import "testing"

// Preserve the native-catalog acceptance cases from upstream d306f2c5.
func TestCodexCatalogApplyPatch_TemplateModelsRetainFreeformByDefault_Issue6286(t *testing.T) {
	// Models defined in codex_client_models.json with "apply_patch_tool_type": "freeform"
	// must retain "freeform" under pure Codex providers without a new capability switch.
	canonicalTemplateModels := []string{
		"gpt-6.1-sol",
		"gpt-6-astra",
		"gpt-6-sol",
		"gpt-6-luna",
		"gpt-reserve",
		"gpt-5.6-sol",
		"gpt-5.6-terra",
		"gpt-5.6-luna",
		"gpt-5.5",
	}

	for _, modelID := range canonicalTemplateModels {
		t.Run(modelID, func(t *testing.T) {
			available := []map[string]any{{"id": modelID}}
			// A nil provider resolver uses the pure Codex template default.
			resp := BuildResponseForClientWithToolCapabilities(available, nil, nil, nil, false, "0.153.4")
			entries, ok := resp["models"].([]map[string]any)
			if !ok || len(entries) != 1 {
				t.Fatalf("expected 1 model entry, got %v", resp["models"])
			}
			entry := entries[0]
			if got, present := entry["apply_patch_tool_type"]; !present || got != "freeform" {
				t.Fatalf("model %s: apply_patch_tool_type = %#v (present: %t), want %q", modelID, got, present, "freeform")
			}
		})
	}

	// Non-template models without freeform in the template still default to null.
	t.Run("non-template model defaults to null", func(t *testing.T) {
		available := []map[string]any{{"id": "non-template-custom-model"}}
		resp := BuildResponseForClientWithToolCapabilities(available, nil, nil, nil, false, "0.153.4")
		entries, ok := resp["models"].([]map[string]any)
		if !ok || len(entries) != 1 {
			t.Fatalf("expected 1 model entry, got %v", resp["models"])
		}
		entry := entries[0]
		if got, present := entry["apply_patch_tool_type"]; !present || got != nil {
			t.Fatalf("non-template model: apply_patch_tool_type = %#v (present: %t), want nil", got, present)
		}
	})
}
