package executor

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestBuildCacheControl_Default(t *testing.T) {
	cc := buildCacheControl("")
	if cc["type"] != "ephemeral" {
		t.Fatalf("expected type=ephemeral, got %q", cc["type"])
	}
	if _, ok := cc["ttl"]; ok {
		t.Fatal("expected no ttl field for default TTL")
	}
}

func TestBuildCacheControl_1h(t *testing.T) {
	cc := buildCacheControl("1h")
	if cc["type"] != "ephemeral" {
		t.Fatalf("expected type=ephemeral, got %q", cc["type"])
	}
	if cc["ttl"] != "1h" {
		t.Fatalf("expected ttl=1h, got %q", cc["ttl"])
	}
}

func TestResolvePromptCacheTTL_NilConfig(t *testing.T) {
	if got := resolvePromptCacheTTL(nil, nil); got != "" {
		t.Fatalf("expected empty TTL with nil config, got %q", got)
	}
}

func TestResolvePromptCacheTTL_GlobalConfig(t *testing.T) {
	cfg := &config.Config{PromptCacheTTL: "1h"}
	if got := resolvePromptCacheTTL(cfg, nil); got != "1h" {
		t.Fatalf("expected 1h from global config, got %q", got)
	}
}

func TestResolvePromptCacheTTL_PerKeyOverridesGlobal(t *testing.T) {
	cfg := &config.Config{PromptCacheTTL: ""}
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"prompt_cache_ttl": "1h"},
	}
	if got := resolvePromptCacheTTL(cfg, auth); got != "1h" {
		t.Fatalf("expected 1h from per-key attribute, got %q", got)
	}
}

func TestResolvePromptCacheTTL_PerKeyEmpty_FallsBackToGlobal(t *testing.T) {
	cfg := &config.Config{PromptCacheTTL: "1h"}
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"prompt_cache_ttl": ""},
	}
	if got := resolvePromptCacheTTL(cfg, auth); got != "1h" {
		t.Fatalf("expected 1h from global fallback, got %q", got)
	}
}

func TestEnsureCacheControl_InjectsWith1hTTL(t *testing.T) {
	payload := []byte(`{
		"model": "claude-sonnet-4",
		"system": "You are helpful.",
		"messages": [
			{"role": "user", "content": "first"},
			{"role": "assistant", "content": "ok"},
			{"role": "user", "content": "second"}
		]
	}`)

	result := ensureCacheControl(payload, "1h")

	var body map[string]interface{}
	if err := json.Unmarshal(result, &body); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	// System should have been converted to array with ttl=1h
	system, ok := body["system"].([]interface{})
	if !ok || len(system) == 0 {
		t.Fatal("expected system to be a non-empty array")
	}
	lastBlock := system[len(system)-1].(map[string]interface{})
	cc, ok := lastBlock["cache_control"].(map[string]interface{})
	if !ok {
		t.Fatal("expected cache_control on last system block")
	}
	if cc["ttl"] != "1h" {
		t.Fatalf("expected ttl=1h on system block, got %v", cc["ttl"])
	}
}

func TestEnsureCacheControl_DefaultTTLHasNoTTLField(t *testing.T) {
	payload := []byte(`{
		"model": "claude-sonnet-4",
		"system": "You are helpful.",
		"messages": [
			{"role": "user", "content": "first"},
			{"role": "assistant", "content": "ok"},
			{"role": "user", "content": "second"}
		]
	}`)

	result := ensureCacheControl(payload, "")

	var body map[string]interface{}
	if err := json.Unmarshal(result, &body); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	system := body["system"].([]interface{})
	lastBlock := system[len(system)-1].(map[string]interface{})
	cc := lastBlock["cache_control"].(map[string]interface{})
	if _, hasTTL := cc["ttl"]; hasTTL {
		t.Fatal("default TTL should not include ttl field")
	}
}
