package registry

import "testing"

func TestMatchModelPattern(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		value   string
		want    bool
	}{
		{"exact match without wildcard", "gpt-5.6-luna", "gpt-5.6-luna", true},
		{"exact mismatch without wildcard", "gpt-5.6-luna", "gpt-5.6-sol", false},
		{"exact match is case insensitive", "GPT-5.6-Luna", "gpt-5.6-luna", true},
		{"trailing wildcard", "claude-haiku-4-5-*", "claude-haiku-4-5-20251001", true},
		{"trailing wildcard is case insensitive", "Claude-Haiku-4-5-*", "CLAUDE-HAIKU-4-5-20251001", true},
		{"trailing wildcard requires the prefix", "claude-haiku-4-5-*", "claude-haiku-4-5", false},
		{"trailing wildcard matches empty remainder", "claude-haiku-4-5*", "claude-haiku-4-5", true},
		{"leading wildcard", "*-20251001", "claude-haiku-4-5-20251001", true},
		{"middle wildcard", "claude-*-20251001", "claude-haiku-4-5-20251001", true},
		{"middle wildcard mismatch", "claude-*-20260101", "claude-haiku-4-5-20251001", false},
		{"bare wildcard matches anything", "*", "anything", true},
		{"unrelated model", "claude-haiku-4-5-*", "gpt-5.6-sol", false},
		{"empty pattern never matches", "", "gpt-5.6-sol", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := MatchModelPattern(tc.pattern, tc.value); got != tc.want {
				t.Errorf("MatchModelPattern(%q, %q) = %v, want %v", tc.pattern, tc.value, got, tc.want)
			}
		})
	}
}

func TestSetModelAliasPatterns_DropsEntriesWithoutWildcard(t *testing.T) {
	t.Parallel()

	r := newTestModelRegistry()
	r.SetModelAliasPatterns([]ModelAliasPattern{
		{Pattern: "claude-haiku-4-5-*", Target: "gpt-5.6-luna"},
		{Pattern: "claude-opus-5", Target: "gpt-5.6-sol"},
		{Pattern: "", Target: "gpt-5.6-sol"},
		{Pattern: "claude-*", Target: ""},
	})

	if len(r.modelAliasPatterns) != 1 {
		t.Fatalf("modelAliasPatterns length = %d, want 1", len(r.modelAliasPatterns))
	}
	if r.modelAliasPatterns[0].Target != "gpt-5.6-luna" {
		t.Errorf("kept pattern target = %q, want %q", r.modelAliasPatterns[0].Target, "gpt-5.6-luna")
	}
}

func TestGetModelProviders_FallsBackToAliasPattern(t *testing.T) {
	t.Parallel()

	r := newTestModelRegistry()
	r.RegisterClient("client-1", "codex", []*ModelInfo{{ID: "gpt-5.6-luna"}})

	// Without a pattern the dated id is unroutable, which is the reported failure.
	if providers := r.GetModelProviders("claude-haiku-4-5-20251001"); len(providers) != 0 {
		t.Fatalf("providers before pattern = %v, want none", providers)
	}

	r.SetModelAliasPatterns([]ModelAliasPattern{
		{Pattern: "claude-haiku-4-5-*", Target: "gpt-5.6-luna"},
	})

	providers := r.GetModelProviders("claude-haiku-4-5-20251001")
	if len(providers) != 1 || providers[0] != "codex" {
		t.Fatalf("providers after pattern = %v, want [codex]", providers)
	}
}

func TestGetModelProviders_AliasPatternDoesNotShadowRegisteredModel(t *testing.T) {
	t.Parallel()

	r := newTestModelRegistry()
	r.RegisterClient("client-1", "codex", []*ModelInfo{{ID: "gpt-5.6-luna"}})
	r.RegisterClient("client-2", "claude", []*ModelInfo{{ID: "claude-haiku-4-5-20251001"}})
	r.SetModelAliasPatterns([]ModelAliasPattern{
		{Pattern: "claude-haiku-4-5-*", Target: "gpt-5.6-luna"},
	})

	providers := r.GetModelProviders("claude-haiku-4-5-20251001")
	if len(providers) != 1 || providers[0] != "claude" {
		t.Fatalf("providers = %v, want [claude] because the exact model is registered", providers)
	}
}

func TestGetModelProviders_AliasPatternWithUnregisteredTarget(t *testing.T) {
	t.Parallel()

	r := newTestModelRegistry()
	r.SetModelAliasPatterns([]ModelAliasPattern{
		{Pattern: "claude-haiku-4-5-*", Target: "gpt-5.6-luna"},
	})

	if providers := r.GetModelProviders("claude-haiku-4-5-20251001"); len(providers) != 0 {
		t.Fatalf("providers = %v, want none when the target is not registered", providers)
	}
}

func TestGetModelProviders_AliasPatternTargetIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	r := newTestModelRegistry()
	r.RegisterClient("client-1", "codex", []*ModelInfo{{ID: "gpt-5.6-luna"}})
	// The configured target casing does not have to match the registered model id,
	// which is how exact aliases already behave.
	r.SetModelAliasPatterns([]ModelAliasPattern{
		{Pattern: "claude-haiku-4-5-*", Target: "GPT-5.6-LUNA"},
	})

	providers := r.GetModelProviders("claude-haiku-4-5-20251001")
	if len(providers) != 1 || providers[0] != "codex" {
		t.Fatalf("providers = %v, want [codex]", providers)
	}
}

func TestGetModelProviders_RegisteredModelIsNotRedirectedByPattern(t *testing.T) {
	t.Parallel()

	r := newTestModelRegistry()
	r.RegisterClient("client-1", "codex", []*ModelInfo{{ID: "gpt-5.6-luna"}})
	// A registered model whose providers have all gone away must report nothing
	// rather than being rerouted to an unrelated wildcard target.
	r.models["claude-haiku-4-5-20251001"] = &ModelRegistration{Providers: map[string]int{}}
	r.SetModelAliasPatterns([]ModelAliasPattern{
		{Pattern: "claude-haiku-4-5-*", Target: "gpt-5.6-luna"},
	})

	if providers := r.GetModelProviders("claude-haiku-4-5-20251001"); len(providers) != 0 {
		t.Fatalf("providers = %v, want none for a registered model with no providers", providers)
	}
}
