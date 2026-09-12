package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/codex"
	codexclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/claude"
	"github.com/tidwall/gjson"
)

func TestApplyThinkingCompatibilityWrapper(t *testing.T) {
	t.Parallel()

	body := []byte(`{"reasoning":{"effort":"max"}}`)
	legacy, legacyErr := thinking.ApplyThinking(body, "gpt-5.5", "claude", "codex", "codex")
	withOptions, optionsErr := thinking.ApplyThinkingWithOptions(body, "gpt-5.5", "claude", "codex", "codex", thinking.ApplyOptions{})
	if (legacyErr == nil) != (optionsErr == nil) || (legacyErr != nil && legacyErr.Error() != optionsErr.Error()) {
		t.Fatalf("errors differ: ApplyThinking=%v ApplyThinkingWithOptions=%v", legacyErr, optionsErr)
	}
	if string(legacy) != string(withOptions) {
		t.Fatalf("outputs differ: ApplyThinking=%s ApplyThinkingWithOptions=%s", legacy, withOptions)
	}
}

func TestApplyThinkingWithOptionsUsesTranslatedClaudeEffort(t *testing.T) {
	t.Parallel()

	claudeBody := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`)
	translated := codexclaude.ConvertClaudeRequestToCodex("gpt-5.5", claudeBody, false)
	if got := gjson.GetBytes(translated, "reasoning.effort").String(); got != "max" {
		t.Fatalf("translated reasoning.effort = %q, want max; body = %s", got, translated)
	}

	withoutRules, err := thinking.ApplyThinkingWithOptions(translated, "gpt-5.5", "claude", "codex", "codex", thinking.ApplyOptions{})
	if err != nil {
		t.Fatalf("ApplyThinkingWithOptions() without rules error = %v", err)
	}
	if got := gjson.GetBytes(withoutRules, "reasoning.effort").String(); got != "xhigh" {
		t.Fatalf("reasoning.effort without rules = %q, want legacy xhigh; body = %s", got, withoutRules)
	}

	withRule, err := thinking.ApplyThinkingWithOptions(translated, "gpt-5.5", "claude", "codex", "codex", thinking.ApplyOptions{
		EffortMapping: []thinking.EffortMappingRule{{From: "max", To: "ultra"}},
	})
	if err != nil {
		t.Fatalf("ApplyThinkingWithOptions() with rule error = %v", err)
	}
	if got := gjson.GetBytes(withRule, "reasoning.effort").String(); got != "ultra" {
		t.Fatalf("reasoning.effort with rule = %q, want ultra; body = %s", got, withRule)
	}
}

func TestApplyThinkingWithOptionsForcedEffort(t *testing.T) {
	t.Parallel()

	rules := []thinking.EffortMappingRule{{From: "max", To: "ultra"}}
	tests := []struct {
		name    string
		model   string
		body    string
		from    string
		to      string
		options thinking.ApplyOptions
		want    string
		wantErr bool
	}{
		{
			name:    "gpt 5.5 max maps to arbitrary ultra",
			model:   "gpt-5.5",
			body:    `{"reasoning":{"effort":"max"}}`,
			from:    "claude",
			to:      "codex",
			options: thinking.ApplyOptions{EffortMapping: rules},
			want:    "ultra",
		},
		{
			name:  "gpt 5.5 max without mapping keeps legacy clamp",
			model: "gpt-5.5",
			body:  `{"reasoning":{"effort":"max"}}`,
			from:  "claude",
			to:    "codex",
			want:  "xhigh",
		},
		{
			name:  "gpt 5.6 max without mapping stays max",
			model: "gpt-5.6-preview",
			body:  `{"reasoning":{"effort":"max"}}`,
			from:  "codex",
			to:    "codex",
			want:  "max",
		},
		{
			name:    "claude target mismatch does not map",
			model:   "gpt-5.5",
			body:    `{"reasoning":{"effort":"max"}}`,
			from:    "claude",
			to:      "codex",
			options: thinking.ApplyOptions{EffortMapping: []thinking.EffortMappingRule{{From: "max", To: "ultra", TargetProtocol: "claude"}}},
			want:    "xhigh",
		},
		{
			name:    "invalid ultra suffix remains invalid",
			model:   "gpt-5.5(ultra)",
			body:    `{}`,
			from:    "codex",
			to:      "codex",
			options: thinking.ApplyOptions{EffortMapping: []thinking.EffortMappingRule{{From: "ultra", To: "low"}}},
			want:    "",
		},
		{
			name:    "known model without thinking is not force enabled",
			model:   "grok-build-0.1",
			body:    `{"reasoning":{"effort":"high"}}`,
			from:    "xai",
			to:      "codex",
			options: thinking.ApplyOptions{EffortMapping: []thinking.EffortMappingRule{{From: "high", To: "ultra"}}},
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := thinking.ApplyThinkingWithOptions([]byte(tt.body), tt.model, tt.from, tt.to, tt.to, tt.options)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ApplyThinkingWithOptions() error = nil, want error; body = %s", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ApplyThinkingWithOptions() error = %v", err)
			}
			if effort := gjson.GetBytes(got, "reasoning.effort").String(); effort != tt.want {
				t.Fatalf("reasoning.effort = %q, want %q; body = %s", effort, tt.want, got)
			}
		})
	}
}
