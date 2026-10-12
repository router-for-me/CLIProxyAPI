package registry

import (
	"encoding/json"
	"testing"
)

func liveCodexClientModel(t *testing.T, slug, displayName string) map[string]any {
	t.Helper()
	var payload codexClientModelsPayload
	if err := json.Unmarshal(embeddedCodexClientModelsJSON, &payload); err != nil {
		t.Fatalf("decode embedded catalog: %v", err)
	}
	if len(payload.Models) == 0 {
		t.Fatal("embedded catalog has no models")
	}
	model := make(map[string]any, len(payload.Models[0]))
	for key, value := range payload.Models[0] {
		model[key] = value
	}
	model["slug"] = slug
	model["display_name"] = displayName
	return model
}

func servedCodexClientModels(t *testing.T) map[string]string {
	t.Helper()
	var payload codexClientModelsPayload
	if err := json.Unmarshal(GetCodexClientModelsJSON(), &payload); err != nil {
		t.Fatalf("decode served catalog: %v", err)
	}
	served := make(map[string]string, len(payload.Models))
	for _, model := range payload.Models {
		slug, _ := model["slug"].(string)
		displayName, _ := model["display_name"].(string)
		served[slug] = displayName
	}
	return served
}

func resetCodexClientLiveOverlay(t *testing.T) {
	t.Helper()
	restore := func() {
		codexClientLiveOverlay.mu.Lock()
		codexClientLiveOverlay.models = nil
		codexClientLiveOverlay.order = nil
		codexClientLiveOverlay.mu.Unlock()
		if _, err := loadCodexClientModelsFromBytes(embeddedCodexClientModelsJSON, "test"); err != nil {
			t.Fatalf("restore embedded catalog: %v", err)
		}
	}
	restore()
	t.Cleanup(restore)
}

func TestMergeLiveCodexClientModelsAddsAndReplacesBySlug(t *testing.T) {
	resetCodexClientLiveOverlay(t)
	before := servedCodexClientModels(t)
	revisionBefore := GetCodexClientModelsRevision()

	payload, errMarshal := json.Marshal(map[string]any{"models": []any{
		liveCodexClientModel(t, "gpt-live-only", "Live Only"),
		liveCodexClientModel(t, "gpt-5.5", "Live GPT-5.5"),
		map[string]any{"slug": "gpt-invalid"},
	}})
	if errMarshal != nil {
		t.Fatalf("marshal live payload: %v", errMarshal)
	}

	changed, errMerge := MergeLiveCodexClientModels(payload)
	if errMerge != nil || !changed {
		t.Fatalf("MergeLiveCodexClientModels() = %t, %v; want a changed catalog", changed, errMerge)
	}
	after := servedCodexClientModels(t)
	if after["gpt-live-only"] != "Live Only" {
		t.Fatalf("live-only model = %q, want it appended", after["gpt-live-only"])
	}
	if after["gpt-5.5"] != "Live GPT-5.5" {
		t.Fatalf("gpt-5.5 display name = %q, want the live entry to replace the base one", after["gpt-5.5"])
	}
	if _, ok := after["gpt-invalid"]; ok {
		t.Fatal("an entry that fails catalog validation was served")
	}
	if len(after) != len(before)+1 {
		t.Fatalf("served %d models, want the %d base models plus one", len(after), len(before))
	}
	if GetCodexClientModelsRevision() == revisionBefore {
		t.Fatal("catalog revision did not change")
	}

	// The same payload again changes nothing.
	revisionMerged := GetCodexClientModelsRevision()
	if changed, errMerge = MergeLiveCodexClientModels(payload); errMerge != nil || changed {
		t.Fatalf("repeated merge = %t, %v; want no change", changed, errMerge)
	}
	if GetCodexClientModelsRevision() != revisionMerged {
		t.Fatal("repeated merge bumped the catalog revision")
	}

	// A base catalog refresh keeps the live entries laid over it.
	if _, errLoad := loadCodexClientModelsFromBytes(embeddedCodexClientModelsJSON, "remote"); errLoad != nil {
		t.Fatalf("reload base catalog: %v", errLoad)
	}
	if reloaded := servedCodexClientModels(t); reloaded["gpt-live-only"] != "Live Only" || reloaded["gpt-5.5"] != "Live GPT-5.5" {
		t.Fatalf("live entries were lost on a base refresh: %v", reloaded)
	}
	if GetCodexClientModelsRevision() != revisionMerged {
		t.Fatal("an unchanged base refresh bumped the catalog revision")
	}
}

func TestMergeLiveCodexClientModelsRejectsUnusablePayloads(t *testing.T) {
	resetCodexClientLiveOverlay(t)
	revisionBefore := GetCodexClientModelsRevision()

	for name, payload := range map[string]string{
		"not json":        `{"models":`,
		"no models":       `{"models":[]}`,
		"no valid models": `{"models":[{"slug":"gpt-invalid"}]}`,
	} {
		if changed, errMerge := MergeLiveCodexClientModels([]byte(payload)); errMerge == nil || changed {
			t.Fatalf("%s: MergeLiveCodexClientModels() = %t, %v; want an error", name, changed, errMerge)
		}
	}
	if GetCodexClientModelsRevision() != revisionBefore {
		t.Fatal("a rejected payload changed the served catalog")
	}
}
