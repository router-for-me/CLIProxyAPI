package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/claude"
	"github.com/tidwall/gjson"
)

// claude-sonnet-5-5 rejects thinking.type="disabled" and requires "between_tools"
// instead. That type accepts no extra thinking fields (display, budget_tokens,
// block_binding) and only effort high or below, so turning thinking off must
// emit {"type":"between_tools"} and remove output_config.effort.
func TestApplyThinking_ClaudeSonnet55DisabledUsesBetweenTools(t *testing.T) {
	const msgs = `"messages":[{"role":"user","content":"hi"}]`
	tests := []struct {
		name  string
		body  string
		model string
	}{
		{
			name:  "disabled",
			body:  `{"model":"claude-sonnet-5-5",` + msgs + `,"thinking":{"type":"disabled"}}`,
			model: "claude-sonnet-5-5",
		},
		{
			name:  "disabled with stale budget display block_binding and effort",
			body:  `{"model":"claude-sonnet-5-5",` + msgs + `,"thinking":{"type":"disabled","budget_tokens":8000,"display":"omitted","block_binding":{"prefix_mismatch_behavior":"drop_block"}},"output_config":{"effort":"xhigh"}}`,
			model: "claude-sonnet-5-5",
		},
		{
			name:  "suffix none overrides active thinking with display and block_binding",
			body:  `{"model":"claude-sonnet-5-5",` + msgs + `,"thinking":{"type":"adaptive","display":"summarized","block_binding":{"prefix_mismatch_behavior":"drop_block"}},"output_config":{"effort":"max"}}`,
			model: "claude-sonnet-5-5(none)",
		},
		{
			name:  "suffix zero",
			body:  `{"model":"claude-sonnet-5-5",` + msgs + `,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`,
			model: "claude-sonnet-5-5(0)",
		},
		{
			name:  "zero budget with display and block_binding",
			body:  `{"model":"claude-sonnet-5-5",` + msgs + `,"thinking":{"type":"enabled","budget_tokens":0,"display":"summarized","block_binding":{"prefix_mismatch_behavior":"drop_block"}}}`,
			model: "claude-sonnet-5-5",
		},
		{
			name:  "context variant",
			body:  `{"model":"claude-sonnet-5-5[1m]",` + msgs + `,"thinking":{"type":"disabled"}}`,
			model: "claude-sonnet-5-5[1m]",
		},
		{
			name:  "dated variant",
			body:  `{"model":"claude-sonnet-5-5-20261001",` + msgs + `,"thinking":{"type":"disabled"}}`,
			model: "claude-sonnet-5-5-20261001",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := thinking.ApplyThinking([]byte(tt.body), tt.model, "claude", "claude", "claude")
			if err != nil {
				t.Fatalf("ApplyThinking: %v", err)
			}
			if got := gjson.GetBytes(out, "thinking").Raw; got != `{"type":"between_tools"}` {
				t.Fatalf("thinking = %s, want {\"type\":\"between_tools\"}; body=%s", got, out)
			}
			if gjson.GetBytes(out, "output_config.effort").Exists() {
				t.Fatalf("between_tools retained output_config.effort: %s", out)
			}
			if got := thinking.ExtractTranslatedReasoningEffort(out, "claude"); got != "none" {
				t.Fatalf("ExtractTranslatedReasoningEffort = %q, want none", got)
			}
		})
	}
}

// Anthropic rejects thinking.type="disabled" on claude-opus-5-5, claude-fable-5,
// and claude-fable-5-1. Turning thinking off must fall back to adaptive thinking
// at the lowest supported effort ("low") with display="omitted", and keep an
// explicit {"type":"disabled"} block (without display, budget_tokens, or
// block_binding) on models that accept disabled.
func TestApplyThinking_ClaudeDisabledFallsBackOnAdaptiveOnlyModels(t *testing.T) {
	tests := []struct {
		model        string
		wantThinking string
		wantEffort   string
	}{
		{model: "claude-opus-5-5", wantThinking: "adaptive", wantEffort: "low"},
		{model: "claude-opus-5-5[1m]", wantThinking: "adaptive", wantEffort: "low"},
		{model: "claude-opus-5-5-20261001", wantThinking: "adaptive", wantEffort: "low"},
		{model: "claude-fable-5", wantThinking: "adaptive", wantEffort: "low"},
		{model: "claude-fable-5[1m]", wantThinking: "adaptive", wantEffort: "low"},
		{model: "claude-fable-5-1", wantThinking: "adaptive", wantEffort: "low"},
		{model: "claude-fable-5-1[1m]", wantThinking: "adaptive", wantEffort: "low"},
		{model: "claude-opus-5", wantThinking: "disabled"},
		{model: "claude-sonnet-5", wantThinking: "disabled"},
		{model: "claude-haiku-5-5", wantThinking: "disabled"},
		{model: "claude-opus-4-8", wantThinking: "disabled"},
		{model: "claude-opus-4-7", wantThinking: "disabled"},
		{model: "claude-haiku-4-5-20251001", wantThinking: "disabled"},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			body := `{"model":"` + tt.model + `","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled","budget_tokens":8000,"display":"summarized","block_binding":{"prefix_mismatch_behavior":"drop_block"}}}`
			out, err := thinking.ApplyThinking([]byte(body), tt.model, "claude", "claude", "claude")
			if err != nil {
				t.Fatalf("ApplyThinking: %v", err)
			}
			if got := gjson.GetBytes(out, "thinking.type").String(); got != tt.wantThinking {
				t.Fatalf("thinking.type = %q, want %q; body=%s", got, tt.wantThinking, out)
			}
			if got := gjson.GetBytes(out, "output_config.effort").String(); got != tt.wantEffort {
				t.Fatalf("output_config.effort = %q, want %q; body=%s", got, tt.wantEffort, out)
			}
			if gjson.GetBytes(out, "thinking.budget_tokens").Exists() {
				t.Fatalf("thinking retained budget_tokens: %s", out)
			}
			if gjson.GetBytes(out, "thinking.block_binding").Exists() {
				t.Fatalf("thinking retained block_binding on off rewrite: %s", out)
			}
			if tt.wantThinking == "adaptive" {
				if got := gjson.GetBytes(out, "thinking.display").String(); got != "omitted" {
					t.Fatalf("thinking.display = %q, want omitted; body=%s", got, out)
				}
			} else if gjson.GetBytes(out, "thinking.display").Exists() {
				t.Fatalf("disabled thinking retained display: %s", out)
			}
		})
	}
}

