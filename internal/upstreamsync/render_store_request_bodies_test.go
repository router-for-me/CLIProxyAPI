package upstreamsync

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// TestRenderPropagatesStoreRequestBodies verifies the per-row capture toggle
// and stable provider id are projected onto the rendered config key.
func TestRenderPropagatesStoreRequestBodies(t *testing.T) {
	providers := []store.UpstreamProvider{{
		ID:                 7,
		ProviderType:       "claude-api-key",
		Name:               "p",
		StoreRequestBodies: true,
		APIKeyEntries:      []store.UpstreamProviderAPIKey{{ID: 1, APIKey: "sk-1"}},
	}}
	out := RenderConfig(providers)
	if len(out.ClaudeKey) != 1 {
		t.Fatalf("want 1 claude key, got %d", len(out.ClaudeKey))
	}
	if !out.ClaudeKey[0].StoreRequestBodies || out.ClaudeKey[0].UpstreamProviderID != 7 {
		t.Fatalf("toggle/id not rendered: %+v", out.ClaudeKey[0])
	}
}

// TestRenderAuthFileStoreRequestBodies verifies the OAuth auth-file metadata
// carries the toggle only when enabled.
func TestRenderAuthFileStoreRequestBodies(t *testing.T) {
	on, err := RenderAuthFile(store.UpstreamProvider{ProviderType: "oauth:claude", StoreRequestBodies: true})
	if err != nil {
		t.Fatalf("render on: %v", err)
	}
	var onMeta map[string]any
	if err := json.Unmarshal(on, &onMeta); err != nil {
		t.Fatalf("unmarshal on: %v", err)
	}
	if v, ok := onMeta["store_request_bodies"].(bool); !ok || !v {
		t.Fatalf("expected store_request_bodies=true, got %#v", onMeta["store_request_bodies"])
	}

	off, err := RenderAuthFile(store.UpstreamProvider{ProviderType: "oauth:claude"})
	if err != nil {
		t.Fatalf("render off: %v", err)
	}
	var offMeta map[string]any
	if err := json.Unmarshal(off, &offMeta); err != nil {
		t.Fatalf("unmarshal off: %v", err)
	}
	if _, ok := offMeta["store_request_bodies"]; ok {
		t.Fatalf("store_request_bodies must be absent when disabled: %#v", offMeta)
	}
}
