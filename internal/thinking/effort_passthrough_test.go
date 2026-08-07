package thinking_test

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/claude"
)

// TestUserDefinedClaudeEffortPassthrough reproduces GitHub issue #4796:
// For user-defined models routed through a Claude-protocol upstream, the
// output_config.effort field is stripped and the request is forced into
// thinking.type=enabled + thinking.budget_tokens.
func TestUserDefinedClaudeEffortPassthrough(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		wantType       string // expected thinking.type
		wantEffort     string // expected output_config.effort ("" means should not exist)
		wantNoBudget   bool   // true if budget_tokens should NOT exist
	}{
		{
			name:         "Case 6: client sends adaptive + effort=max",
			body:         `{"model":"glm-5.2","thinking":{"type":"adaptive"},"output_config":{"effort":"max"},"messages":[{"role":"user","content":"hi"}]}`,
			wantType:     "adaptive",
			wantEffort:   "max",
			wantNoBudget: true,
		},
		{
			name:         "Case 5: client sends empty thinking + effort=max",
			body:         `{"model":"glm-5.2","thinking":{},"output_config":{"effort":"max"},"messages":[{"role":"user","content":"hi"}]}`,
			wantEffort:   "max",
		},
		{
			name:         "Case 3: client sends enabled+budget, body also has effort=max",
			body:         `{"model":"glm-5.2","thinking":{"type":"enabled","budget_tokens":1024},"output_config":{"effort":"max"},"messages":[{"role":"user","content":"hi"}]}`,
			wantType:     "enabled",
			wantEffort:   "",
			wantNoBudget: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := thinking.ApplyThinking([]byte(tt.body), "glm-5.2", "claude", "claude", "claude")
			if err != nil {
				t.Fatalf("thinking.ApplyThinking error: %v", err)
			}

			var out map[string]any
			if err := json.Unmarshal(result, &out); err != nil {
				t.Fatalf("Unmarshal error: %v", err)
			}

			pretty, _ := json.MarshalIndent(out, "", "  ")
			t.Logf("Output:\n%s", string(pretty))
		})
	}
}

func TestConvertBudgetToLevelMax(t *testing.T) {
	level, ok := thinking.ConvertBudgetToLevel(128000)
	if !ok {
		t.Fatal("ConvertBudgetToLevel(128000) returned ok=false")
	}
	t.Logf("ConvertBudgetToLevel(128000) = %s", level)
	if level != "max" {
		t.Errorf("ConvertBudgetToLevel(128000) = %q, want %q", level, "max")
	}
}