func TestApplyThinking_ClaudeDisabledFallbackInputs(t *testing.T) {
	const activeWithExtras = `{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":8000,"display":"summarized","block_binding":{"prefix_mismatch_behavior":"drop_block"}}}`
	tests := []struct {
		name  string
		body  string
		model string
	}{
		{name: "suffix none", body: activeWithExtras, model: "claude-opus-5-5(none)"},
		{name: "suffix zero", body: activeWithExtras, model: "claude-opus-5-5(0)"},
		{name: "zero budget", body: `{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":0,"display":"summarized","block_binding":{"prefix_mismatch_behavior":"drop_block"}}}`, model: "claude-opus-5-5"},
		{name: "zero budget onopus5", body: `{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":0,"display":"summarized","block_binding":{"prefix_mismatch_behavior":"drop_block"}}}`, model: "claude-opus-5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := thinking.ApplyThinking([]byte(tt.body), tt.model, "claude", "claude", "claude")
			if err != nil {
				t.Fatalf("ApplyThinking: %v", err)
			}
			if gjson.GetBytes(out, "thinking.budget_tokens").Exists() {
				t.Fatalf("retained budget_tokens: %s", out)
			}
			if gjson.GetBytes(out, "thinking.block_binding").Exists() {
				t.Fatalf("retained block_binding: %s", out)
			}
			if tt.model == "claude-opus-5" {
				if got := gjson.GetBytes(out, "thinking").Raw; got != `{"type":"disabled"}` {
					t.Fatalf("thinking = %s, want {\"type\":\"disabled\"}; body=%s", got, out)
				}
				return
			}
			if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
				t.Fatalf("thinking.type = %q, want adaptive; body=%s", got, out)
			}
			if got := gjson.GetBytes(out, "output_config.effort").String(); got != "low" {
				t.Fatalf("output_config.effort = %q, want low; body=%s", got, out)
			}
			if got := gjson.GetBytes(out, "thinking.display").String(); got != "omitted" {
				t.Fatalf("thinking.display = %q, want omitted; body=%s", got, out)
			}
		})
	}
}

