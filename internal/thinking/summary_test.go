package thinking

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestExtractSummaryConfig(t *testing.T) {
	tests := []struct {
		name       string
		format     string
		body       string
		wantMode   SummaryMode
		wantDetail string
	}{
		{name: "chat effort enables", format: "openai", body: `{"reasoning_effort":"high"}`, wantMode: SummaryEnabled, wantDetail: "auto"},
		{name: "chat none disables", format: "openai", body: `{"reasoning_effort":"none"}`, wantMode: SummaryDisabled},
		{name: "chat missing unspecified", format: "openai", body: `{}`, wantMode: SummaryUnspecified},
		{name: "chat null effort unspecified", format: "openai", body: `{"reasoning_effort":null}`, wantMode: SummaryUnspecified},
		{name: "chat non-string effort unspecified", format: "openai", body: `{"reasoning_effort":17}`, wantMode: SummaryUnspecified},
		{name: "chat google extension false overrides effort", format: "openai", body: `{"reasoning_effort":"high","extra_body":{"google":{"thinking_config":{"include_thoughts":false}}}}`, wantMode: SummaryDisabled},
		{name: "chat google extension true", format: "openai", body: `{"extra_body":{"google":{"thinking_config":{"include_thoughts":true}}}}`, wantMode: SummaryEnabled, wantDetail: "auto"},
		{name: "chat exclude disables", format: "openai", body: `{"reasoning_effort":"high","reasoning":{"exclude":true}}`, wantMode: SummaryDisabled},
		{name: "chat exclude false enables", format: "openai", body: `{"reasoning":{"effort":"high","exclude":false}}`, wantMode: SummaryEnabled, wantDetail: "auto"},
		{name: "chat legacy include_reasoning false disables", format: "openai", body: `{"reasoning_effort":"high","include_reasoning":false}`, wantMode: SummaryDisabled},
		{name: "chat legacy include_reasoning true enables", format: "openai", body: `{"include_reasoning":true}`, wantMode: SummaryEnabled, wantDetail: "auto"},
		{name: "chat reasoning enabled false disables", format: "openai", body: `{"reasoning":{"enabled":false}}`, wantMode: SummaryDisabled},
		{name: "chat reasoning enabled true enables", format: "openai", body: `{"reasoning":{"enabled":true}}`, wantMode: SummaryEnabled, wantDetail: "auto"},
		{name: "chat exclude wins over include_reasoning", format: "openai", body: `{"reasoning":{"exclude":true},"include_reasoning":true}`, wantMode: SummaryDisabled},
		{name: "chat non-boolean include_reasoning unspecified", format: "openai", body: `{"include_reasoning":"false"}`, wantMode: SummaryUnspecified},
		{name: "responses effort alone unspecified", format: "openai-response", body: `{"reasoning":{"effort":"high"}}`, wantMode: SummaryUnspecified},
		{name: "responses summary auto", format: "openai-response", body: `{"reasoning":{"effort":"high","summary":"auto"}}`, wantMode: SummaryEnabled, wantDetail: "auto"},
		{name: "responses summary concise", format: "openai-response", body: `{"reasoning":{"summary":"concise"}}`, wantMode: SummaryEnabled, wantDetail: "concise"},
		{name: "responses summary null", format: "openai-response", body: `{"reasoning":{"summary":null}}`, wantMode: SummaryDisabled},
		{name: "responses boolean summary invalid", format: "openai-response", body: `{"reasoning":{"summary":true}}`, wantMode: SummaryUnspecified},
		{name: "responses deprecated generate summary", format: "openai-response", body: `{"reasoning":{"generate_summary":"detailed"}}`, wantMode: SummaryEnabled, wantDetail: "detailed"},
		{name: "claude summarized", format: "claude", body: `{"thinking":{"type":"adaptive","display":"summarized"}}`, wantMode: SummaryEnabled, wantDetail: "auto"},
		{name: "claude omitted", format: "claude", body: `{"thinking":{"type":"enabled","budget_tokens":2048,"display":"omitted"}}`, wantMode: SummaryDisabled},
		{name: "claude display without type is invalid", format: "claude", body: `{"thinking":{"display":"summarized"}}`, wantMode: SummaryUnspecified},
		{name: "claude display with auto type is invalid", format: "claude", body: `{"thinking":{"type":"auto","display":"summarized"}}`, wantMode: SummaryUnspecified},
		// ApplySummaryConfig runs before ApplyThinking fills budget_tokens, so an
		// absent budget must not be read as inactive thinking.
		{name: "claude enabled display without budget is valid", format: "claude", body: `{"thinking":{"type":"enabled","display":"summarized"}}`, wantMode: SummaryEnabled, wantDetail: "auto"},
		{name: "claude enabled display with zero budget is invalid", format: "claude", body: `{"thinking":{"type":"enabled","budget_tokens":0,"display":"summarized"}}`, wantMode: SummaryUnspecified},
		{name: "claude auto compatibility budget summarized", format: "claude", body: `{"thinking":{"type":"enabled","budget_tokens":-1,"display":"summarized"}}`, wantMode: SummaryEnabled, wantDetail: "auto"},
		{name: "claude auto compatibility budget omitted", format: "claude", body: `{"thinking":{"type":"enabled","budget_tokens":-1,"display":"omitted"}}`, wantMode: SummaryDisabled},
		{name: "gemini include true", format: "gemini", body: `{"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}`, wantMode: SummaryEnabled, wantDetail: "auto"},
		{name: "gemini include false", format: "gemini", body: `{"generationConfig":{"thinkingConfig":{"includeThoughts":false}}}`, wantMode: SummaryDisabled},
		{name: "antigravity include true", format: "antigravity", body: `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}}`, wantMode: SummaryEnabled, wantDetail: "auto"},
		{name: "interactions auto", format: "interactions", body: `{"generation_config":{"thinking_summaries":"auto"}}`, wantMode: SummaryEnabled, wantDetail: "auto"},
		{name: "interactions none", format: "interactions", body: `{"generation_config":{"thinking_summaries":"none"}}`, wantMode: SummaryDisabled},
		{name: "interactions nested snake include false", format: "interactions", body: `{"generation_config":{"thinking_config":{"include_thoughts":false}}}`, wantMode: SummaryDisabled},
		{name: "interactions nested camel include true", format: "interactions", body: `{"generation_config":{"thinking_config":{"includeThoughts":true}}}`, wantMode: SummaryEnabled, wantDetail: "auto"},
		{name: "interactions camel config snake include true", format: "interactions", body: `{"generation_config":{"thinkingConfig":{"include_thoughts":true}}}`, wantMode: SummaryEnabled, wantDetail: "auto"},
		{name: "interactions camel config camel include false", format: "interactions", body: `{"generation_config":{"thinkingConfig":{"includeThoughts":false}}}`, wantMode: SummaryDisabled},
		{name: "interactions enum wins over compatibility reasoning", format: "interactions", body: `{"generation_config":{"thinking_summaries":"none"},"reasoning":{"summary":"auto"}}`, wantMode: SummaryDisabled},
		{name: "interactions compatibility reasoning auto", format: "interactions", body: `{"reasoning":{"summary":"auto"}}`, wantMode: SummaryEnabled, wantDetail: "auto"},
		{name: "interactions compatibility reasoning none", format: "interactions", body: `{"reasoning":{"summary":"none"}}`, wantMode: SummaryDisabled},
		{name: "interactions enum wins over include alias", format: "interactions", body: `{"generation_config":{"thinking_summaries":"none","thinking_config":{"include_thoughts":true}}}`, wantMode: SummaryDisabled},
		{name: "interactions string include alias is invalid", format: "interactions", body: `{"generation_config":{"thinking_config":{"include_thoughts":"false"}}}`, wantMode: SummaryUnspecified},
		{name: "interactions detailed is invalid", format: "interactions", body: `{"generation_config":{"thinking_summaries":"detailed"}}`, wantMode: SummaryUnspecified},
		{name: "interactions boolean is invalid", format: "interactions", body: `{"generation_config":{"thinking_summaries":true}}`, wantMode: SummaryUnspecified},
		{name: "gemini string bool is invalid", format: "gemini", body: `{"generationConfig":{"thinkingConfig":{"includeThoughts":"true"}}}`, wantMode: SummaryUnspecified},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ExtractSummaryConfig([]byte(test.body), test.format)
			if got.Mode != test.wantMode || got.Detail != test.wantDetail {
				t.Fatalf("ExtractSummaryConfig() = %+v, want mode=%v detail=%q", got, test.wantMode, test.wantDetail)
			}
		})
	}
}

