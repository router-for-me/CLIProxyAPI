package models

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestCatalogExposesCanonicalIdentityWithoutChangingRoute(t *testing.T) {
	r := registry.GetGlobalRegistry()
	const client = "canonical-identity-test"
	const alias = "personal/codex-oauth/gpt-5.6-sol"
	r.RegisterClient(client, "codex", []*registry.ModelInfo{{ID: alias, MetadataModelID: "gpt-5.6-sol"}})
	t.Cleanup(func() { r.UnregisterClient(client) })
	entries := buildCodexClientModels([]map[string]any{{"id": alias}, {"id": "unknown/A"}}, r.GetModelProviders, nil, false, "")
	if len(entries) != 2 || entries[0]["slug"] != alias || entries[0]["canonical_model_id"] != "gpt-5.6-sol" {
		t.Fatalf("explicit identity or route lost: %#v", entries)
	}
	if entries[1]["canonical_model_id"] != "unknown/A" {
		t.Fatal("unknown identity must not be guessed from a suffix")
	}
}
