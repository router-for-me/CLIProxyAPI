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