func TestExtractExplicitSummaryConfigDoesNotUseChatEffort(t *testing.T) {
	body := []byte(`{"reasoning_effort":"high"}`)
	if got := ExtractExplicitSummaryConfig(body, "openai"); got.Mode != SummaryUnspecified {
		t.Fatalf("ExtractExplicitSummaryConfig() = %+v, want unspecified", got)
	}

	body = []byte(`{"reasoning_effort":"high","reasoning":{"exclude":true}}`)
	if got := ExtractExplicitSummaryConfig(body, "openai"); got.Mode != SummaryDisabled {
		t.Fatalf("ExtractExplicitSummaryConfig() = %+v, want disabled", got)
	}
}

func TestApplySummaryConfig(t *testing.T) {
	tests := []struct {
		name   string
		format string
		body   string
		config SummaryConfig
		path   string
		want   string
	}{
		{name: "chat enabled invents no effort", format: "openai", config: SummaryConfig{Mode: SummaryEnabled}, path: "reasoning_effort", want: ""},
		{name: "chat enabled preserves active effort", format: "openai", body: `{"reasoning_effort":"high"}`, config: SummaryConfig{Mode: SummaryEnabled}, path: "reasoning_effort", want: "high"},
		{name: "chat enabled preserves disabled effort", format: "openai", body: `{"reasoning_effort":"none"}`, config: SummaryConfig{Mode: SummaryEnabled}, path: "reasoning_effort", want: "none"},
		// Chat cannot express "reason but hide", so disabling must not fall back to
		// reasoning_effort:"none", which would disable reasoning altogether.
		{name: "chat disabled preserves requested effort", format: "openai", body: `{"reasoning_effort":"high"}`, config: SummaryConfig{Mode: SummaryDisabled}, path: "reasoning_effort", want: "high"},
		{name: "chat disabled sets openrouter exclude when present", format: "openai", body: `{"reasoning":{"effort":"high","exclude":false}}`, config: SummaryConfig{Mode: SummaryDisabled}, path: "reasoning.exclude", want: "true"},
		{name: "chat enabled clears openrouter exclude when present", format: "openai", body: `{"reasoning":{"effort":"high","exclude":true}}`, config: SummaryConfig{Mode: SummaryEnabled}, path: "reasoning.exclude", want: "false"},
		{name: "chat disabled updates legacy include_reasoning when present", format: "openai", body: `{"reasoning_effort":"high","include_reasoning":true}`, config: SummaryConfig{Mode: SummaryDisabled}, path: "include_reasoning", want: "false"},
		{name: "chat disabled invents no openrouter field", format: "openai", body: `{"reasoning_effort":"high"}`, config: SummaryConfig{Mode: SummaryDisabled}, path: "reasoning", want: ""},
		{name: "claude enabled", format: "claude", body: `{"thinking":{"type":"adaptive"}}`, config: SummaryConfig{Mode: SummaryEnabled}, path: "thinking.display", want: "summarized"},
		{name: "claude disabled", format: "claude", body: `{"thinking":{"type":"enabled","budget_tokens":2048}}`, config: SummaryConfig{Mode: SummaryDisabled}, path: "thinking.display", want: "omitted"},
		{name: "gemini enabled", format: "gemini", config: SummaryConfig{Mode: SummaryEnabled}, path: "generationConfig.thinkingConfig.includeThoughts", want: "true"},
		{name: "gemini disabled", format: "gemini", config: SummaryConfig{Mode: SummaryDisabled}, path: "generationConfig.thinkingConfig.includeThoughts", want: "false"},
		{name: "antigravity enabled", format: "antigravity", config: SummaryConfig{Mode: SummaryEnabled}, path: "request.generationConfig.thinkingConfig.includeThoughts", want: "true"},
		{name: "interactions detail collapses to auto", format: "interactions", config: SummaryConfig{Mode: SummaryEnabled, Detail: "detailed"}, path: "generation_config.thinking_summaries", want: "auto"},
		{name: "interactions disabled", format: "interactions", config: SummaryConfig{Mode: SummaryDisabled}, path: "generation_config.thinking_summaries", want: "none"},
		{name: "responses concise", format: "openai-response", config: SummaryConfig{Mode: SummaryEnabled, Detail: "concise"}, path: "reasoning.summary", want: "concise"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := test.body
			if body == "" {
				body = `{}`
			}
			out := ApplySummaryConfig([]byte(body), test.format, test.config)
			if got := gjson.GetBytes(out, test.path).String(); got != test.want {
				t.Fatalf("%s = %q, want %q; body=%s", test.path, got, test.want, out)
			}
		})
	}
}

