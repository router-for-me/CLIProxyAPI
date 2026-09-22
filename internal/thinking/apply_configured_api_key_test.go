package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/codex"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/openai"
	"github.com/tidwall/gjson"
)

func TestApplyThinkingWithModelInfoMapsCrossFamilyHighIntent(t *testing.T) {
	tests := []struct {
		name      string
		source    string
		supported []string
		want      string
	}{
		{name: "xhigh stays xhigh", source: "xhigh", supported: []string{"high", "max", "xhigh"}, want: "xhigh"},
		{name: "xhigh prefers max", source: "xhigh", supported: []string{"high", "max"}, want: "max"},
		{name: "xhigh falls back to high", source: "xhigh", supported: []string{"high"}, want: "high"},
		{name: "max stays max", source: "max", supported: []string{"high", "xhigh", "max"}, want: "max"},
		{name: "max prefers xhigh", source: "max", supported: []string{"high", "xhigh"}, want: "xhigh"},
		{name: "max falls back to high", source: "max", supported: []string{"high"}, want: "high"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			modelInfo := &registry.ModelInfo{
				ID:       "claude-upstream",
				Type:     "claude",
				Thinking: &registry.ThinkingSupport{Levels: tc.supported},
			}
			body := []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}}`)
			source := []byte(`{"reasoning_effort":"` + tc.source + `"}`)
			out, err := thinking.ApplyThinkingWithModelInfo(body, source, "claude-upstream", "openai", "claude", "claude", modelInfo)
			if err != nil {
				t.Fatalf("ApplyThinkingWithModelInfo() error = %v", err)
			}
			if got := gjson.GetBytes(out, "output_config.effort").String(); got != tc.want {
				t.Fatalf("output effort = %q, want %q; body=%s", got, tc.want, out)
			}
		})
	}
}

func TestApplyThinkingWithModelInfoMapsOpenAICompatibilityHighIntent(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:       "compat-upstream",
		Type:     "openai-compatibility",
		Thinking: &registry.ThinkingSupport{Levels: []string{"high", "max"}},
	}
	body := []byte(`{"reasoning_effort":"high"}`)
	source := []byte(`{"reasoning_effort":"xhigh"}`)
	out, err := thinking.ApplyThinkingWithModelInfo(body, source, "compat-upstream", "openai", "openai", "compat-provider", modelInfo)
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfo() error = %v", err)
	}
	if got := gjson.GetBytes(out, "reasoning_effort").String(); got != "max" {
		t.Fatalf("reasoning_effort = %q, want max; body=%s", got, out)
	}
}

func TestApplyThinkingWithModelInfoMapsResponsesToCodexHighIntent(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:       "codex-upstream",
		Type:     "codex",
		Thinking: &registry.ThinkingSupport{Levels: []string{"high", "xhigh"}},
	}
	body := []byte(`{"reasoning":{"effort":"high"}}`)
	source := []byte(`{"reasoning":{"effort":"max"}}`)
	out, err := thinking.ApplyThinkingWithModelInfo(body, source, "codex-upstream", "openai-response", "codex", "codex", modelInfo)
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfo() error = %v", err)
	}
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "xhigh" {
		t.Fatalf("reasoning.effort = %q, want xhigh; body=%s", got, out)
	}
}

func TestApplyThinkingWithModelInfoKeepsSameFamilyValidationStrict(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:       "openai-upstream",
		Type:     "openai",
		Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}},
	}
	body := []byte(`{"reasoning_effort":"xhigh"}`)
	out, err := thinking.ApplyThinkingWithModelInfo(body, body, "openai-upstream", "openai", "openai", "openai", modelInfo)
	if err == nil {
		t.Fatalf("ApplyThinkingWithModelInfo() error = nil, want unsupported xhigh error; body=%s", out)
	}
}

func TestApplyThinkingWithModelInfoAppliesEnabledSummaryOnlyClaudeVisibility(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:       "private-claude",
		Type:     "claude",
		Thinking: &registry.ThinkingSupport{Levels: []string{"high"}},
	}
	out, err := thinking.ApplyThinkingWithModelInfo(
		[]byte(`{"model":"private-claude","max_tokens":32000}`),
		[]byte(`{"reasoning":{"summary":"auto"}}`),
		"private-claude", "openai-response", "claude", "claude", modelInfo,
	)
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfo() error = %v", err)
	}
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want adaptive; body=%s", got, out)
	}
	if got := gjson.GetBytes(out, "thinking.display").String(); got != "summarized" {
		t.Fatalf("thinking.display = %q, want summarized; body=%s", got, out)
	}
}

func TestApplyThinkingWithModelInfoAndSummaryDropsInferredClaudeModeWhenSummaryRemoved(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:       "private-manual-claude",
		Type:     "claude",
		Thinking: &registry.ThinkingSupport{Min: 1024, Max: 16000},
	}
	out, err := thinking.ApplyThinkingWithModelInfoAndSummary(
		[]byte(`{"model":"private-manual-claude","max_tokens":32000,"thinking":{"type":"adaptive"}}`),
		[]byte(`{"reasoning":{"summary":"auto"}}`),
		"private-manual-claude", "openai-response", "claude", "claude", modelInfo,
		thinking.SummaryConfig{},
	)
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfoAndSummary() error = %v", err)
	}
	if gjson.GetBytes(out, "thinking").Exists() {
		t.Fatalf("removed summary retained globally inferred adaptive thinking: %s", out)
	}
}

func TestApplyThinkingWithModelInfoDoesNotActivateClaudeForDisabledSummary(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:       "private-claude",
		Type:     "claude",
		Thinking: &registry.ThinkingSupport{Levels: []string{"high"}},
	}
	out, err := thinking.ApplyThinkingWithModelInfo(
		[]byte(`{"model":"private-claude","max_tokens":32000}`),
		[]byte(`{"reasoning":{"summary":null}}`),
		"private-claude", "openai-response", "claude", "claude", modelInfo,
	)
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfo() error = %v", err)
	}
	if gjson.GetBytes(out, "thinking").Exists() {
		t.Fatalf("disabled summary activated Claude thinking: %s", out)
	}
}

func TestApplyThinkingWithModelInfoSummaryOnlyDoesNotInventOpenAIEffort(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:       "private-openai",
		Type:     "openai",
		Thinking: &registry.ThinkingSupport{Levels: []string{"high", "max"}},
	}
	out, err := thinking.ApplyThinkingWithModelInfo(
		[]byte(`{"model":"private-openai","messages":[{"role":"user","content":"hi"}]}`),
		[]byte(`{"model":"private-openai","reasoning":{"summary":"auto"},"input":"hi"}`),
		"private-openai", "openai-response", "openai", "openai", modelInfo,
	)
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfo() error = %v; body=%s", err, out)
	}
	if gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("summary-only request invented reasoning_effort: %s", out)
	}
}

func TestApplyThinkingWithSummaryKeepsOpenAIChatSuffixNone(t *testing.T) {
	out, err := thinking.ApplyThinkingWithSummary(
		[]byte(`{"model":"private-openai","messages":[{"role":"user","content":"hi"}]}`),
		"private-openai(none)", "openai-response", "openai", "openai",
		thinking.SummaryConfig{Mode: thinking.SummaryEnabled, Detail: "auto"},
	)
	if err != nil {
		t.Fatalf("ApplyThinkingWithSummary() error = %v; body=%s", err, out)
	}
	if got := gjson.GetBytes(out, "reasoning_effort").String(); got != "none" {
		t.Fatalf("reasoning_effort = %q, want none; body=%s", got, out)
	}
}

func TestApplyThinkingWithModelInfoUsesOpenRouterVisibility(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:       "openrouter-model",
		Type:     "openai-compatibility",
		Thinking: &registry.ThinkingSupport{Levels: []string{"high", "max"}},
	}
	out, err := thinking.ApplyThinkingWithModelInfo(
		[]byte(`{"model":"openrouter-model","messages":[{"role":"user","content":"hi"}]}`),
		[]byte(`{"model":"openrouter-model","reasoning":{"summary":"auto"},"input":"hi"}`),
		"openrouter-model", "openai-response", "openai", "openrouter", modelInfo,
	)
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfo() error = %v; body=%s", err, out)
	}
	if exclude := gjson.GetBytes(out, "reasoning.exclude"); !exclude.Exists() || exclude.Bool() {
		t.Fatalf("OpenRouter summary visibility not enabled: %s", out)
	}
	if gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("OpenRouter summary visibility invented reasoning_effort: %s", out)
	}
}

func TestApplyThinkingWithModelInfoUsesOriginalResponsesEffort(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:       "claude-upstream",
		Type:     "claude",
		Thinking: &registry.ThinkingSupport{Levels: []string{"high", "max"}},
	}
	body := []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}}`)
	source := []byte(`{"reasoning":{"effort":"xhigh"}}`)
	out, err := thinking.ApplyThinkingWithModelInfo(body, source, "claude-upstream", "openai-response", "claude", "claude", modelInfo)
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfo() error = %v", err)
	}
	if got := gjson.GetBytes(out, "output_config.effort").String(); got != "max" {
		t.Fatalf("output effort = %q, want max; body=%s", got, out)
	}
}
