package qoder

import (
	"testing"
)

func TestParseCatalogUsesAccountModelsAndCapabilities(t *testing.T) {
	body := []byte(`{
		"assistant": [
			{"key":"auto","display_name":"Auto","enable":true,"max_input_tokens":180000,"max_output_tokens":32000,"is_vl":true,"thinking_config":{"disabled":{},"enabled":{"efforts":{"low":{},"high":{},"max":{}}}}},
			{"key":"disabled-model","enable":false}
		],
		"byok_enterprise": [
			{"key":"vendor/custom","display_name":"Custom","enable":true,"max_input_tokens":64000,"is_reasoning":true},
			{"key":"auto","display_name":"Duplicate","enable":true}
		]
	}`)
	catalog, err := parseCatalog(body, ProviderCN)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models) != 2 {
		t.Fatalf("models = %d, want 2", len(catalog.Models))
	}
	first := catalog.Models[0]
	if first.ID != "auto" || first.OwnedBy != ProviderCN || first.ContextLength != 212000 || first.MaxCompletionTokens != 32000 {
		t.Fatalf("first model = %+v", first)
	}
	if len(first.SupportedInputModalities) != 2 || first.Thinking == nil || !first.Thinking.ZeroAllowed || len(first.Thinking.Levels) != 3 {
		t.Fatalf("capabilities = %+v", first)
	}
	if catalog.Models[1].ID != "vendor/custom" {
		t.Fatalf("upstream model ID changed: %q", catalog.Models[1].ID)
	}
	if string(catalog.Configs["vendor/custom"]) == "" || string(catalog.Configs["disabled-model"]) != "" {
		t.Fatal("raw model config cache does not match enabled catalog")
	}
}

func TestCatalogCacheReturnsDefensiveCopies(t *testing.T) {
	catalog, err := parseCatalog([]byte(`{"assistant":[{"key":"auto","enable":true,"thinking_config":{"enabled":{"efforts":{"low":{}}}}}]}`), ProviderGlobal)
	if err != nil {
		t.Fatal(err)
	}
	authID := t.Name()
	CacheCatalog(authID, catalog)
	t.Cleanup(func() { DropCatalog(authID) })
	catalog.Models[0].ID = "mutated"
	catalog.Configs["auto"][0] = 'X'
	config, err := CachedModelConfig(authID, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if config["key"] != "auto" {
		t.Fatalf("cached config = %v", config)
	}
	config["key"] = "changed-again"
	again, err := CachedModelConfig(authID, "auto")
	if err != nil || again["key"] != "auto" {
		t.Fatalf("cached config was mutated: %v, %v", again, err)
	}
}

func TestParseCatalogRejectsEmptyOrDisabledCatalog(t *testing.T) {
	for _, body := range []string{`{}`, `{"assistant":[]}`, `{"assistant":[{"key":"auto","enable":false}]}`} {
		if _, err := parseCatalog([]byte(body), ProviderGlobal); err == nil {
			t.Fatalf("parseCatalog(%s) succeeded", body)
		}
	}
}
