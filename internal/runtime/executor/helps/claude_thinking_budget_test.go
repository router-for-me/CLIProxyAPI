package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

func TestEnforceClaudeThinkingBudgetBelowMaxTokens(t *testing.T) {
	info := &registry.ModelInfo{ID: "claude-test", Thinking: &registry.ThinkingSupport{Min: 1024, Max: 128000}}
	tests := []struct {
		name       string
		body       string
		info       *registry.ModelInfo
		wantType   string
		wantBudget int64 // -1 means absent
	}{
		{name: "valid budget unchanged", body: `{"max_tokens":8000,"thinking":{"type":"enabled","budget_tokens":4096}}`, info: info, wantType: "enabled", wantBudget: 4096},
		{name: "budget clamped", body: `{"max_tokens":8000,"thinking":{"type":"enabled","budget_tokens":24576}}`, info: info, wantType: "enabled", wantBudget: 7999},
		{name: "equal budget clamped", body: `{"max_tokens":8000,"thinking":{"type":"enabled","budget_tokens":8000}}`, info: info, wantType: "enabled", wantBudget: 7999},
		{name: "below model minimum disables", body: `{"max_tokens":1000,"thinking":{"type":"enabled","budget_tokens":24576,"display":"summarized"}}`, info: info, wantType: "disabled", wantBudget: -1},
		{name: "unknown model clamps to one", body: `{"max_tokens":2,"thinking":{"type":"enabled","budget_tokens":24576}}`, info: nil, wantType: "enabled", wantBudget: 1},
		{name: "adaptive untouched", body: `{"max_tokens":10,"thinking":{"type":"adaptive"}}`, info: info, wantType: "adaptive", wantBudget: -1},
		{name: "missing max_tokens untouched", body: `{"thinking":{"type":"enabled","budget_tokens":24576}}`, info: info, wantType: "enabled", wantBudget: 24576},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := EnforceClaudeThinkingBudgetBelowMaxTokens([]byte(tt.body), tt.info)
			if got := gjson.GetBytes(out, "thinking.type").String(); got != tt.wantType {
				t.Fatalf("thinking.type = %q, want %q; body=%s", got, tt.wantType, out)
			}
			budget := gjson.GetBytes(out, "thinking.budget_tokens")
			if tt.wantBudget < 0 {
				if budget.Exists() {
					t.Fatalf("budget_tokens should be absent; body=%s", out)
				}
			} else if budget.Int() != tt.wantBudget {
				t.Fatalf("budget_tokens = %d, want %d; body=%s", budget.Int(), tt.wantBudget, out)
			}
			if tt.wantType == "disabled" && gjson.GetBytes(out, "thinking.display").Exists() {
				t.Fatalf("display must be removed when thinking is disabled; body=%s", out)
			}
		})
	}
}
