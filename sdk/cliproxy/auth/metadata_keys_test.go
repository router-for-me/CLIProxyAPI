package auth

import (
	"context"
	"reflect"
	"testing"
)

func TestNormalizeCredentialMetadata(t *testing.T) {
	metadata := map[string]any{
		"api-key":               "legacy-key",
		"base-url":              "https://legacy.example",
		"disable-cooling":       true,
		"excluded-models":       []any{"legacy-model"},
		"fingerprint-profile":   "claude-code-cli",
		"model-aliases":         []any{map[string]any{"name": "upstream", "alias": "public"}},
		"proxy-url":             "http://legacy-proxy.example",
		"request-retry":         3,
		"request_retry":         0,
		"request-scoped-errors": []any{map[string]any{"status": 429}},
		"tool-prefix-disabled":  true,
		"provider_field":        "preserved",
	}

	NormalizeCredentialMetadata(metadata)

	want := map[string]any{
		"api_key":               "legacy-key",
		"base_url":              "https://legacy.example",
		"disable_cooling":       true,
		"excluded_models":       []any{"legacy-model"},
		"fingerprint_profile":   "claude-code-cli",
		"model_aliases":         []any{map[string]any{"name": "upstream", "alias": "public"}},
		"proxy_url":             "http://legacy-proxy.example",
		"request_retry":         0,
		"request_scoped_errors": []any{map[string]any{"status": 429}},
		"tool_prefix_disabled":  true,
		"provider_field":        "preserved",
	}
	if !reflect.DeepEqual(metadata, want) {
		t.Fatalf("NormalizeCredentialMetadata() = %#v, want %#v", metadata, want)
	}
}

func TestCanonicalCredentialMetadataKeyPreservesUnknownKeys(t *testing.T) {
	if got := CanonicalCredentialMetadataKey("provider-specific-key"); got != "provider-specific-key" {
		t.Fatalf("CanonicalCredentialMetadataKey() = %q, want provider-specific-key", got)
	}
}

func TestManagerRegisterNormalizesCredentialMetadata(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "legacy-auth",
		Provider: "codex",
		Metadata: map[string]any{
			"request-retry": 2,
			"request_retry": 0,
		},
	}

	registered, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	if got, ok := registered.Metadata["request_retry"]; !ok || got != 0 {
		t.Fatalf("registered request_retry = %#v, want 0", got)
	}
	if _, exists := registered.Metadata["request-retry"]; exists {
		t.Fatalf("registered metadata retained legacy key: %#v", registered.Metadata)
	}
}
