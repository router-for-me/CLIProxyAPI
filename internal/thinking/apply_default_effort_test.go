package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/codex"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/gemini"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/openai"
	"github.com/tidwall/gjson"
)

func TestApplyThinking_DefaultReasoningEffortOnMissing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		body          []byte
		model         string
		fromFormat    string
		toFormat      string
		providerKey   string
		defaultEffort string
		assertPath    string
		want          string
	}{
		{
			name:          "openai missing applies default",
			body:          []byte(`{"model":"test-openai"}`),
			model:         "test-openai",
			fromFormat:    "openai",
			toFormat:      "openai",
			providerKey:   "openai",
			defaultEffort: "high",
			assertPath:    "reasoning_effort",
			want:          "high",
		},
		{
			name:          "openai empty applies default",
			body:          []byte(`{"model":"test-openai","reasoning_effort":"   "}`),
			model:         "test-openai",
			fromFormat:    "openai",
			toFormat:      "openai",
			providerKey:   "openai",
			defaultEffort: "high",
			assertPath:    "reasoning_effort",
			want:          "high",
		},
		{
			name:          "openai explicit non-empty keeps request value",
			body:          []byte(`{"model":"test-openai","reasoning_effort":"low"}`),
			model:         "test-openai",
			fromFormat:    "openai",
			toFormat:      "openai",
			providerKey:   "openai",
			defaultEffort: "high",
			assertPath:    "reasoning_effort",
			want:          "low",
		},
		{
			name:          "suffix has highest priority",
			body:          []byte(`{"model":"test-openai(xhigh)"}`),
			model:         "test-openai(xhigh)",
			fromFormat:    "openai",
			toFormat:      "openai",
			providerKey:   "openai",
			defaultEffort: "low",
			assertPath:    "reasoning_effort",
			want:          "xhigh",
		},
		{
			name:          "codex missing applies default",
			body:          []byte(`{"model":"test-codex"}`),
			model:         "test-codex",
			fromFormat:    "openai",
			toFormat:      "codex",
			providerKey:   "codex",
			defaultEffort: "medium",
			assertPath:    "reasoning.effort",
			want:          "medium",
		},
		{
			name:          "claude missing applies default effort",
			body:          []byte(`{"model":"test-claude"}`),
			model:         "test-claude",
			fromFormat:    "openai",
			toFormat:      "claude",
			providerKey:   "claude",
			defaultEffort: "high",
			assertPath:    "output_config.effort",
			want:          "high",
		},
		{
			name:          "gemini missing applies default level",
			body:          []byte(`{"model":"test-gemini"}`),
			model:         "test-gemini",
			fromFormat:    "openai",
			toFormat:      "gemini",
			providerKey:   "gemini",
			defaultEffort: "high",
			assertPath:    "generationConfig.thinkingConfig.thinkingBudget",
			want:          "24576",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			out, err := thinking.ApplyThinking(tt.body, tt.model, tt.fromFormat, tt.toFormat, tt.providerKey, tt.defaultEffort)
			if err != nil {
				t.Fatalf("ApplyThinking() error = %v", err)
			}
			got := gjson.GetBytes(out, tt.assertPath).String()
			if got != tt.want {
				t.Fatalf("path %q = %q, want %q, body=%s", tt.assertPath, got, tt.want, string(out))
			}
		})
	}
}
