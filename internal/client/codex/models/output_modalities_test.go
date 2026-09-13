package models

import (
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestCatalogPreservesDeclaredOutputModalities(t *testing.T) {
	r := registry.GetGlobalRegistry()
	client := "catalog-output-modalities-test"
	r.RegisterClient(client, "opencode-go", []*registry.ModelInfo{
		{ID: "opencode-go/lab-output", SupportedOutputModalities: []string{"text", "image"}},
		{ID: "opencode-go/lab-unknown"},
	})
	t.Cleanup(func() { r.UnregisterClient(client) })
	for _, model := range buildCodexClientModels([]map[string]any{
		{"id": "opencode-go/lab-output"}, {"id": "opencode-go/lab-unknown"},
	}, r.GetModelProviders, nil, false, "pi") {
		if model["slug"] == "opencode-go/lab-output" {
			if !reflect.DeepEqual(model["output_modalities"], []string{"text", "image"}) {
				t.Fatalf("output modalities lost: %#v", model["output_modalities"])
			}
		} else if _, exists := model["output_modalities"]; exists {
			t.Fatal("unknown output modalities must remain absent")
		}
	}
}

func TestCatalogSeparatesProviderInputsFromCodexProjection(t *testing.T) {
	r := registry.GetGlobalRegistry()
	const client = "catalog-provider-inputs-test"
	const id = "opencode-go/gpt-5.6-luna"
	declared := []string{"text", "image", "audio", "video", "pdf"}
	r.RegisterClient(client, "opencode-go", []*registry.ModelInfo{{
		ID: id, ContextLength: 1050000, MaxContextLength: 1050000,
		SupportedInputModalities: declared, ExplicitInputModalities: true,
	}})
	t.Cleanup(func() { r.UnregisterClient(client) })
	models := buildCodexClientModels(r.GetAvailableModels("openai"), r.GetModelProviders, nil, false, "")
	for _, model := range models {
		if model["slug"] != id {
			continue
		}
		if model["context_window"] != 1050000 || model["max_context_window"] != 1050000 {
			t.Fatalf("template replaced context: %+v", model)
		}
		if !reflect.DeepEqual(model["supported_input_modalities"], declared) {
			t.Fatalf("provider inputs lost: %+v", model)
		}
		if !reflect.DeepEqual(model["input_modalities"], []any{"text", "image"}) {
			t.Fatalf("invalid Codex input types: %+v", model["input_modalities"])
		}
		return
	}
	t.Fatal("model missing")
}

func TestCatalogSharedRouteModalitiesRemainConservative(t *testing.T) {
	r := registry.GetGlobalRegistry()
	const id = "catalog-shared/gpt-5.6-luna"
	r.RegisterClient("catalog-shared-a", "claude", []*registry.ModelInfo{{ID: id, MetadataModelID: "gpt-5.6-luna", SupportedInputModalities: []string{"text"}, SupportedOutputModalities: []string{"text"}, ExplicitInputModalities: true}})
	r.RegisterClient("catalog-shared-b", "codex", []*registry.ModelInfo{{ID: id, MetadataModelID: "gpt-5.6-luna", SupportedInputModalities: []string{"text", "image", "audio"}, SupportedOutputModalities: []string{"text", "image"}, ExplicitInputModalities: true}})
	t.Cleanup(func() { r.UnregisterClient("catalog-shared-a"); r.UnregisterClient("catalog-shared-b") })
	for _, entry := range buildCodexClientModels(r.GetAvailableModels("openai"), r.GetModelProviders, nil, false, "pi") {
		if entry["slug"] != id {
			continue
		}
		if !reflect.DeepEqual(entry["input_modalities"], []any{"text"}) || !reflect.DeepEqual(entry["supported_input_modalities"], []string{"text"}) || !reflect.DeepEqual(entry["output_modalities"], []string{"text"}) {
			t.Fatalf("rich capabilities disagree with selectable routes: %#v", entry)
		}
		return
	}
	t.Fatal("shared route missing")
}