func TestApplySummaryConfig_OpenAIChatProviderDialects(t *testing.T) {
	tests := []struct {
		name         string
		provider     string
		body         string
		mode         SummaryMode
		wantExclude  string
		wantExisting bool
		wantEffort   string
	}{
		{name: "OpenAI does not invent visibility", provider: "openai", body: `{}`, mode: SummaryEnabled},
		{name: "OpenRouter enables visibility", provider: "openrouter", body: `{}`, mode: SummaryEnabled, wantExclude: "false", wantExisting: true},
		{name: "OpenRouter disables visibility", provider: "prod-openrouter", body: `{}`, mode: SummaryDisabled, wantExclude: "true", wantExisting: true},
		{name: "DeepSeek preserves documented effort", provider: "deepseek", body: `{"reasoning_effort":"high"}`, mode: SummaryDisabled, wantEffort: "high"},
		{name: "Kimi preserves documented K3 effort", provider: "kimi", body: `{"reasoning_effort":"max"}`, mode: SummaryEnabled, wantEffort: "max"},
		{name: "Moonshot does not invent visibility", provider: "moonshot", body: `{"thinking":{"type":"enabled"}}`, mode: SummaryEnabled},
		{name: "generic provider updates existing OpenRouter field", provider: "openai-compatibility", body: `{"reasoning":{"exclude":false}}`, mode: SummaryDisabled, wantExclude: "true", wantExisting: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out := applySummaryConfigForProvider([]byte(test.body), "openai", "model", test.provider, nil, SummaryConfig{Mode: test.mode})
			exclude := gjson.GetBytes(out, "reasoning.exclude")
			if exclude.Exists() != test.wantExisting {
				t.Fatalf("reasoning.exclude exists = %v, want %v; body=%s", exclude.Exists(), test.wantExisting, out)
			}
			if test.wantExisting && exclude.String() != test.wantExclude {
				t.Fatalf("reasoning.exclude = %q, want %q; body=%s", exclude.String(), test.wantExclude, out)
			}
			effort := gjson.GetBytes(out, "reasoning_effort")
			if test.wantEffort == "" {
				if effort.Exists() {
					t.Fatalf("summary visibility invented reasoning_effort: %s", out)
				}
			} else if effort.String() != test.wantEffort {
				t.Fatalf("reasoning_effort = %q, want %q; body=%s", effort.String(), test.wantEffort, out)
			}
		})
	}
}

