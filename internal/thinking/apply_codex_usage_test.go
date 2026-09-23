package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/codex"
	"github.com/tidwall/gjson"
)

func TestExtractCodexReasoningEffortWithConfigurationUpdate(t *testing.T) {
	tests := []struct {
		name                 string
		provider             string
		model                string
		body                 string
		wantRequestEffort    string
		wantTranslatedEffort string
	}{
		{
			name:                 "codex extracts configuration_update effort over top-level",
			provider:             "codex",
			model:                "gpt-6-astra",
			body:                 `{"model":"gpt-6-astra","reasoning":{"effort":"xhigh","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}}]}`,
			wantRequestEffort:    "low",
			wantTranslatedEffort: "low",
		},
		{
			name:                 "openai-response extracts configuration_update effort over top-level",
			provider:             "openai-response",
			model:                "gpt-6-astra",
			body:                 `{"model":"gpt-6-astra","reasoning":{"effort":"xhigh","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}}]}`,
			wantRequestEffort:    "low",
			wantTranslatedEffort: "low",
		},
		{
			name:     "codex picks latest configuration_update when multiple are present",
			provider: "codex",
			model:    "gpt-6-astra",
			body: `{
				"model":"gpt-6-astra",
				"reasoning":{"effort":"xhigh","summary":"auto"},
				"input":[
					{"type":"configuration_update","reasoning":{"effort":"low"}},
					{"role":"user","content":"second turn"},
					{"type":"configuration_update","reasoning":{"effort":"medium"}}
				]
			}`,
			wantRequestEffort:    "medium",
			wantTranslatedEffort: "medium",
		},
		{
			name:                 "codex falls back to top-level when configuration_update has no reasoning effort",
			provider:             "codex",
			model:                "gpt-6-astra",
			body:                 `{"model":"gpt-6-astra","reasoning":{"effort":"xhigh","summary":"auto"},"input":[{"type":"configuration_update","tools":[]}]}`,
			wantRequestEffort:    "xhigh",
			wantTranslatedEffort: "xhigh",
		},
		{
			name:                 "codex handles configuration_update effort none",
			provider:             "codex",
			model:                "gpt-6-astra",
			body:                 `{"model":"gpt-6-astra","reasoning":{"effort":"xhigh"},"input":[{"type":"configuration_update","reasoning":{"effort":"none"}}]}`,
			wantRequestEffort:    "none",
			wantTranslatedEffort: "none",
		},
		{
			name:                 "codex handles configuration_update effort auto",
			provider:             "codex",
			model:                "gpt-6-astra",
			body:                 `{"model":"gpt-6-astra","reasoning":{"effort":"xhigh"},"input":[{"type":"configuration_update","reasoning":{"effort":"auto"}}]}`,
			wantRequestEffort:    "auto",
			wantTranslatedEffort: "auto",
		},
		{
			name:     "trailing configuration_update without reasoning effort preserves earlier effort",
			provider: "codex",
			model:    "gpt-6-astra",
			body: `{
				"model":"gpt-6-astra",
				"reasoning":{"effort":"xhigh"},
				"input":[
					{"type":"configuration_update","reasoning":{"effort":"low"}},
					{"type":"configuration_update","tools":[]}
				]
			}`,
			wantRequestEffort:    "low",
			wantTranslatedEffort: "low",
		},
		{
			name:                 "invalid json returns empty effort",
			provider:             "codex",
			model:                "gpt-6-astra",
			body:                 `{"model":"gpt-6-astra","reasoning":{"effort":"xhigh"},"input":`,
			wantRequestEffort:    "",
			wantTranslatedEffort: "",
		},
		{
			name:                 "codex falls back to top-level when input has no configuration_update",
			provider:             "codex",
			model:                "gpt-6-astra",
			body:                 `{"model":"gpt-6-astra","reasoning":{"effort":"xhigh"},"input":[{"role":"user","content":"hello"}]}`,
			wantRequestEffort:    "xhigh",
			wantTranslatedEffort: "xhigh",
		},
		{
			name:                 "model suffix takes precedence over configuration_update for request effort",
			provider:             "codex",
			model:                "gpt-6-astra(high)",
			body:                 `{"model":"gpt-6-astra","reasoning":{"effort":"xhigh"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}}]}`,
			wantRequestEffort:    "high",
			wantTranslatedEffort: "low",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotRequest := thinking.ExtractReasoningEffort([]byte(tc.body), tc.provider, tc.model)
			if gotRequest != tc.wantRequestEffort {
				t.Fatalf("ExtractReasoningEffort() = %q, want %q", gotRequest, tc.wantRequestEffort)
			}

			gotTranslated := thinking.ExtractTranslatedReasoningEffort([]byte(tc.body), tc.provider)
			if gotTranslated != tc.wantTranslatedEffort {
				t.Fatalf("ExtractTranslatedReasoningEffort() = %q, want %q", gotTranslated, tc.wantTranslatedEffort)
			}
		})
	}
}

func TestApplyThinkingPreservesCodexTopLevelReasoningEffortBaseline(t *testing.T) {
	// Preserving request-level baseline sent upstream ensures prompt prefix cache preservation.
	body := []byte(`{"model":"gpt-6-astra","reasoning":{"effort":"xhigh","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}}]}`)
	applied, err := thinking.ApplyThinking(body, "gpt-6-astra", "codex", "codex", "codex")
	if err != nil {
		t.Fatalf("ApplyThinking() error = %v", err)
	}

	// Verify top-level request baseline is preserved for upstream prompt caching.
	if gotTopEffort := gjson.GetBytes(applied, "reasoning.effort").String(); gotTopEffort != "xhigh" {
		t.Fatalf("ApplyThinking() top-level reasoning.effort = %q, want %q", gotTopEffort, "xhigh")
	}
	if gotSummary := gjson.GetBytes(applied, "reasoning.summary").String(); gotSummary != "auto" {
		t.Fatalf("ApplyThinking() reasoning.summary = %q, want %q", gotSummary, "auto")
	}
	if gotInputEffort := gjson.GetBytes(applied, "input.0.reasoning.effort").String(); gotInputEffort != "low" {
		t.Fatalf("ApplyThinking() input[0].reasoning.effort = %q, want %q", gotInputEffort, "low")
	}

	// Verify usage reporting extracts the effective in-turn effort.
	effort := thinking.ExtractTranslatedReasoningEffort(applied, "codex")
	if effort != "low" {
		t.Fatalf("ExtractTranslatedReasoningEffort(applied) = %q, want %q", effort, "low")
	}
}
