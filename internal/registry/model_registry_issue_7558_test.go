package registry

import "testing"

func TestGetModelInfo_StrictProviderIsolation(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("plugin-client", "other-provider", []*ModelInfo{{
		ID:          "gemini-4-hydrogen",
		Type:        "gemini",
		UserDefined: true,
	}})

	if got := r.GetModelInfo("gemini-4-hydrogen", "vertex"); got != nil {
		t.Fatalf("GetModelInfo(\"gemini-4-hydrogen\", \"vertex\") = %+v, want nil when vertex has no registration", got)
	}
	if got := r.GetModelInfo("gemini-4-hydrogen", "other-provider"); got == nil || !got.UserDefined {
		t.Fatalf("GetModelInfo(\"gemini-4-hydrogen\", \"other-provider\") = %+v, want UserDefined=true", got)
	}
	if got := r.GetModelInfo("gemini-4-hydrogen", ""); got == nil || !got.UserDefined {
		t.Fatalf("GetModelInfo(\"gemini-4-hydrogen\", \"\") = %+v, want global UserDefined=true", got)
	}
}

func TestLookupModelInfo_AliasedProviderFallsBackToChannelStaticCatalog(t *testing.T) {
	reg := GetGlobalRegistry()
	vertexClientID := "test-issue7558-vertex-aliased"
	shadowClientID := "test-issue7558-other-shadow"

	reg.RegisterClient(vertexClientID, "vertex", []*ModelInfo{{
		ID:          "vertex/gemini-4-hydrogen",
		Type:        "gemini",
		UserDefined: false,
		Thinking:    &ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh"}},
	}})
	reg.RegisterClient(shadowClientID, "other-provider", []*ModelInfo{{
		ID:          "gemini-4-hydrogen",
		Type:        "gemini",
		UserDefined: true,
	}})
	t.Cleanup(func() {
		reg.UnregisterClient(vertexClientID)
		reg.UnregisterClient(shadowClientID)
	})

	vertexInfo := LookupModelInfo("gemini-4-hydrogen", "vertex")
	if vertexInfo == nil {
		t.Fatal("LookupModelInfo(\"gemini-4-hydrogen\", \"vertex\") returned nil")
	}
	if vertexInfo.UserDefined {
		t.Fatalf("LookupModelInfo(\"gemini-4-hydrogen\", \"vertex\").UserDefined = true, want false (got cross-provider shadow: %+v)", vertexInfo)
	}
	if vertexInfo.Thinking == nil || vertexInfo.Thinking.Min != 0 || vertexInfo.Thinking.Max != 0 {
		t.Fatalf("expected Vertex level-only static ThinkingSupport, got %+v", vertexInfo.Thinking)
	}
	foundXHigh := false
	for _, lvl := range vertexInfo.Thinking.Levels {
		if lvl == "xhigh" {
			foundXHigh = true
			break
		}
	}
	if !foundXHigh {
		t.Fatalf("expected Vertex gemini-4-hydrogen levels to include xhigh, got %v", vertexInfo.Thinking.Levels)
	}

	aistudioInfo := LookupModelInfo("gemini-4-hydrogen", "aistudio")
	if aistudioInfo == nil {
		t.Fatal("LookupModelInfo(\"gemini-4-hydrogen\", \"aistudio\") returned nil")
	}
	if aistudioInfo.UserDefined {
		t.Fatalf("LookupModelInfo(\"gemini-4-hydrogen\", \"aistudio\").UserDefined = true, want false")
	}
	if aistudioInfo.Thinking == nil || aistudioInfo.Thinking.Min != 128 || aistudioInfo.Thinking.Max != 65535 {
		t.Fatalf("expected AI Studio hybrid static ThinkingSupport [128,65535], got %+v", aistudioInfo.Thinking)
	}
}