func TestApplySummaryConfigNormalizesTargetAliases(t *testing.T) {
	tests := []struct {
		format    string
		body      string
		canonical string
		alias     string
	}{
		{format: "gemini", body: `{"generationConfig":{"thinkingConfig":{"include_thoughts":true}}}`, canonical: "generationConfig.thinkingConfig.includeThoughts", alias: "generationConfig.thinkingConfig.include_thoughts"},
		{format: "antigravity", body: `{"request":{"generationConfig":{"thinkingConfig":{"include_thoughts":true}}}}`, canonical: "request.generationConfig.thinkingConfig.includeThoughts", alias: "request.generationConfig.thinkingConfig.include_thoughts"},
		{format: "interactions", body: `{"generation_config":{"thinkingSummaries":"auto"}}`, canonical: "generation_config.thinking_summaries", alias: "generation_config.thinkingSummaries"},
	}
	for _, test := range tests {
		out := ApplySummaryConfig([]byte(test.body), test.format, SummaryConfig{Mode: SummaryEnabled})
		if !gjson.GetBytes(out, test.canonical).Exists() {
			t.Fatalf("%s missing canonical field: %s", test.format, out)
		}
		if gjson.GetBytes(out, test.alias).Exists() {
			t.Fatalf("%s retained alias %s: %s", test.format, test.alias, out)
		}
	}
}

// Anthropic requires thinking.type, and rejects display on a disabled block, so
// display must never be written unless thinking is already active.
func TestApplySummaryConfig_ClaudeDisplayRequiresActiveThinking(t *testing.T) {
	bodies := []string{
		`{}`,
		`{"messages":[{"role":"user","content":"hi"}]}`,
		`{"thinking":{"type":"disabled"}}`,
	}
	for _, mode := range []SummaryMode{SummaryEnabled, SummaryDisabled} {
		for _, body := range bodies {
			out := ApplySummaryConfig([]byte(body), "claude", SummaryConfig{Mode: mode})
			if gjson.GetBytes(out, "thinking.display").Exists() {
				t.Fatalf("mode %v wrote display without active thinking: %s", mode, out)
			}
			if !bytes.Equal(out, []byte(body)) {
				t.Fatalf("mode %v changed body: got %s, want %s", mode, out, body)
			}
		}
	}
}

