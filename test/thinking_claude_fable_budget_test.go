package test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// Claude Fable 5 and Fable 5.1 reject thinking.type="enabled" with budget_tokens.
// Budget and auto requests must map to adaptive thinking instead.
func TestThinkingE2EClaudeFableBudget(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	uid := fmt.Sprintf("thinking-e2e-claude-fable-budget-%d", time.Now().UnixNano())

	var fableModels []*registry.ModelInfo
	for _, m := range registry.GetClaudeModels() {
		if strings.HasPrefix(m.ID, "claude-fable-5") {
			fableModels = append(fableModels, m)
		}
	}
	if len(fableModels) < 2 {
		t.Fatalf("expected claude-fable-5 and claude-fable-5-1 in the embedded registry, got %d models", len(fableModels))
	}
	reg.RegisterClient(uid, "test", fableModels)
	defer reg.UnregisterClient(uid)

	adaptive := func(name, from, model, input, effort string) thinkingTestCase {
		tc := thinkingTestCase{
			name:         name,
			from:         from,
			to:           "claude",
			model:        model,
			inputJSON:    input,
			expectField:  "thinking.type",
			expectValue:  "adaptive",
			expectAbsent: []string{"thinking.budget_tokens"},
		}
		if effort != "" {
			tc.expectField2 = "output_config.effort"
			tc.expectValue2 = effort
		} else {
			tc.expectAbsent = append(tc.expectAbsent, "output_config.effort")
		}
		return tc
	}

	var cases []thinkingTestCase
	for _, model := range []string{"claude-fable-5", "claude-fable-5-1"} {
		cases = append(cases,
			adaptive(model+"-claude-budget", "claude", model,
				`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":16384}}`, "high"),
			adaptive(model+"-gemini-budget", "gemini", model,
				`{"model":"`+model+`","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":1024}}}`, "low"),
			adaptive(model+"-suffix-budget", "claude", model+"(64000)",
				`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`, "xhigh"),
			adaptive(model+"-openai-effort", "openai", model,
				`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"medium"}`, "medium"),
			// Auto keeps the upstream default effort instead of a mid-range budget.
			adaptive(model+"-suffix-auto", "claude", model+"(auto)",
				`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`, ""),
		)
	}

	runThinkingTests(t, cases)
}
