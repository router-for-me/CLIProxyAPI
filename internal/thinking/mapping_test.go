package thinking

import "testing"

func TestSelectEffortMapping(t *testing.T) {
	t.Parallel()

	baseContext := effortMappingContext{
		SourceProtocol: "openai",
		TargetProtocol: "codex",
		TargetProvider: "codex",
		ResolvedModel:  "gpt-5.5",
		RequestedModel: "coding-alias",
	}

	tests := []struct {
		name    string
		rules   []EffortMappingRule
		effort  string
		context effortMappingContext
		want    ThinkingConfig
		matched bool
	}{
		{
			name: "first complete match wins",
			rules: []EffortMappingRule{
				{From: "max", To: "ultra"},
				{From: "max", To: "low"},
			},
			effort:  "max",
			context: baseContext,
			want:    ThinkingConfig{Mode: ModeLevel, Level: "ultra"},
			matched: true,
		},
		{
			name: "non cascading",
			rules: []EffortMappingRule{
				{From: "max", To: "ultra"},
				{From: "ultra", To: "low"},
			},
			effort:  "max",
			context: baseContext,
			want:    ThinkingConfig{Mode: ModeLevel, Level: "ultra"},
			matched: true,
		},
		{
			name:    "no rules",
			effort:  "max",
			context: baseContext,
		},
		{
			name: "source mismatch",
			rules: []EffortMappingRule{
				{From: "max", To: "ultra", SourceProtocol: "claude"},
			},
			effort:  "max",
			context: baseContext,
		},
		{
			name: "target protocol mismatch",
			rules: []EffortMappingRule{
				{From: "max", To: "ultra", TargetProtocol: "claude"},
			},
			effort:  "max",
			context: baseContext,
		},
		{
			name: "provider mismatch",
			rules: []EffortMappingRule{
				{From: "max", To: "ultra", TargetProvider: "openrouter"},
			},
			effort:  "max",
			context: baseContext,
		},
		{
			name: "all scopes match case insensitively",
			rules: []EffortMappingRule{
				{From: "MAX", To: "ULTRA", SourceProtocol: "OPENAI", TargetProtocol: "CODEX", TargetProvider: "CODEX"},
			},
			effort:  "max",
			context: baseContext,
			want:    ThinkingConfig{Mode: ModeLevel, Level: "ultra"},
			matched: true,
		},
		{
			name: "resolved exact model",
			rules: []EffortMappingRule{
				{From: "max", To: "ultra", Models: []string{"GPT-5.5"}},
			},
			effort:  "max",
			context: baseContext,
			want:    ThinkingConfig{Mode: ModeLevel, Level: "ultra"},
			matched: true,
		},
		{
			name: "resolved wildcard model",
			rules: []EffortMappingRule{
				{From: "max", To: "ultra", Models: []string{"gpt-*"}},
			},
			effort:  "max",
			context: baseContext,
			want:    ThinkingConfig{Mode: ModeLevel, Level: "ultra"},
			matched: true,
		},
		{
			name: "requested alias model",
			rules: []EffortMappingRule{
				{From: "max", To: "ultra", Models: []string{"coding-*"}},
			},
			effort:  "max",
			context: baseContext,
			want:    ThinkingConfig{Mode: ModeLevel, Level: "ultra"},
			matched: true,
		},
		{
			name: "model suffix stripped",
			rules: []EffortMappingRule{
				{From: "max", To: "ultra", Models: []string{"gpt-5.5"}},
			},
			effort: "max",
			context: effortMappingContext{
				ResolvedModel: "gpt-5.5(max)",
			},
			want:    ThinkingConfig{Mode: ModeLevel, Level: "ultra"},
			matched: true,
		},
		{
			name: "model mismatch",
			rules: []EffortMappingRule{
				{From: "max", To: "ultra", Models: []string{"claude-*"}},
			},
			effort:  "max",
			context: baseContext,
		},
		{
			name: "none destination",
			rules: []EffortMappingRule{
				{From: "max", To: "none"},
			},
			effort:  "max",
			context: baseContext,
			want:    ThinkingConfig{Mode: ModeNone},
			matched: true,
		},
		{
			name: "auto destination",
			rules: []EffortMappingRule{
				{From: "max", To: "auto"},
			},
			effort:  "max",
			context: baseContext,
			want:    ThinkingConfig{Mode: ModeAuto, Budget: -1},
			matched: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, matched := selectEffortMapping(tt.rules, tt.effort, tt.context)
			if matched != tt.matched {
				t.Fatalf("selectEffortMapping() matched = %v, want %v", matched, tt.matched)
			}
			if got != tt.want {
				t.Fatalf("selectEffortMapping() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestCanonicalNamedEffort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config ThinkingConfig
		want   string
		ok     bool
	}{
		{name: "level", config: ThinkingConfig{Mode: ModeLevel, Level: LevelMax}, want: "max", ok: true},
		{name: "budget", config: ThinkingConfig{Mode: ModeBudget, Budget: 8192}, want: "medium", ok: true},
		{name: "none", config: ThinkingConfig{Mode: ModeNone}, want: "none", ok: true},
		{name: "auto", config: ThinkingConfig{Mode: ModeAuto, Budget: -1}, want: "auto", ok: true},
		{name: "arbitrary level is not canonical input", config: ThinkingConfig{Mode: ModeLevel, Level: "ultra"}},
		{name: "invalid budget", config: ThinkingConfig{Mode: ModeBudget, Budget: -2}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := canonicalNamedEffort(tt.config)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("canonicalNamedEffort(%#v) = (%q, %v), want (%q, %v)", tt.config, got, ok, tt.want, tt.ok)
			}
		})
	}
}
