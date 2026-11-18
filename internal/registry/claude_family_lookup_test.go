package registry

import "testing"

func TestLookupStaticModelInfoClaudeVariantsUseCatalogEntry(t *testing.T) {
	tests := []struct {
		id     string
		family string
	}{
		{id: "claude-opus-5-5[1m]", family: "claude-opus-5-5"},
		{id: "claude-opus-5-5-20261001", family: "claude-opus-5-5"},
		{id: "claude-opus-5-5-20261001[1m]", family: "claude-opus-5-5"},
		{id: "claude-sonnet-5-5[1m]", family: "claude-sonnet-5-5"},
		{id: "claude-sonnet-5-5-20261001", family: "claude-sonnet-5-5"},
		{id: "claude-fable-5[1m]", family: "claude-fable-5"},
		{id: "claude-fable-5-1[1m]", family: "claude-fable-5-1"},
		{id: "claude-haiku-4-5-20251001[1m]", family: "claude-haiku-4-5-20251001"},
	}
	for _, tt := range tests {
		info := LookupStaticModelInfo(tt.id)
		if info == nil || info.ID != tt.family {
			t.Errorf("LookupStaticModelInfo(%q) = %+v, want catalog entry %q", tt.id, info, tt.family)
		}
		chInfo := LookupStaticModelInfoByChannel(tt.id, "claude")
		if chInfo == nil || chInfo.ID != tt.family {
			t.Errorf("LookupStaticModelInfoByChannel(%q, \"claude\") = %+v, want catalog entry %q", tt.id, chInfo, tt.family)
		}
	}

	// Exact catalog IDs keep winning, and only Claude variants are normalized.
	if info := LookupStaticModelInfo("claude-haiku-4-5-20251001"); info == nil || info.ID != "claude-haiku-4-5-20251001" {
		t.Errorf("exact dated ID resolved to %+v", info)
	}
	for _, id := range []string{"claude-unknown-20261001", "claude-opus-5-5-2026", "claude-opus-5-5[1m", "gpt-5[1m]"} {
		if info := LookupStaticModelInfo(id); info != nil {
			t.Errorf("LookupStaticModelInfo(%q) = %q, want nil", id, info.ID)
		}
	}
}

func TestClaudeCatalogIncludesMythosModels(t *testing.T) {
	want := map[string]bool{"claude-mythos-5-1": false, "claude-mythos-5-2": false}
	for _, m := range GetClaudeModels() {
		if _, ok := want[m.ID]; !ok {
			continue
		}
		want[m.ID] = true
		if m.ContextLength != 1000000 || m.MaxCompletionTokens != 128000 {
			t.Errorf("%s: context=%d max_completion=%d", m.ID, m.ContextLength, m.MaxCompletionTokens)
		}
		if m.Thinking == nil || m.Thinking.ZeroAllowed || len(m.Thinking.Levels) != 5 {
			t.Errorf("%s: unexpected thinking support %+v", m.ID, m.Thinking)
		}
	}
	for id, found := range want {
		if !found {
			t.Errorf("expected %s in the Claude catalog", id)
		}
	}
	if LookupStaticModelInfo("claude-mythos-5-2[1m]") == nil {
		t.Error("expected claude-mythos-5-2[1m] to resolve to its catalog entry")
	}
}
