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
