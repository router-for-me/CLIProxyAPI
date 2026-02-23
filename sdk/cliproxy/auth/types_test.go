package auth

import (
	"strings"
	"testing"
)

func TestToolPrefixDisabled(t *testing.T) {
	var a *Auth
	if a.ToolPrefixDisabled() {
		t.Error("nil auth should return false")
	}

	a = &Auth{}
	if a.ToolPrefixDisabled() {
		t.Error("empty auth should return false")
	}

	a = &Auth{Metadata: map[string]any{"tool_prefix_disabled": true}}
	if !a.ToolPrefixDisabled() {
		t.Error("should return true when set to true")
	}

	a = &Auth{Metadata: map[string]any{"tool_prefix_disabled": "true"}}
	if !a.ToolPrefixDisabled() {
		t.Error("should return true when set to string 'true'")
	}

	a = &Auth{Metadata: map[string]any{"tool-prefix-disabled": true}}
	if !a.ToolPrefixDisabled() {
		t.Error("should return true with kebab-case key")
	}

	a = &Auth{Metadata: map[string]any{"tool_prefix_disabled": false}}
	if a.ToolPrefixDisabled() {
		t.Error("should return false when set to false")
	}
}

func TestStableAuthIndex_UsesFullDigest(t *testing.T) {
	idx := stableAuthIndex("seed-value")
	if len(idx) != 64 {
		t.Fatalf("stableAuthIndex length = %d, want 64", len(idx))
	}
}

func TestEnsureIndex_DoesNotUseAPIKeySeed(t *testing.T) {
	a := &Auth{
		ID: "auth-id-1",
		Attributes: map[string]string{
			"api_key": "sensitive-token",
		},
	}
	idx := a.EnsureIndex()
	if idx == "" {
		t.Fatal("expected non-empty index")
	}
	if idx != stableAuthIndex("id:"+a.ID) {
		t.Fatalf("EnsureIndex = %q, want id-derived index", idx)
	}
	if strings.Contains(idx, "sensitive-token") {
		t.Fatal("index should not include API key material")
	}
}
