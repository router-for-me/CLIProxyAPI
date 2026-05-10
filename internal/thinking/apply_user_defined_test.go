package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/claude"
	"github.com/tidwall/gjson"
)

func TestApplyThinking_UserDefinedClaudePreservesAdaptiveLevel(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-user-defined-claude-" + t.Name()
	modelID := "custom-claude-4-6"
	reg.RegisterClient(clientID, "claude", []*registry.ModelInfo{{ID: modelID, UserDefined: true}})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})

	tests := []struct {
		name  string
		model string
		body  []byte
	}{
		{
			name:  "claude adaptive effort body",
			model: modelID,
			body:  []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`),
		},
		{
			name:  "suffix level",
			model: modelID + "(high)",
			body:  []byte(`{}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := thinking.ApplyThinking(tt.body, tt.model, "openai", "claude", "claude")
			if err != nil {
				t.Fatalf("ApplyThinking() error = %v", err)
			}
			if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
				t.Fatalf("thinking.type = %q, want %q, body=%s", got, "adaptive", string(out))
			}
			if got := gjson.GetBytes(out, "output_config.effort").String(); got != "high" {
				t.Fatalf("output_config.effort = %q, want %q, body=%s", got, "high", string(out))
			}
			if gjson.GetBytes(out, "thinking.budget_tokens").Exists() {
				t.Fatalf("thinking.budget_tokens should be removed, body=%s", string(out))
			}
		})
	}
}
