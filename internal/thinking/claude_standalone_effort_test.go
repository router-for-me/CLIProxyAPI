package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/codex"
	codexclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/codex/claude"
	"github.com/tidwall/gjson"
)

func TestApplyThinking_ClaudeStandaloneEffort(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantEffort string
	}{
		{name: "high without thinking", body: `{"output_config":{"effort":"high"}}`, wantEffort: "high"},
		{name: "low without thinking", body: `{"output_config":{"effort":"low"}}`, wantEffort: "low"},
		{name: "normalized effort", body: `{"output_config":{"effort":" HIGH "}}`, wantEffort: "high"},
		{name: "none without thinking", body: `{"output_config":{"effort":"none"}}`, wantEffort: "none"},
		{name: "auto without thinking", body: `{"output_config":{"effort":"auto"}}`, wantEffort: "auto"},
		{name: "missing effort", body: `{}`, wantEffort: ""},
		{name: "empty effort", body: `{"output_config":{"effort":""}}`, wantEffort: ""},
		{name: "blank effort", body: `{"output_config":{"effort":"   "}}`, wantEffort: ""},
		{name: "non-string effort", body: `{"output_config":{"effort":123}}`, wantEffort: ""},
		{name: "disabled wins", body: `{"thinking":{"type":"disabled"},"output_config":{"effort":"high"}}`, wantEffort: "none"},
		{name: "budget wins", body: `{"thinking":{"type":"enabled","budget_tokens":8192},"output_config":{"effort":"high"}}`, wantEffort: "medium"},
		{name: "enabled without budget", body: `{"thinking":{"type":"enabled"},"output_config":{"effort":"high"}}`, wantEffort: "high"},
		{name: "unknown thinking type unchanged", body: `{"thinking":{"type":"unknown"},"output_config":{"effort":"high"}}`, wantEffort: ""},
		{name: "empty thinking object unchanged", body: `{"thinking":{},"output_config":{"effort":"high"}}`, wantEffort: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, errApply := thinking.ApplyThinking([]byte(tt.body), "custom-openai", "claude", "openai", "openai")
			if errApply != nil {
				t.Fatalf("ApplyThinking returned error: %v", errApply)
			}
			if got := gjson.GetBytes(out, "reasoning_effort").String(); got != tt.wantEffort {
				t.Fatalf("reasoning_effort = %q, want %q; body=%s", got, tt.wantEffort, out)
			}
		})
	}
}

func TestClaudeStandaloneEffortPreservesNativeThinkingDefault(t *testing.T) {
	for _, tt := range []struct {
		name string
		info *registry.ModelInfo
	}{
		{
			name: "adaptive model",
			info: &registry.ModelInfo{ID: "claude-opus-4-6", Thinking: &registry.ThinkingSupport{
				Levels: []string{"low", "medium", "high", "max"},
			}},
		},
		{
			name: "budget model",
			info: &registry.ModelInfo{ID: "claude-sonnet-4-5", Thinking: &registry.ThinkingSupport{
				Min: 1024, Max: 32768, ZeroAllowed: true,
			}},
		},
		{name: "custom model"},
		{name: "model without thinking support", info: &registry.ModelInfo{ID: "claude-no-thinking"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"max_tokens":32768,"messages":[{"role":"user","content":"hi"}],"output_config":{"effort":"high"}}`)
			out, errApply := thinking.ApplyThinkingWithModelInfo(body, body, "native-claude", "claude", "claude", "claude", tt.info)
			if errApply != nil {
				t.Fatalf("ApplyThinkingWithModelInfo returned error: %v", errApply)
			}
			if string(out) != string(body) {
				t.Fatalf("native standalone effort must preserve the request, got %s; want %s", out, body)
			}
			if tt.info != nil && tt.info.Thinking == nil {
				return
			}
			out, errApply = thinking.ApplyThinkingWithModelInfo(body, body, "native-claude(low)", "claude", "claude", "claude", tt.info)
			if errApply != nil {
				t.Fatalf("ApplyThinkingWithModelInfo with suffix returned error: %v", errApply)
			}
			if !gjson.GetBytes(out, "thinking.type").Exists() {
				t.Fatalf("explicit suffix must still configure thinking; body=%s", out)
			}
		})
	}
}

func TestClaudeToCodexStandaloneEffort(t *testing.T) {
	tests := []struct {
		name       string
		fields     string
		suffix     string
		wantEffort string
	}{
		{name: "standalone high", fields: `,"output_config":{"effort":"high"}`, wantEffort: "high"},
		{name: "standalone low", fields: `,"output_config":{"effort":"low"}`, wantEffort: "low"},
		{name: "normalized high", fields: `,"output_config":{"effort":" HIGH "}`, wantEffort: "high"},
		{name: "adaptive high", fields: `,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}`, wantEffort: "high"},
		{name: "enabled budget high", fields: `,"thinking":{"type":"enabled","budget_tokens":16000}`, wantEffort: "high"},
		{name: "budget precedes effort", fields: `,"thinking":{"type":"enabled","budget_tokens":8192},"output_config":{"effort":"high"}`, wantEffort: "medium"},
		{name: "disabled precedes effort", fields: `,"thinking":{"type":"disabled"},"output_config":{"effort":"high"}`, wantEffort: "low"},
		{name: "suffix precedes standalone effort", fields: `,"output_config":{"effort":"high"}`, suffix: "(low)", wantEffort: "low"},
		{name: "suffix precedes disabled", fields: `,"thinking":{"type":"disabled"},"output_config":{"effort":"low"}`, suffix: "(high)", wantEffort: "high"},
		{name: "missing effort keeps default", wantEffort: "medium"},
		{name: "blank effort keeps default", fields: `,"output_config":{"effort":"   "}`, wantEffort: "medium"},
		{name: "non-string effort keeps default", fields: `,"output_config":{"effort":123}`, wantEffort: "medium"},
	}
	info := &registry.ModelInfo{
		ID: "gpt-6.1-sol",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"low", "medium", "high", "xhigh"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := []byte(`{"model":"gpt-6.1-sol","max_tokens":256,"stream":true,"messages":[{"role":"user","content":"Reply exactly OK."}]` + tt.fields + `}`)
			translated, _ := codexclaude.ConvertClaudeRequestToCodex("gpt-6.1-sol", source, false)
			for _, bound := range []bool{false, true} {
				var out []byte
				var errApply error
				if bound {
					out, errApply = thinking.ApplyThinkingWithModelInfo(translated, source, "gpt-6.1-sol"+tt.suffix, "claude", "codex", "codex", info)
				} else {
					out, errApply = thinking.ApplyThinkingWithSourceAndSummary(translated, source, "gpt-6.1-sol"+tt.suffix, "claude", "codex", "codex", thinking.SummaryConfig{})
				}
				if errApply != nil {
					t.Fatalf("bound=%v: ApplyThinking returned error: %v", bound, errApply)
				}
				if got := gjson.GetBytes(out, "reasoning.effort").String(); got != tt.wantEffort {
					t.Fatalf("bound=%v: reasoning.effort = %q, want %q; body=%s", bound, got, tt.wantEffort, out)
				}
			}
		})
	}
}