// Opus 4.7, Opus 4.8, Fable 5, and Fable 5.1 reject thinking.budget_tokens and
// accept only adaptive thinking with output_config.effort. Numeric budgets must
// map to the nearest effort level, and auto must stay plain adaptive without
// a synthesized mid-range budget. Opus 4.6 and Sonnet 4.6 still accept budgets.
func TestApplyThinking_ClaudeAdaptiveOnlyModelsConvertBudgetAndAuto(t *testing.T) {
	adaptiveOnlyModels := []string{
		"claude-opus-4-7",
		"claude-opus-4-8",
		"claude-fable-5",
		"claude-fable-5-1",
	}
	for _, model := range adaptiveOnlyModels {
		t.Run(model+"/native_budget_tokens", func(t *testing.T) {
			body := `{"model":"` + model + `","max_tokens":32000,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":16384}}`
			out, err := thinking.ApplyThinking([]byte(body), model, "claude", "claude", "claude")
			if err != nil {
				t.Fatalf("ApplyThinking: %v", err)
			}
			if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
				t.Fatalf("thinking.type = %q, want adaptive; body=%s", got, out)
			}
			if gjson.GetBytes(out, "thinking.budget_tokens").Exists() {
				t.Fatalf("thinking.budget_tokens still present: %s", out)
			}
			if got := gjson.GetBytes(out, "output_config.effort").String(); got != "high" {
				t.Fatalf("output_config.effort = %q, want high; body=%s", got, out)
			}
		})

		t.Run(model+"/numeric_suffix", func(t *testing.T) {
			body := `{"model":"` + model + `","max_tokens":32000,"messages":[{"role":"user","content":"hi"}]}`
			out, err := thinking.ApplyThinking([]byte(body), model+"(64000)", "claude", "claude", "claude")
			if err != nil {
				t.Fatalf("ApplyThinking: %v", err)
			}
			if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
				t.Fatalf("thinking.type = %q, want adaptive; body=%s", got, out)
			}
			if gjson.GetBytes(out, "thinking.budget_tokens").Exists() {
				t.Fatalf("thinking.budget_tokens still present: %s", out)
			}
			if got := gjson.GetBytes(out, "output_config.effort").String(); got != "xhigh" {
				t.Fatalf("output_config.effort = %q, want xhigh; body=%s", got, out)
			}
		})

		t.Run(model+"/translated_budget", func(t *testing.T) {
			info := registry.LookupModelInfo(model, "claude")
			body := `{"model":"` + model + `","max_tokens":32000,"messages":[{"role":"user","content":"hi"}]}`
			source := `{"generationConfig":{"thinkingConfig":{"thinkingBudget":4096}}}`
			out, err := thinking.ApplyThinkingWithModelInfo([]byte(body), []byte(source), model, "gemini", "claude", "claude", info)
			if err != nil {
				t.Fatalf("ApplyThinkingWithModelInfo: %v", err)
			}
			if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
				t.Fatalf("thinking.type = %q, want adaptive; body=%s", got, out)
			}
			if gjson.GetBytes(out, "thinking.budget_tokens").Exists() {
				t.Fatalf("thinking.budget_tokens still present: %s", out)
			}
			if got := gjson.GetBytes(out, "output_config.effort").String(); got != "medium" {
				t.Fatalf("output_config.effort = %q, want medium; body=%s", got, out)
			}
		})

		t.Run(model+"/auto_suffix", func(t *testing.T) {
			body := `{"model":"` + model + `","max_tokens":32000,"messages":[{"role":"user","content":"hi"}]}`
			out, err := thinking.ApplyThinking([]byte(body), model+"(auto)", "claude", "claude", "claude")
			if err != nil {
				t.Fatalf("ApplyThinking: %v", err)
			}
			if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
				t.Fatalf("thinking.type = %q, want adaptive; body=%s", got, out)
			}
			if gjson.GetBytes(out, "thinking.budget_tokens").Exists() {
				t.Fatalf("auto synthesized budget_tokens: %s", out)
			}
			if gjson.GetBytes(out, "output_config.effort").Exists() {
				t.Fatalf("auto set explicit effort: %s", out)
			}
		})
	}

	// Configured routes with unordered levels or special "auto" token must still
	// select the lowest canonical effort level ("low") when falling back from off.
	t.Run("configured_unordered_or_auto_levels_select_lowest_canonical", func(t *testing.T) {
		for _, levels := range [][]string{
			{"high", "medium", "low"},
			{"auto", "high", "low"},
		} {
			info := &registry.ModelInfo{
				ID:   "claude-opus-5-5",
				Type: "claude",
				Thinking: &registry.ThinkingSupport{
					ZeroAllowed:    false,
					DynamicAllowed: true,
					Levels:         levels,
				},
			}
			body := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"}}`)
			out, err := thinking.ApplyThinkingWithModelInfo(body, nil, "claude-opus-5-5", "claude", "claude", "claude", info)
			if err != nil {
				t.Fatalf("ApplyThinkingWithModelInfo(%v): %v", levels, err)
			}
			if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
				t.Fatalf("levels=%v thinking.type = %q, want adaptive; body=%s", levels, got, out)
			}
			if got := gjson.GetBytes(out, "output_config.effort").String(); got != "low" {
				t.Fatalf("levels=%v output_config.effort = %q, want low; body=%s", levels, got, out)
			}
		}
	})

	for _, hybridModel := range []string{"claude-opus-4-6", "claude-sonnet-4-6"} {
		t.Run(hybridModel+"/retains_budget_tokens", func(t *testing.T) {
			body := `{"model":"` + hybridModel + `","max_tokens":32000,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":16384}}`
			out, err := thinking.ApplyThinking([]byte(body), hybridModel, "claude", "claude", "claude")
			if err != nil {
				t.Fatalf("ApplyThinking: %v", err)
			}
			if got := gjson.GetBytes(out, "thinking.type").String(); got != "enabled" {
				t.Fatalf("thinking.type = %q, want enabled; body=%s", got, out)
			}
			if got := gjson.GetBytes(out, "thinking.budget_tokens").Int(); got != 16384 {
				t.Fatalf("thinking.budget_tokens = %d, want 16384; body=%s", got, out)
			}
		})
	}
}
