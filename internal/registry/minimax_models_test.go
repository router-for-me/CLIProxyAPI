package registry

import "testing"

// TestMinimaxModelsRegistered verifies the MiniMax catalog is reachable through
// both the channel-specific and the global lookup paths.
func TestMinimaxModelsRegistered(t *testing.T) {
	models := GetMinimaxModels()
	if len(models) == 0 {
		t.Fatal("MiniMax models must be registered")
	}

	wantIDs := map[string]bool{
		"MiniMax-M3.1-Flash-Preview": false,
		"MiniMax-M3":                 false,
		"MiniMax-M2.7":               false,
		"MiniMax-M2.7-highspeed":     false,
	}
	for _, m := range models {
		if m == nil {
			t.Fatal("nil model entry")
		}
		if _, ok := wantIDs[m.ID]; ok {
			wantIDs[m.ID] = true
		}
		if m.Type != "minimax" {
			t.Fatalf("%s type = %q, want minimax", m.ID, m.Type)
		}
		// Image models are served outside the chat pipeline and legitimately
		// carry no context window.
		if m.ID == "image-01" {
			continue
		}
		if m.ContextLength <= 0 || m.MaxCompletionTokens <= 0 {
			t.Fatalf("%s has invalid limits: ctx=%d max=%d", m.ID, m.ContextLength, m.MaxCompletionTokens)
		}
	}
	for id, seen := range wantIDs {
		if !seen {
			t.Fatalf("expected model %s in the catalog", id)
		}
	}

	if got := GetStaticModelDefinitionsByChannel("minimax"); len(got) != len(models) {
		t.Fatalf("channel lookup returned %d models, want %d", len(got), len(models))
	}

	// The global lookup is what the executor and thinking pipeline consult.
	info := LookupStaticModelInfo("MiniMax-M3")
	if info == nil {
		t.Fatal("MiniMax-M3 must be found by the global lookup")
	}
	if info.ContextLength != 1000000 {
		t.Fatalf("MiniMax-M3 context = %d, want 1000000", info.ContextLength)
	}
	if info.Thinking == nil || len(info.Thinking.Levels) == 0 {
		t.Fatal("MiniMax-M3 must advertise thinking levels")
	}

	flash := LookupStaticModelInfo("MiniMax-M3.1-Flash-Preview")
	if flash == nil {
		t.Fatal("MiniMax-M3.1-Flash-Preview must be registered")
	}
	if flash.ContextLength != 524288 {
		t.Fatalf("flash context = %d, want 524288", flash.ContextLength)
	}
}

// TestMinimaxModelsAreCloned guards against callers mutating the shared catalog.
func TestMinimaxModelsAreCloned(t *testing.T) {
	first := GetMinimaxModels()
	if len(first) == 0 {
		t.Fatal("expected models")
	}
	first[0].ID = "mutated"
	second := GetMinimaxModels()
	if second[0].ID == "mutated" {
		t.Fatal("model definitions must be cloned per call")
	}
}

// TestMinimaxIncludesImageModel proves image-01 is present without relying on
// models.json, since it is served through the OpenAI image endpoints rather
// than the chat pipeline and must resolve for provider routing.
func TestMinimaxIncludesImageModel(t *testing.T) {
	models := GetMinimaxModels()
	var found *ModelInfo
	for _, m := range models {
		if m != nil && m.ID == "image-01" {
			found = m
		}
	}
	if found == nil {
		t.Fatal("image-01 must be registered for provider routing")
	}
	if found.Type != "minimax" {
		t.Fatalf("image-01 type = %q, want minimax", found.Type)
	}
	if found.OwnedBy != "minimax" {
		t.Fatalf("image-01 owned_by = %q", found.OwnedBy)
	}
}

// TestWithMinimaxBuiltinsUpserts ensures the built-in replaces an existing
// entry with the same ID rather than duplicating it.
func TestWithMinimaxBuiltinsUpserts(t *testing.T) {
	models := WithMinimaxBuiltins([]*ModelInfo{{ID: "image-01", DisplayName: "stale"}})
	count := 0
	for _, m := range models {
		if m != nil && m.ID == "image-01" {
			count++
			if m.DisplayName == "stale" {
				t.Fatal("the built-in definition must replace a stale entry")
			}
		}
	}
	if count != 1 {
		t.Fatalf("image-01 appears %d times, want exactly 1", count)
	}
}
