package registry

import "testing"

func limitedModel(id string, contextLength, maxCompletionTokens int) *ModelInfo {
	return &ModelInfo{ID: id, Object: "model", OwnedBy: "test", ContextLength: contextLength, MaxCompletionTokens: maxCompletionTokens}
}

func TestSharedAliasPublishesSmallestRouteLimits(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("small", "claude", []*ModelInfo{limitedModel("team", 200_000, 64_000)})
	r.RegisterClient("large", "gemini", []*ModelInfo{limitedModel("team", 1_000_000, 65_536)})

	got := r.GetModelInfo("team", "")
	if got.ContextLength != 200_000 || got.MaxCompletionTokens != 64_000 {
		t.Fatalf("shared alias limits = %d/%d, want the smallest route's 200000/64000", got.ContextLength, got.MaxCompletionTokens)
	}
	if got := r.GetModelInfo("team", "gemini"); got.ContextLength != 1_000_000 {
		t.Fatalf("gemini route ContextLength = %d, want its own 1000000", got.ContextLength)
	}

	r.UnregisterClient("small")
	if got := r.GetModelInfo("team", ""); got.ContextLength != 1_000_000 || got.MaxCompletionTokens != 65_536 {
		t.Fatalf("after the small route left, limits = %d/%d, want 1000000/65536", got.ContextLength, got.MaxCompletionTokens)
	}
}

func TestSharedAliasLimitsFollowReRegistration(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("a", "codex", []*ModelInfo{limitedModel("team", 400_000, 128_000)})
	r.RegisterClient("b", "codex", []*ModelInfo{limitedModel("team", 272_000, 128_000)})
	if got := r.GetModelInfo("team", "codex"); got.ContextLength != 272_000 {
		t.Fatalf("per-provider ContextLength = %d, want 272000", got.ContextLength)
	}

	// A client re-registering with a larger window raises the shared limit only
	// when no other client still declares a smaller one.
	r.RegisterClient("b", "codex", []*ModelInfo{limitedModel("team", 500_000, 128_000)})
	if got := r.GetModelInfo("team", ""); got.ContextLength != 400_000 {
		t.Fatalf("after re-registration ContextLength = %d, want 400000", got.ContextLength)
	}

	// A limit no remaining client declares is cleared, not kept from a removed client.
	r.RegisterClient("c", "codex", []*ModelInfo{limitedModel("other", 0, 0)})
	r.RegisterClient("d", "codex", []*ModelInfo{limitedModel("other", 300_000, 0)})
	r.UnregisterClient("d")
	if got := r.GetModelInfo("other", ""); got.ContextLength != 0 {
		t.Fatalf("ContextLength = %d after the only declaring client left, want 0", got.ContextLength)
	}
}
