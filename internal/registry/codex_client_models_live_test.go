package registry

import (
	"bytes"
	"encoding/json"
	"sync"
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

func liveCodexClientPayload(t *testing.T, models ...any) []byte {
	t.Helper()
	payload, errMarshal := json.Marshal(map[string]any{"models": models})
	if errMarshal != nil {
		t.Fatalf("marshal live payload: %v", errMarshal)
	}
	return payload
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
		codexClientLiveOverlay.sources = nil
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

	payload := liveCodexClientPayload(t,
		liveCodexClientModel(t, "gpt-live-only", "Live Only"),
		liveCodexClientModel(t, "gpt-5.5", "Live GPT-5.5"),
		map[string]any{"slug": "gpt-invalid"},
	)

	changed, errMerge := MergeLiveCodexClientModels("codex-a", payload)
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
	if changed, errMerge = MergeLiveCodexClientModels("codex-a", payload); errMerge != nil || changed {
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
		if changed, errMerge := MergeLiveCodexClientModels("codex-a", []byte(payload)); errMerge == nil || changed {
			t.Fatalf("%s: MergeLiveCodexClientModels() = %t, %v; want an error", name, changed, errMerge)
		}
	}
	if GetCodexClientModelsRevision() != revisionBefore {
		t.Fatal("a rejected payload changed the served catalog")
	}
}

func TestLiveCodexClientModelsFollowTheCredentialsThatReportThem(t *testing.T) {
	resetCodexClientLiveOverlay(t)
	base := GetCodexClientModelsJSON()

	fromA := liveCodexClientPayload(t, liveCodexClientModel(t, "gpt-shared-live", "Shared from A"), liveCodexClientModel(t, "gpt-a-only-live", "A only"))
	fromB := liveCodexClientPayload(t, liveCodexClientModel(t, "gpt-shared-live", "Shared from B"), liveCodexClientModel(t, "gpt-b-only-live", "B only"))

	// Credentials that report different entries for a slug: the lowest ID is
	// served, whichever was read last.
	for _, order := range [][]string{{"codex-b", "codex-a"}, {"codex-a", "codex-b"}} {
		resetCodexClientLiveOverlay(t)
		for _, id := range order {
			payload := fromA
			if id == "codex-b" {
				payload = fromB
			}
			if _, errMerge := MergeLiveCodexClientModels(id, payload); errMerge != nil {
				t.Fatalf("merge %s: %v", id, errMerge)
			}
		}
		served := servedCodexClientModels(t)
		if served["gpt-shared-live"] != "Shared from A" || served["gpt-a-only-live"] != "A only" || served["gpt-b-only-live"] != "B only" {
			t.Fatalf("read order %v served %v, want the shared slug from codex-a and both own slugs", order, served)
		}
	}

	// Reading the same answers again does not change the served catalog.
	revision := GetCodexClientModelsRevision()
	for _, read := range []struct {
		id      string
		payload []byte
	}{{"codex-b", fromB}, {"codex-a", fromA}, {"codex-b", fromB}} {
		if changed, errMerge := MergeLiveCodexClientModels(read.id, read.payload); errMerge != nil || changed {
			t.Fatalf("repeated read of %s = %t, %v; want no change", read.id, changed, errMerge)
		}
	}
	if GetCodexClientModelsRevision() != revision {
		t.Fatal("repeated reads changed the served catalog")
	}

	// A credential that is no longer read takes its entries with it.
	if !RetainLiveCodexClientModels([]string{"codex-b"}) {
		t.Fatal("dropping codex-a did not change the served catalog")
	}
	served := servedCodexClientModels(t)
	if _, ok := served["gpt-a-only-live"]; ok || served["gpt-shared-live"] != "Shared from B" || served["gpt-b-only-live"] != "B only" {
		t.Fatalf("after dropping codex-a the catalog serves %v", served)
	}
	if RetainLiveCodexClientModels([]string{"codex-b"}) {
		t.Fatal("retaining the same credentials changed the served catalog")
	}

	// With no credential left the base catalog is served alone again.
	if !RetainLiveCodexClientModels(nil) {
		t.Fatal("dropping the last credential did not change the served catalog")
	}
	if !bytes.Equal(GetCodexClientModelsJSON(), base) {
		t.Fatal("the served catalog is not the base catalog once no credential is read")
	}
}

func TestLiveCodexClientModelsInstallsDoNotLoseUpdates(t *testing.T) {
	resetCodexClientLiveOverlay(t)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		id := string(rune('a' + i))
		payload := liveCodexClientPayload(t, liveCodexClientModel(t, "gpt-concurrent-"+id, "Concurrent "+id))
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, errMerge := MergeLiveCodexClientModels("codex-"+id, payload); errMerge != nil {
				t.Errorf("merge %s: %v", id, errMerge)
			}
		}()
		go func() {
			defer wg.Done()
			if _, errLoad := loadCodexClientModelsFromBytes(embeddedCodexClientModelsJSON, "remote"); errLoad != nil {
				t.Errorf("reload base catalog: %v", errLoad)
			}
		}()
	}
	wg.Wait()

	served := servedCodexClientModels(t)
	for i := 0; i < 8; i++ {
		id := string(rune('a' + i))
		if served["gpt-concurrent-"+id] != "Concurrent "+id {
			t.Fatalf("the entry of codex-%s was lost by a concurrent install: %v", id, served)
		}
	}
}
