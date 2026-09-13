package registry

import "testing"

func TestApplyClientModelCapabilitiesPublishesLimitsAndInvalidatesCache(t *testing.T) {
	r := newTestModelRegistry()
	const id = "personal-ag/alias"
	r.RegisterClient("personal", "antigravity", []*ModelInfo{{ID: id, ContextLength: 200000, MaxCompletionTokens: 32000}})
	r.RegisterClient("work", "antigravity", []*ModelInfo{{ID: "work-ag/alias", ContextLength: 100000, MaxCompletionTokens: 8000}})
	r.GetAvailableModels("openai")
	epoch := r.ClientRegistrationEpoch("personal")
	if !r.ApplyClientModelCapabilities("personal", epoch, func(_ string, info *ModelInfo) {
		info.ContextLength = 250000
		info.MaxCompletionTokens = 64000
	}) {
		t.Fatal("update rejected")
	}
	views := append(r.GetModelsForClient("personal"), r.GetModelInfo(id, ""), r.GetModelInfo(id, "antigravity"))
	for _, info := range r.GetAvailableModelInfos() {
		if info.ID == id {
			views = append(views, info)
		}
	}
	for _, info := range views {
		if info == nil || info.ContextLength != 250000 || info.MaxCompletionTokens != 64000 {
			t.Fatalf("stale discovery view: %+v", info)
		}
	}
	found := false
	for _, info := range r.GetAvailableModels("openai") {
		if info["id"] == id {
			found = true
			if info["context_length"] != 250000 || info["max_completion_tokens"] != 64000 {
				t.Fatalf("stale cached model: %+v", info)
			}
		}
	}
	if !found {
		t.Fatal("updated model absent from discovery")
	}
	work := r.GetModelInfo("work-ag/alias", "")
	if work.ContextLength != 100000 || work.MaxCompletionTokens != 8000 {
		t.Fatalf("other account changed: %+v", work)
	}
	r.RegisterClient("personal", "antigravity", []*ModelInfo{{ID: id, ContextLength: 300000, MaxCompletionTokens: 128000}})
	if r.ApplyClientModelCapabilities("personal", epoch, func(_ string, info *ModelInfo) { info.ContextLength = 1 }) {
		t.Fatal("stale epoch accepted")
	}
	if r.GetModelInfo(id, "").ContextLength != 300000 {
		t.Fatal("stale probe replaced fresh limits")
	}
}

func TestApplyClientModelCapabilitiesUsesExactPoolLimits(t *testing.T) {
	r := newTestModelRegistry()
	const id = "shared"
	r.RegisterClient("ag-a", "antigravity", []*ModelInfo{{ID: id, ContextLength: 200000, MaxCompletionTokens: 32000}})
	r.RegisterClient("ag-b", "antigravity", []*ModelInfo{{ID: id, ContextLength: 150000, MaxCompletionTokens: 16000}})
	r.RegisterClient("claude", "claude", []*ModelInfo{{ID: id, ContextLength: 100000, MaxCompletionTokens: 8000}})
	r.GetAvailableModels("openai")
	apply := func(client string, context, output int) {
		t.Helper()
		if !r.ApplyClientModelCapabilities(client, r.ClientRegistrationEpoch(client), func(_ string, info *ModelInfo) {
			info.ContextLength = context
			info.MaxCompletionTokens = output
		}) {
			t.Fatal("update rejected")
		}
	}
	assertLimits := func(provider string, context, output int) {
		t.Helper()
		info := r.GetModelInfo(id, provider)
		if info.ContextLength != context || info.MaxCompletionTokens != output {
			t.Fatalf("%s limits = %d/%d, want %d/%d", provider, info.ContextLength, info.MaxCompletionTokens, context, output)
		}
	}
	apply("ag-a", 250000, 64000)
	assertLimits("", 100000, 8000)
	assertLimits("antigravity", 150000, 16000)
	assertLimits("claude", 100000, 8000)
	for _, entry := range r.GetAvailableModels("openai") {
		if entry["id"] == id && (entry["context_length"] != 100000 || entry["max_completion_tokens"] != 8000) {
			t.Fatalf("cached discovery overclaims shared limits: %#v", entry)
		}
	}
	if info := r.GetModelsForClient("ag-a")[0]; info.ContextLength != 250000 || info.MaxCompletionTokens != 64000 {
		t.Fatal("pool aggregation overwrote the account's actual limits")
	}
	apply("ag-b", 300000, 128000)
	assertLimits("", 100000, 8000)
	assertLimits("antigravity", 250000, 64000)
	apply("ag-a", 50000, 4000)
	assertLimits("", 50000, 4000)
	assertLimits("antigravity", 50000, 4000)
	assertLimits("claude", 100000, 8000)
}

func TestApplyClientModelCapabilitiesKeepsUnknownPoolLimitsUnknown(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("a", "antigravity", []*ModelInfo{{ID: "shared", ContextLength: 200000, MaxCompletionTokens: 32000}})
	r.RegisterClient("b", "antigravity", []*ModelInfo{{ID: "shared", MaxCompletionTokens: 8000}})
	r.ApplyClientModelCapabilities("a", r.ClientRegistrationEpoch("a"), func(_ string, info *ModelInfo) {
		info.ContextLength = 250000
		info.MaxCompletionTokens = 64000
	})
	for _, provider := range []string{"", "antigravity"} {
		info := r.GetModelInfo("shared", provider)
		if info.ContextLength != 0 || info.MaxCompletionTokens != 8000 {
			t.Fatalf("unknown context or known output lost: %+v", info)
		}
	}
}
