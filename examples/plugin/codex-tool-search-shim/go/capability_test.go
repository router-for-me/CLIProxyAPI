package main

import (
	"encoding/json"
	"testing"
)

func TestCodexModelCatalogCapabilityRequiresExactBridgeModel(t *testing.T) {
	t.Cleanup(func() { _ = applyPluginConfig(nil) })
	if errConfigure := applyPluginConfig([]byte("bridge_models:\n  - route-model\n")); errConfigure != nil {
		t.Fatalf("applyPluginConfig() error = %v", errConfigure)
	}
	input := []byte(`{"models":[
		{"slug":"route-model","supports_search_tool":false},
		{"slug":"other-model","supports_search_tool":false}
	]}`)

	raw, errResponse := interceptResponse(mustJSON(t, map[string]any{
		"RequestID":    "capability-catalog",
		"SourceFormat": "openai",
		"Metadata":     map[string]any{"response_kind": "codex_client_models"},
		"Body":         input,
	}))
	if errResponse != nil {
		t.Fatalf("interceptResponse() error = %v", errResponse)
	}
	out := decodeResponseBodyResult(t, raw)
	var catalog struct {
		Models []map[string]any `json:"models"`
	}
	if errUnmarshal := json.Unmarshal(out, &catalog); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	if catalog.Models[0]["supports_search_tool"] != true {
		t.Fatalf("bridge model capability = %#v", catalog.Models[0])
	}
	if catalog.Models[1]["supports_search_tool"] != false {
		t.Fatalf("unconfigured model capability = %#v", catalog.Models[1])
	}
}

func TestCodexModelCatalogWithoutBridgeModelsIsUntouched(t *testing.T) {
	t.Cleanup(func() { _ = applyPluginConfig(nil) })
	input := []byte(`{"models":[{"slug":"route-model","supports_search_tool":false}]}`)
	raw, errResponse := interceptResponse(mustJSON(t, map[string]any{
		"RequestID":    "capability-catalog-disabled",
		"SourceFormat": "openai",
		"Metadata":     map[string]any{"response_kind": "codex_client_models"},
		"Body":         input,
	}))
	if errResponse != nil {
		t.Fatalf("interceptResponse() error = %v", errResponse)
	}
	if body := decodeResponseBodyResult(t, raw); len(body) != 0 {
		t.Fatalf("disabled capability catalog changed: %s", body)
	}
}

func TestNativeBridgeModelProfileIsIndependentOfStrictProfile(t *testing.T) {
	t.Cleanup(func() { _ = applyPluginConfig(nil) })
	if errConfigure := applyPluginConfig([]byte("bridge_native_models:\n  - native-route\n")); errConfigure != nil {
		t.Fatalf("applyPluginConfig() error = %v", errConfigure)
	}
	if policy := requestPolicyForModels("request_after", "openai-response", "openai-response", "native-route", ""); !policy.bridge || !policy.native {
		t.Fatalf("native bridge policy = %#v", policy)
	}
	if policy := requestPolicyForModels("request_after", "openai-response", "openai-response", "other-model", ""); policy.bridge || !policy.native {
		t.Fatalf("native passthrough policy = %#v", policy)
	}
}

func TestRequestPolicyRetainsConfigurationSnapshotAcrossReconfigure(t *testing.T) {
	t.Cleanup(func() { _ = applyPluginConfig(nil) })
	configYAML := []byte(`
strict_responses_models:
  - strict-route
bridge_native_models:
  - native-route
extra_source_formats:
  - custom-responses
max_active_tool_bytes: 1024
max_request_states: 7
max_state_bytes: 2048
max_schema_expansion_bytes: 4096
max_schema_expansion_nodes: 99
max_schema_depth: 12
strip_custom_tools: true
`)
	if errConfigure := applyPluginConfig(configYAML); errConfigure != nil {
		t.Fatalf("applyPluginConfig() error = %v", errConfigure)
	}
	policy := requestPolicyForModels("request_after", "custom-responses", "openai-response", "native-route", "")
	if errReset := applyPluginConfig(nil); errReset != nil {
		t.Fatalf("applyPluginConfig(nil) error = %v", errReset)
	}

	if !isResponsesFormatWithConfig("custom-responses", policy.config) {
		t.Fatal("request policy lost its extra source format")
	}
	if !strictNativeModelMatchesWithConfig(policy.config, "strict-route") {
		t.Fatal("request policy lost its strict model profile")
	}
	if len(policy.config.bridgeNativeModels) != 1 || policy.config.bridgeNativeModels[0] != "native-route" {
		t.Fatalf("request policy bridge models = %#v", policy.config.bridgeNativeModels)
	}
	if policy.config.toolBudget.MaxToolBytes != 1024 ||
		policy.config.stateBudget.MaxStates != 7 ||
		policy.config.stateBudget.MaxBytes != 2048 ||
		policy.config.schemaBudget.MaxBytes != 4096 ||
		policy.config.schemaBudget.MaxNodes != 99 ||
		policy.config.schemaBudget.MaxDepth != 12 ||
		!policy.config.stripCustomTools {
		t.Fatalf("request policy budgets = %#v", policy.config)
	}
	if isResponsesFormat("custom-responses") {
		t.Fatal("current configuration retained the replaced source format")
	}
}

func TestRegistrationDeclaresLifecycleAndCompleteConfiguration(t *testing.T) {
	registration := currentRegistration()
	if !registration.Capabilities.RequestLifecyclePlugin {
		t.Fatal("request_lifecycle_plugin is false")
	}
	fields := make(map[string]struct{}, len(registration.Metadata.ConfigFields))
	for _, field := range registration.Metadata.ConfigFields {
		fields[field.Name] = struct{}{}
	}
	for _, name := range []string{
		"bridge_models",
		"bridge_native_models",
		"max_active_tool_bytes",
		"max_request_states",
		"max_state_bytes",
		"max_schema_expansion_bytes",
		"max_schema_expansion_nodes",
		"max_schema_depth",
		"strip_custom_tools",
	} {
		if _, exists := fields[name]; !exists {
			t.Errorf("missing config field %q", name)
		}
	}
}
