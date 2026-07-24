package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/codex"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/gemini"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/interactions"
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
		{"xhigh stays xhigh", "xhigh", []string{"high", "max", "xhigh"}, "xhigh"},
		{"xhigh prefers max", "xhigh", []string{"high", "max"}, "max"},
		{"xhigh falls back to high", "xhigh", []string{"high"}, "high"},
		{"max falls back to xhigh", "max", []string{"high", "xhigh"}, "xhigh"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			modelInfo := &registry.ModelInfo{
				ID:       "tenant/claude-upstream",
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

func TestApplyThinkingWithModelInfoClampsUnsupportedCrossFamilyHighIntent(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:       "tenant/claude-upstream",
		Type:     "claude",
		Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium"}},
	}
	body := []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}}`)
	source := []byte(`{"reasoning_effort":"xhigh"}`)
	out, err := thinking.ApplyThinkingWithModelInfo(body, source, "claude-upstream", "openai", "claude", "claude", modelInfo)
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfo() error = %v", err)
	}
	if got := gjson.GetBytes(out, "output_config.effort").String(); got != "medium" {
		t.Fatalf("output effort = %q, want medium; body=%s", got, out)
	}
}

func TestApplyThinkingWithModelInfoKeepsSameFamilyValidationStrict(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:       "tenant/openai-upstream",
		Type:     "openai",
		Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}},
	}
	body := []byte(`{"reasoning_effort":"xhigh"}`)
	if _, err := thinking.ApplyThinkingWithModelInfo(body, body, "openai-upstream", "openai", "openai", "openai", modelInfo); err == nil {
		t.Fatal("ApplyThinkingWithModelInfo() error = nil, want unsupported xhigh error")
	}
}

func TestApplyThinkingWithModelInfoUsesOriginalResponsesEffort(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:       "tenant/claude-upstream",
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

func TestApplyThinkingWithModelInfoUsesResponsesTargetApplier(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:       "tenant/codex-upstream",
		Type:     "codex",
		Thinking: &registry.ThinkingSupport{Levels: []string{"high", "xhigh"}},
	}
	body := []byte(`{"reasoning":{"effort":"high"}}`)
	source := []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`)
	out, err := thinking.ApplyThinkingWithModelInfo(body, source, "codex-upstream", "claude", "openai-response", "codex", modelInfo)
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfo() error = %v", err)
	}
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "xhigh" {
		t.Fatalf("reasoning effort = %q, want xhigh; body=%s", got, out)
	}
}

func TestApplyThinkingWithModelInfoCrossFormatMatrix(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		source     string
		from       string
		to         string
		modelType  string
		supported  []string
		resultPath string
		want       string
	}{
		{
			name: "claude to openai", body: `{"reasoning_effort":"low"}`,
			source: `{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`,
			from:   "claude", to: "openai", modelType: "openai", supported: []string{"high", "xhigh"},
			resultPath: "reasoning_effort", want: "xhigh",
		},
		{
			name: "gemini to interactions", body: `{"generation_config":{"thinking_level":"low"}}`,
			source: `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"xhigh"}}}`,
			from:   "gemini", to: "interactions", modelType: "interactions", supported: []string{"high", "max"},
			resultPath: "generation_config.thinking_level", want: "max",
		},
		{
			name: "interactions to claude", body: `{"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}}`,
			source: `{"generation_config":{"thinking_level":"xhigh"}}`,
			from:   "interactions", to: "claude", modelType: "claude", supported: []string{"high"},
			resultPath: "output_config.effort", want: "high",
		},
		{
			name: "openai to gemini", body: `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"low"}}}`,
			source: `{"reasoning_effort":"xhigh"}`,
			from:   "openai", to: "gemini", modelType: "gemini", supported: []string{"high"},
			resultPath: "generationConfig.thinkingConfig.thinkingLevel", want: "high",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			modelInfo := &registry.ModelInfo{ID: "upstream", Type: tc.modelType, Thinking: &registry.ThinkingSupport{Levels: tc.supported}}
			out, err := thinking.ApplyThinkingWithModelInfo([]byte(tc.body), []byte(tc.source), "upstream", tc.from, tc.to, tc.to, modelInfo)
			if err != nil {
				t.Fatalf("ApplyThinkingWithModelInfo() error = %v", err)
			}
			if got := gjson.GetBytes(out, tc.resultPath).String(); got != tc.want {
				t.Fatalf("%s = %q, want %q; body=%s", tc.resultPath, got, tc.want, out)
			}
		})
	}
}
