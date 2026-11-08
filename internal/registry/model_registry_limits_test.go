package registry

import (
	"fmt"
	"testing"
)

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

func TestSharedAliasNormalizesLimitFieldsAcrossRoutes(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("gemini", "gemini", []*ModelInfo{{ID: "team", Object: "model", InputTokenLimit: 1_048_576, OutputTokenLimit: 65_536}})
	if got := r.GetModelInfo("team", ""); got.InputTokenLimit != 1_048_576 || got.ContextLength != 0 {
		t.Fatalf("single route = input %d / context %d, want its own 1048576 / 0", got.InputTokenLimit, got.ContextLength)
	}

	r.RegisterClient("claude", "claude", []*ModelInfo{limitedModel("team", 200_000, 64_000)})
	got := r.GetModelInfo("team", "")
	if got.InputTokenLimit != 200_000 || got.ContextLength != 200_000 {
		t.Fatalf("input/context = %d/%d, want 200000 in both", got.InputTokenLimit, got.ContextLength)
	}
	if got.OutputTokenLimit != 64_000 || got.MaxCompletionTokens != 64_000 {
		t.Fatalf("output limits = %d/%d, want 64000 in both", got.OutputTokenLimit, got.MaxCompletionTokens)
	}
	if got := r.GetModelInfo("team", "gemini"); got.InputTokenLimit != 1_048_576 || got.ContextLength != 0 {
		t.Fatalf("gemini route = input %d / context %d, want its own 1048576 / 0", got.InputTokenLimit, got.ContextLength)
	}
}

func TestSharedAliasReconcilesMaxContextOverrides(t *testing.T) {
	r := newTestModelRegistry()
	large := limitedModel("team", 1_000_000, 128_000)
	large.MaxContextLength = 1_000_000
	r.RegisterClient("small", "codex", []*ModelInfo{limitedModel("team", 200_000, 128_000)})
	r.RegisterClient("large", "claude", []*ModelInfo{large})
	got := r.GetModelInfo("team", "")
	if got.MaxContextLength != 200_000 || got.ContextLength != 200_000 {
		t.Fatalf("max/context = %d/%d, want 200000 in both (an override must not exceed the smaller route)", got.MaxContextLength, got.ContextLength)
	}
}

func TestRouteLimitCountsFollowClients(t *testing.T) {
	r := newTestModelRegistry()
	const clients = 2000
	for i := 0; i < clients; i++ {
		r.RegisterClient(fmt.Sprintf("c%d", i), "codex", []*ModelInfo{limitedModel("shared", 272_000, 128_000), limitedModel(fmt.Sprintf("own%d", i), 400_000, 128_000)})
	}
	if n := r.modelRouteLimits["shared"][routeLimits{contextLength: 272_000, maxCompletionTokens: 128_000}]; n != clients {
		t.Fatalf("shared model counted %d clients, want %d", n, clients)
	}
	for i := 0; i < clients; i++ {
		r.UnregisterClient(fmt.Sprintf("c%d", i))
	}
	if len(r.modelRouteLimits) != 0 || len(r.providerRouteLimits) != 0 || len(r.clientRouteLimits) != 0 {
		t.Fatalf("counts left after every client left: %d/%d/%d", len(r.modelRouteLimits), len(r.providerRouteLimits), len(r.clientRouteLimits))
	}
}
