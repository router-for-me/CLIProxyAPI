package models

import (
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestCodexCatalogApplyPatchCapability(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "catalog-patch-metadata-test"
	modelRegistry.RegisterClient(clientID, "custom-compat", []*registry.ModelInfo{
		{ID: "catalog-patch-alias", MetadataModelID: "gpt-5.5"},
		{ID: "catalog-patch-image", Type: registry.OpenAIImageModelType},
		{ID: "catalog-patch-image-input", SupportedInputModalities: []string{"image"}},
	})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	for _, version := range []string{"", "0.137.0", "0.153.4", "cpa"} {
		for _, model := range []struct {
			id      string
			canText bool
		}{
			{"gpt-5.5", true},
			{"custom-model", true},
			{"catalog-patch-alias", true},
			{"team/gpt-5.5", true},
			{"gpt-reserve", true},
			{"gpt-image-2", false},
			{"team/gpt-image-2", false},
			{"team/nested/gpt-image-2", false},
			{"GPT-IMAGE-2", false},
			{"grok-imagine-video", false},
			{"catalog-patch-image", false},
			{"catalog-patch-image-input", false},
		} {
			for _, providerLookup := range []struct {
				name string
				get  ProvidersForModelFunc
			}{
				{"nil", nil},
				{"custom", func(string) []string { return []string{"custom-compat"} }},
				{"mixed", func(string) []string { return []string{"codex", "custom-compat"} }},
			} {
				for _, capability := range []struct {
					name string
					get  ApplyPatchCapabilityForModelFunc
					want bool
				}{
					{"nil", nil, false},
					{"unsupported", func(string) bool { return false }, false},
					{"supported", func(id string) bool {
						if id != model.id {
							t.Errorf("capability queried %q, want exact public ID %q", id, model.id)
						}
						return true
					}, model.canText},
				} {
					t.Run(version+"/"+model.id+"/"+providerLookup.name+"/"+capability.name, func(t *testing.T) {
						available := []map[string]any{{"id": " " + model.id + " "}}
						webSearch := func(string) *bool { supported := false; return &supported }
						baseline := BuildResponseForClientWithCPACapabilities(available, providerLookup.get, webSearch, true, version)
						response := BuildResponseForClientWithToolCapabilities(available, providerLookup.get, webSearch, capability.get, true, version)
						entries := response["models"].([]map[string]any)
						if len(entries) != 1 {
							t.Fatalf("models count = %d, want 1", len(entries))
						}
						entry := entries[0]
						value, present := entry["apply_patch_tool_type"]
						var want any
						if capability.want {
							want = "freeform"
						} else if capability.get == nil && providerLookup.name == "nil" {
							switch model.id {
							case "gpt-5.5", "catalog-patch-alias", "team/gpt-5.5", "gpt-reserve":
								want = "freeform"
							}
						}
						if !present || value != want {
							t.Fatalf("apply_patch_tool_type = %#v (present %v), want %#v", value, present, want)
						}
						// Capability opt-in must not modify any other catalog metadata.
						delete(entry, "apply_patch_tool_type")
						delete(baseline["models"].([]map[string]any)[0], "apply_patch_tool_type")
						if !reflect.DeepEqual(response, baseline) {
							t.Fatal("apply_patch opt-in changed unrelated metadata")
						}
					})
				}
			}
		}
	}
}

func TestCodexCatalogApplyPatchLegacyEntryPointsUnknown(t *testing.T) {
	available := []map[string]any{{"id": "gpt-5.5"}, {"id": "custom-model"}, {"id": "gpt-reserve"}}
	responses := []map[string]any{
		BuildResponse(available, nil, false),
		BuildResponseForClient(available, nil, false, "0.153.4"),
		BuildResponseForClientWithCPACapabilities(available, nil, nil, false, "cpa"),
	}
	for _, response := range responses {
		for _, entry := range response["models"].([]map[string]any) {
			slug := stringModelValue(entry, "slug")
			switch slug {
			case "gpt-5.5", "gpt-reserve":
				if got, _ := entry["apply_patch_tool_type"].(string); got != "freeform" {
					t.Fatalf("model %s: apply_patch_tool_type = %#v, want freeform", slug, entry["apply_patch_tool_type"])
				}
			default:
				assertCodexNullableFieldCleared(t, entry, "apply_patch_tool_type")
			}
		}
	}
}

func TestApplyPatchFieldModalities(t *testing.T) {
	for _, tc := range []struct {
		name       string
		visibility string
		modalities any
		want       any
	}{
		{"hidden-text-any", "hide", []any{"text", "image"}, "freeform"},
		{"hidden-text-strings", "hide", []string{"text"}, "freeform"},
		{"hidden-image-any", "hide", []any{"image"}, nil},
		{"hidden-image-strings", "hide", []string{"image"}, nil},
		{"hidden-unknown", "hide", nil, nil},
		{"hidden-empty", "hide", []any{}, nil},
		{"public-image", "list", []any{"image"}, nil},
		{"public-unconstrained", "list", []string{}, "freeform"},
		{"public-text", "list", []string{"text"}, "freeform"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := map[string]any{"visibility": tc.visibility, "input_modalities": tc.modalities, "apply_patch_tool_type": "function"}
			called := false
			applyCodexClientApplyPatchCapability(entry, " public-alias ", func(id string) bool {
				called = true
				if id != "public-alias" {
					t.Fatalf("queried ID = %q, want public-alias", id)
				}
				return true
			})
			if value, present := entry["apply_patch_tool_type"]; !present || value != tc.want {
				t.Fatalf("apply_patch_tool_type = %#v (present %v), want %#v", value, present, tc.want)
			}
			if called != (tc.want != nil) {
				t.Fatalf("callback called = %v, want %v", called, tc.want != nil)
			}
		})
	}
}

func TestCodexCatalogApplyPatch_TemplateModelsRetainFreeformByDefault_Issue6286(t *testing.T) {
	// Models defined in codex_client_models.json with "apply_patch_tool_type": "freeform"
	// must retain "freeform" under pure Codex providers even when enable-apply-patch is false (capability == nil).
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
			// capability is nil (simulates client.codex.enable-apply-patch: false)
			// providersForModel is nil (pure Codex default)
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

	// Non-template models without freeform in template must still default to null when capability is nil.
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
