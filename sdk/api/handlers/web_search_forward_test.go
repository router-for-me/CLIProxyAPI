package handlers

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestHasWebSearchTool(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"basic 20250305", `{"tools":[{"type":"web_search_20250305","name":"web_search"}]}`, true},
		{"20260209", `{"tools":[{"type":"web_search_20260209","name":"web_search"}]}`, true},
		{"future dated version", `{"tools":[{"type":"web_search_20991231","name":"web_search"}]}`, true},
		{"openai function tool", `{"tools":[{"type":"function","function":{"name":"foo"}}]}`, false},
		{"empty type", `{"tools":[{"type":"","name":"x"}]}`, false},
		{"missing tools field", `{"model":"glm"}`, false},
		{"empty tools array", `{"tools":[]}`, false},
		{"mixed function and web_search", `{"tools":[{"type":"function","function":{"name":"foo"}},{"type":"web_search_20250305","name":"web_search"}]}`, true},
		{"tool without type field", `{"tools":[{"name":"web_search"}]}`, false},
		{"invalid json", `{"tools":[`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasWebSearchTool([]byte(tt.body)); got != tt.want {
				t.Errorf("hasWebSearchTool() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWebSearchForwardTarget(t *testing.T) {
	wsBody := `{"tools":[{"type":"web_search_20250305","name":"web_search"}]}`
	noToolBody := `{"tools":[{"type":"function","function":{"name":"foo"}}]}`

	tests := []struct {
		name      string
		enable    bool
		model     string
		requested string
		body      string
		want      string
	}{
		{"forwards when enabled with tool", true, "deepseek", "glm", wsBody, "deepseek"},
		{"disabled returns empty", false, "deepseek", "glm", wsBody, ""},
		{"empty target model returns empty", true, "  ", "glm", wsBody, ""},
		{"no web_search tool returns empty", true, "deepseek", "glm", noToolBody, ""},
		{"self-forward guard same model", true, "glm", "glm", wsBody, ""},
		{"self-forward guard case-insensitive", true, "DeepSeek", "deepseek", wsBody, ""},
		{"requested model trimmed before compare", true, "deepseek", " glm ", wsBody, "deepseek"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &BaseAPIHandler{Cfg: &config.SDKConfig{}}
			h.Cfg.WebSearchForward.Enable = tt.enable
			h.Cfg.WebSearchForward.Model = tt.model
			if got := h.webSearchForwardTarget(tt.requested, []byte(tt.body)); got != tt.want {
				t.Errorf("webSearchForwardTarget() = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("nil cfg returns empty", func(t *testing.T) {
		h := &BaseAPIHandler{}
		if got := h.webSearchForwardTarget("glm", []byte(wsBody)); got != "" {
			t.Errorf("webSearchForwardTarget() with nil Cfg = %q, want empty", got)
		}
	})
}