func TestApplySummaryConfigForModel_ClaudeEnabledSummaryUsesValidThinkingMode(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		body       string
		wantType   string
		wantBudget int64
	}{
		{name: "adaptive model", model: "claude-opus-5", body: `{"model":"claude-opus-5","max_tokens":32000}`, wantType: "adaptive"},
		{name: "manual model", model: "claude-haiku-4-5-20251001", body: `{"model":"claude-haiku-4-5-20251001","max_tokens":32000}`, wantType: "enabled", wantBudget: 1024},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out := ApplySummaryConfigForModel([]byte(test.body), "claude", test.model, SummaryConfig{Mode: SummaryEnabled})
			if got := gjson.GetBytes(out, "thinking.type").String(); got != test.wantType {
				t.Fatalf("thinking.type = %q, want %q; body=%s", got, test.wantType, out)
			}
			if got := gjson.GetBytes(out, "thinking.display").String(); got != "summarized" {
				t.Fatalf("thinking.display = %q, want summarized; body=%s", got, out)
			}
			if test.wantBudget > 0 && gjson.GetBytes(out, "thinking.budget_tokens").Int() != test.wantBudget {
				t.Fatalf("thinking.budget_tokens = %d, want %d; body=%s", gjson.GetBytes(out, "thinking.budget_tokens").Int(), test.wantBudget, out)
			}
		})
	}
}

// Disabling summaries must not make CPA add a Claude thinking block. Absence
// preserves the per-model default: newer models may still think by default,
// while older models remain off.
func TestApplySummaryConfigForModel_ClaudeDisabledSummaryDoesNotEnableThinking(t *testing.T) {
	for _, model := range []string{"claude-opus-5", "claude-haiku-4-5-20251001"} {
		body := []byte(`{"model":"` + model + `","max_tokens":32000}`)
		out := ApplySummaryConfigForModel(body, "claude", model, SummaryConfig{Mode: SummaryDisabled})
		if gjson.GetBytes(out, "thinking").Exists() {
			t.Fatalf("model %s gained thinking for a disabled summary: %s", model, out)
		}
	}
}

func TestApplySummaryConfig_ResponsesNormalizesDeprecatedGenerateSummary(t *testing.T) {
	out := ApplySummaryConfig([]byte(`{"reasoning":{"generate_summary":"detailed"}}`), "openai-response", SummaryConfig{Mode: SummaryEnabled, Detail: "detailed"})
	if got := gjson.GetBytes(out, "reasoning.summary").String(); got != "detailed" {
		t.Fatalf("reasoning.summary = %q, want detailed; body=%s", got, out)
	}
	if gjson.GetBytes(out, "reasoning.generate_summary").Exists() {
		t.Fatalf("deprecated reasoning.generate_summary remained: %s", out)
	}
}

func TestApplySummaryConfig_ResponsesDisabledOmitsSummary(t *testing.T) {
	out := ApplySummaryConfig([]byte(`{"reasoning":{"effort":"high","summary":"auto"}}`), "openai-response", SummaryConfig{Mode: SummaryDisabled})
	if result := gjson.GetBytes(out, "reasoning.summary"); result.Exists() {
		t.Fatalf("reasoning.summary = %s, want absent; body=%s", result.Raw, out)
	}
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "high" {
		t.Fatalf("reasoning.effort = %q, want high; body=%s", got, out)
	}
}

func TestApplySummaryConfig_ResponsesDisabledDropsEmptyReasoning(t *testing.T) {
	out := ApplySummaryConfig([]byte(`{"model":"gpt-5.4","reasoning":{"summary":"auto"}}`), "openai-response", SummaryConfig{Mode: SummaryDisabled})
	if gjson.GetBytes(out, "reasoning").Exists() {
		t.Fatalf("empty reasoning object left behind: %s", out)
	}
}

func TestApplySummaryConfig_UnspecifiedLeavesBodyUnchanged(t *testing.T) {
	body := []byte(`{"thinking":{"type":"adaptive"}}`)
	if got := ApplySummaryConfig(body, "claude", SummaryConfig{}); !bytes.Equal(got, body) {
		t.Fatalf("unspecified summary changed body: got %s, want %s", got, body)
	}
}
