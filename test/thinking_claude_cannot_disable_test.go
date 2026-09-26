package test

import (
	"fmt"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// Claude Opus 5.5 and Fable 5.1 reject thinking.type="disabled" with a 400 error.
// Requests that ask for no thinking must fall back to the lowest adaptive effort.
func TestThinkingE2EClaudeCannotDisable(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	uid := fmt.Sprintf("thinking-e2e-claude-cannot-disable-%d", time.Now().UnixNano())

	levels := []string{"low", "medium", "high", "xhigh", "max"}
	reg.RegisterClient(uid, "test", []*registry.ModelInfo{
		{
			ID:                  "claude-level-cannot-disable-model",
			Object:              "model",
			Created:             1790035200,
			OwnedBy:             "anthropic",
			Type:                "claude",
			DisplayName:         "Claude Level Cannot Disable",
			MaxCompletionTokens: 128000,
			Thinking:            &registry.ThinkingSupport{ZeroAllowed: false, DynamicAllowed: true, Levels: levels},
		},
		{
			ID:                  "claude-hybrid-cannot-disable-model",
			Object:              "model",
			Created:             1788220800,
			OwnedBy:             "anthropic",
			Type:                "claude",
			DisplayName:         "Claude Hybrid Cannot Disable",
			MaxCompletionTokens: 128000,
			Thinking:            &registry.ThinkingSupport{Min: 1024, Max: 128000, ZeroAllowed: false, Levels: levels},
		},
		{
			ID:                  "claude-level-can-disable-model",
			Object:              "model",
			Created:             1784038800,
			OwnedBy:             "anthropic",
			Type:                "claude",
			DisplayName:         "Claude Level Can Disable",
			MaxCompletionTokens: 128000,
			Thinking:            &registry.ThinkingSupport{ZeroAllowed: true, DynamicAllowed: true, Levels: levels},
		},
	})
	defer reg.UnregisterClient(uid)

	lowAdaptive := func(name, from, model, input string) thinkingTestCase {
		return thinkingTestCase{
			name:         name,
			from:         from,
			to:           "claude",
			model:        model,
			inputJSON:    input,
			expectField:  "thinking.type",
			expectValue:  "adaptive",
			expectField2: "output_config.effort",
			expectValue2: "low",
			expectAbsent: []string{"thinking.budget_tokens"},
		}
	}

	cases := []thinkingTestCase{
		lowAdaptive("1", "openai", "claude-level-cannot-disable-model",
			`{"model":"claude-level-cannot-disable-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`),
		lowAdaptive("2", "claude", "claude-level-cannot-disable-model",
			`{"model":"claude-level-cannot-disable-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"}}`),
		lowAdaptive("3", "claude", "claude-level-cannot-disable-model(none)",
			`{"model":"claude-level-cannot-disable-model","messages":[{"role":"user","content":"hi"}]}`),
		lowAdaptive("4", "gemini", "claude-level-cannot-disable-model",
			`{"model":"claude-level-cannot-disable-model","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":0}}}`),
		lowAdaptive("5", "openai", "claude-hybrid-cannot-disable-model",
			`{"model":"claude-hybrid-cannot-disable-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`),
		lowAdaptive("6", "claude", "claude-hybrid-cannot-disable-model",
			`{"model":"claude-hybrid-cannot-disable-model","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"}}`),
		// Models that allow disabling keep the explicit disabled type.
		{
			name:         "7",
			from:         "openai",
			to:           "claude",
			model:        "claude-level-can-disable-model",
			inputJSON:    `{"model":"claude-level-can-disable-model","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`,
			expectField:  "thinking.type",
			expectValue:  "disabled",
			expectAbsent: []string{"output_config.effort"},
		},
	}

	runThinkingTests(t, cases)
}
