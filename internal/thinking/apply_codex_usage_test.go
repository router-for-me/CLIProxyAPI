package thinking_test

import (
	"bytes"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
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
			name:                 "source configuration_update takes precedence over suffix for request effort",
			provider:             "codex",
			model:                "gpt-6-astra(high)",
			body:                 `{"model":"gpt-6-astra","reasoning":{"effort":"xhigh"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}}]}`,
			wantRequestEffort:    "low",
			wantTranslatedEffort: "low",
		},
		{
			name:                 "suffix takes precedence over top-level without updates",
			provider:             "openai-response",
			model:                "gpt-6-astra(high)",
			body:                 `{"reasoning":{"effort":"xhigh"},"input":[{"type":"configuration_update","tools":[]}]}`,
			wantRequestEffort:    "high",
			wantTranslatedEffort: "xhigh",
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

func TestExtractCodexReasoningEffortWithConfigurationUpdateTargetRouting(t *testing.T) {
	const source = `{"reasoning":{"effort":"xhigh"},"input":[{"type":"configuration_update","reasoning":{"effort":"medium"}},{"type":"configuration_update","reasoning":{"effort":"low"}},{"type":"configuration_update","reasoning":{"effort":null}},{"role":"user","content":"ok"}]}`
	for _, supported := range []bool{true, false} {
		name := "unsupported"
		if supported {
			name = "supported"
		}
		t.Run(name, func(t *testing.T) {
			info := &registry.ModelInfo{ID: "opaque-route", Type: "codex", SupportConfigurationUpdate: supported, Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high", "xhigh"}}}
			body, err := thinking.ApplyThinkingWithModelInfoAndSummary([]byte(source), []byte(source), "opaque-route(high)", "openai-response", "codex", "codex", info, thinking.SummaryConfig{})
			if err != nil {
				t.Fatalf("ApplyThinkingWithModelInfoAndSummary() error = %v", err)
			}
			if got := thinking.ExtractReasoningEffort([]byte(source), "openai-response", "opaque-route(high)"); got != "low" {
				t.Fatalf("source effort = %q, want low", got)
			}
			want := "high"
			if supported {
				want = "low"
			}
			if got := thinking.ExtractTranslatedReasoningEffort(body, "codex"); got != want {
				t.Fatalf("final effort = %q, want %q; body=%s", got, want, body)
			}
			if got := gjson.GetBytes(body, "reasoning.effort").String(); got != "high" {
				t.Fatalf("final top-level effort = %q, want high; body=%s", got, body)
			}
			if hasUpdate := gjson.GetBytes(body, "input.0.type").String() == "configuration_update"; hasUpdate != supported {
				t.Fatalf("final update present = %t, want %t; body=%s", hasUpdate, supported, body)
			}
		})
	}
}

func TestApplyConfigurationUpdateRouting(t *testing.T) {
	const model = "configured-responses"
	tests := []struct {
		name       string
		body       string
		format     string
		suffix     string
		supported  bool
		noThinking bool
		skipEffort bool
		wantEffort string
		wantInput  string
		wantSame   bool
	}{
		{
			name:      "supported native request preserves baseline and updates byte for byte",
			body:      `{"reasoning":{"effort":"xhigh","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"ok"}]}`,
			supported: true, wantEffort: "xhigh", wantInput: `[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"ok"}]`, wantSame: true,
		},
		{
			name:      "supported no-effort request remains unchanged",
			body:      `{"reasoning":{"summary":"auto"},"input":[{"type":"configuration_update","tools":[]},{"role":"user","content":"ok"}]}`,
			supported: true, wantInput: `[{"type":"configuration_update","tools":[]},{"role":"user","content":"ok"}]`, wantSame: true,
		},
		{
			name:       "supported native request without thinking metadata preserves baseline summary and updates",
			body:       `{"reasoning":{"effort":"xhigh","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"ok"}]}`,
			supported:  true,
			noThinking: true,
			wantEffort: "xhigh",
			wantInput:  `[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"ok"}]`,
			wantSame:   true,
		},
		{
			name:       "supported native suffix still strips effort without thinking metadata",
			body:       `{"reasoning":{"effort":"xhigh","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"ok"}]}`,
			supported:  true,
			noThinking: true,
			suffix:     "high",
			wantInput:  `[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"ok"}]`,
		},
		{
			name:      "supported suffix only rewrites the top-level effort",
			body:      `{"reasoning":{"effort":"xhigh","summary":"auto","other":7},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"ok"}]}`,
			supported: true, suffix: "high", wantEffort: "high", wantInput: `[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"ok"}]`,
		},
		{
			name:      "supported suffix keeps effective input effort for current turn",
			body:      `{"reasoning":{"summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"ok"}]}`,
			supported: true, suffix: "high", wantEffort: "high", wantInput: `[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"ok"}]`,
		},
		{
			name:      "supported invalid suffix leaves native payload untouched",
			body:      `{"reasoning":{"generate_summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"ok"}]}`,
			supported: true, suffix: "invalid", wantInput: `[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"ok"}]`, wantSame: true,
		},
		{
			name:       "unsupported latest nonempty update wins and other input order stays unchanged",
			body:       `{"reasoning":{"effort":"xhigh","summary":"auto","other":7},"input":[{"role":"user","content":"first"},{"type":"configuration_update","reasoning":{"effort":"low"}},{"type":"configuration_update","reasoning":{"effort":"  "}},{"role":"assistant","content":"reply"},{"type":"configuration_update","reasoning":{"effort":"medium"}},{"type":"configuration_update","tools":[]},{"role":"user","content":"last"}]}`,
			wantEffort: "medium", wantInput: `[{"role":"user","content":"first"},{"role":"assistant","content":"reply"},{"role":"user","content":"last"}]`,
		},
		{
			name:       "unsupported ignores empty and nonstring efforts",
			body:       `{"reasoning":{"summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"type":"configuration_update","reasoning":{"effort":42}},{"type":"configuration_update","reasoning":{"effort":null}},{"type":"configuration_update","reasoning":{"effort":"  "}},{"role":"user","content":"ok"}]}`,
			wantEffort: "low", wantInput: `[{"role":"user","content":"ok"}]`,
		},
		{
			name:   "unsupported suffix takes precedence and still removes updates",
			body:   `{"reasoning":{"effort":"xhigh","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"ok"}]}`,
			suffix: "high", wantEffort: "high", wantInput: `[{"role":"user","content":"ok"}]`,
		},
		{
			name:       "unsupported without top-level reasoning promotes the last update",
			body:       `{"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"ok"}]}`,
			wantEffort: "low", wantInput: `[{"role":"user","content":"ok"}]`,
		},
		{
			name:      "unsupported removes updates with no effort without inventing one",
			body:      `{"reasoning":{"summary":"auto"},"input":[{"type":"configuration_update","tools":[]},{"role":"user","content":"ok"}]}`,
			wantInput: `[{"role":"user","content":"ok"}]`,
		},
		{
			name:       "unsupported without thinking support still removes updates",
			body:       `{"reasoning":{"summary":"auto"},"input":[{"type":"configuration_update","tools":[]},{"role":"user","content":"ok"}]}`,
			noThinking: true, wantInput: `[{"role":"user","content":"ok"}]`,
		},
		{
			name:       "unsupported without thinking support strips effort but keeps summary",
			body:       `{"reasoning":{"effort":"xhigh","summary":"auto","other":7},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"ok"}]}`,
			noThinking: true, wantInput: `[{"role":"user","content":"ok"}]`,
		},
		{
			name:   "unsupported openai-response alias removes updates",
			body:   `{"reasoning":{"effort":"xhigh","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"ok"}]}`,
			format: "openai-response", wantEffort: "low", wantInput: `[{"role":"user","content":"ok"}]`,
		},
		{
			name:     "invalid JSON is untouched",
			body:     `{"reasoning":{"effort":"xhigh"},"input":[`,
			wantSame: true, skipEffort: true,
		},
		{
			name:      "nonarray input is untouched",
			body:      `{"reasoning":{"summary":"auto"},"input":{"type":"configuration_update","reasoning":{"effort":"low"}}}`,
			wantInput: `{"type":"configuration_update","reasoning":{"effort":"low"}}`, wantSame: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			format := tc.format
			if format == "" {
				format = "codex"
			}
			thinkingSupport := &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh"}}
			if tc.noThinking {
				thinkingSupport = nil
			}
			info := &registry.ModelInfo{ID: model, Type: "codex", SupportConfigurationUpdate: tc.supported, Thinking: thinkingSupport}
			body := []byte(tc.body)
			applied, err := thinking.ApplyThinkingWithModelInfo(body, body, model+suffixForTest(tc.suffix), format, format, "codex", info)
			if err != nil {
				t.Fatalf("ApplyThinkingWithModelInfo() error = %v", err)
			}
			if tc.wantSame && !bytes.Equal(applied, body) {
				t.Fatalf("body changed: got %s, want %s", applied, body)
			}
			if !tc.skipEffort {
				if got := gjson.GetBytes(applied, "reasoning.effort").String(); got != tc.wantEffort {
					t.Errorf("reasoning.effort = %q, want %q; body=%s", got, tc.wantEffort, applied)
				}
			}
			if tc.wantInput != "" && gjson.GetBytes(applied, "input").Raw != tc.wantInput {
				t.Errorf("input = %s, want %s; body=%s", gjson.GetBytes(applied, "input").Raw, tc.wantInput, applied)
			}
			if gjson.GetBytes(body, "reasoning.summary").Exists() {
				if got := gjson.GetBytes(applied, "reasoning.summary").String(); got != "auto" {
					t.Errorf("reasoning.summary = %q, want auto; body=%s", got, applied)
				}
			}
			if gjson.GetBytes(body, "reasoning.other").Exists() && gjson.GetBytes(applied, "reasoning.other").Int() != 7 {
				t.Errorf("reasoning.other changed: %s", applied)
			}
			if tc.supported && tc.suffix != "" {
				if got := thinking.ExtractTranslatedReasoningEffort(applied, format); got != "low" {
					t.Errorf("effective effort = %q, want low; body=%s", got, applied)
				}
			}
		})
	}
}

func suffixForTest(suffix string) string {
	if suffix == "" {
		return ""
	}
	return "(" + suffix + ")"
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
