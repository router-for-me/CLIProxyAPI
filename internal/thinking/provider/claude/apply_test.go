package claude_test

import (
	"fmt"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/claude"
)

func TestApplierDisablesThinkingWhenMaxTokensCannotFitMinimumBudget(t *testing.T) {
	applier := thinking.GetProviderApplier("claude")
	if applier == nil {
		t.Fatal("claude provider applier is not registered")
	}
	modelInfo := &registry.ModelInfo{
		ID:                  "claude-test",
		Type:                "claude",
		MaxCompletionTokens: 64000,
		Thinking: &registry.ThinkingSupport{
			Min:         1024,
			Max:         64000,
			ZeroAllowed: true,
		},
	}

	for _, tc := range []struct {
		name      string
		maxTokens int
	}{
		{name: "max equals minimum", maxTokens: 1024},
		{name: "max below minimum", maxTokens: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"max_tokens":%d}`, tc.maxTokens))
			out, errApply := applier.Apply(body, thinking.ThinkingConfig{
				Mode:   thinking.ModeBudget,
				Budget: 2048,
			}, modelInfo)
			if errApply != nil {
				t.Fatalf("Apply() error = %v", errApply)
			}
			if got := ojson.GetBytes(out, "thinking.type").String(); got != "disabled" {
				t.Fatalf("thinking.type = %q, want disabled; body=%s", got, out)
			}
			if budget := ojson.GetBytes(out, "thinking.budget_tokens"); budget.Exists() {
				t.Fatalf("thinking.budget_tokens remains after disabling thinking: %s", budget.Raw)
			}
		})
	}
}
