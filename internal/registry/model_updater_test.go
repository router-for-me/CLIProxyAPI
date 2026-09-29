package registry

import (
	"encoding/json"
	"testing"
)

func TestDetectChangedProviders_CodexConfigurationUpdate(t *testing.T) {
	oldData := &staticModelsJSON{
		CodexFree: []*ModelInfo{{ID: "gpt-6-luna"}},
	}
	newData := &staticModelsJSON{
		CodexFree: []*ModelInfo{{ID: "gpt-6-luna", SupportConfigurationUpdate: true}},
	}

	changed := detectChangedProviders(oldData, newData)
	if len(changed) != 1 || changed[0] != "codex" {
		t.Fatalf("configuration_update-only change: got providers %v, want [codex]", changed)
	}
}

func TestDetectChangedProviders_KimiAliases(t *testing.T) {
	oldData := &staticModelsJSON{
		Kimi: []*ModelInfo{{ID: "kimi-k2"}},
	}
	newData := &staticModelsJSON{
		Kimi: []*ModelInfo{{ID: "kimi-k2"}, {ID: "kimi-k3"}},
	}

	changed := detectChangedProviders(oldData, newData)
	expected := map[string]bool{
		"kimi":     false,
		"kimi-ai":  false,
		"kimi.ai":  false,
		"kimi.com": false,
	}

	for _, p := range changed {
		if _, ok := expected[p]; ok {
			expected[p] = true
		}
	}

	for p, found := range expected {
		if !found {
			t.Errorf("expected changed provider %q to be reported, got %v", p, changed)
		}
	}
}

func TestDetectChangedProviders_Cline(t *testing.T) {
	oldData := &staticModelsJSON{
		Cline: []*ModelInfo{{ID: "anthropic/claude-sonnet-4-6"}},
	}
	newData := &staticModelsJSON{
		Cline: []*ModelInfo{{ID: "anthropic/claude-sonnet-4-6"}, {ID: "cline-pass/glm-5.3"}},
	}

	changed := detectChangedProviders(oldData, newData)
	found := false
	for _, p := range changed {
		if p == "cline" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected cline change to be reported, got %v", changed)
	}
}

func TestPreserveCatalogFallbacksKeepsClineWhenRemoteOmitsIt(t *testing.T) {
	oldData := &staticModelsJSON{
		Cline: []*ModelInfo{{ID: "anthropic/claude-sonnet-4-6"}},
	}
	newData := &staticModelsJSON{}

	preserveCatalogFallbacks(oldData, newData)

	if len(newData.Cline) != 1 || newData.Cline[0].ID != "anthropic/claude-sonnet-4-6" {
		t.Fatalf("cline section lost after remote refresh: %+v", newData.Cline)
	}
}

func TestPreserveCatalogFallbacksKeepsMetaWhenRemoteOmitsIt(t *testing.T) {
	oldData := &staticModelsJSON{
		Meta: []*ModelInfo{{ID: "claude-sonnet-4"}},
	}
	newData := &staticModelsJSON{}

	preserveCatalogFallbacks(oldData, newData)

	if len(newData.Meta) != 1 || newData.Meta[0].ID != "claude-sonnet-4" {
		t.Fatalf("meta section not preserved: %+v", newData.Meta)
	}
}

func TestPreserveCatalogFallbacksKeepsRemoteClineWhenPresent(t *testing.T) {
	oldData := &staticModelsJSON{
		Cline: []*ModelInfo{{ID: "anthropic/claude-sonnet-4-6"}},
	}
	newData := &staticModelsJSON{
		Cline: []*ModelInfo{{ID: "cline-pass/glm-5.3"}},
	}

	preserveCatalogFallbacks(oldData, newData)

	if len(newData.Cline) != 1 || newData.Cline[0].ID != "cline-pass/glm-5.3" {
		t.Fatalf("remote cline section must win when non-empty: %+v", newData.Cline)
	}
}

// TestClineModelsSurviveRemoteCatalogRefresh reproduces the live regression:
// the embedded catalog advertises cline models, a successful remote refresh
// (which today lacks a cline section entirely) must not wipe them, or every
// subsequent auth-file re-registration drops the provider's models.
func TestClineModelsSurviveRemoteCatalogRefresh(t *testing.T) {
	embedded := `{"claude": [{"id": "claude-sonnet-4"}], "cline": [{"id": "anthropic/claude-sonnet-4-6"}]}`
	remote := `{"claude": [{"id": "claude-sonnet-5"}]}`

	if errLoad := loadModelsFromBytes([]byte(embedded), "test-embedded"); errLoad != nil {
		t.Fatalf("load embedded catalog: %v", errLoad)
	}
	defer func() {
		if errLoad := loadModelsFromBytes(embeddedModelsJSON, "embed"); errLoad != nil {
			t.Fatalf("restore embedded catalog: %v", errLoad)
		}
	}()

	oldData := getModels()
	var remoteParsed staticModelsJSON
	if err := json.Unmarshal([]byte(remote), &remoteParsed); err != nil {
		t.Fatalf("parse remote catalog: %v", err)
	}
	preserveCatalogFallbacks(oldData, &remoteParsed)
	if errLoad := loadModelsFromBytes(mustMarshalJSON(t, &remoteParsed), "test-remote"); errLoad != nil {
		t.Fatalf("load remote catalog: %v", errLoad)
	}

	models := GetClineModels()
	if len(models) != 1 || models[0].ID != "anthropic/claude-sonnet-4-6" {
		t.Fatalf("GetClineModels() = %v after remote refresh without cline section", models)
	}
}

func mustMarshalJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}
