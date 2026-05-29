package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/deepseek"
	"github.com/tidwall/gjson"
)

func TestApplyThinking_DeepSeekProviderKeyUsesThinkingExtraBody(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-deepseek-thinking-" + t.Name()
	modelID := "deepseek-v4-pro"
	reg.RegisterClient(clientID, "deepseek", []*registry.ModelInfo{{
		ID:       modelID,
		Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh", "max"}},
	}})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})

	out, err := thinking.ApplyThinking(
		[]byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"low"}`),
		"deepseek-v4-pro(max)",
		"openai",
		"openai",
		"deepseek",
	)
	if err != nil {
		t.Fatalf("ApplyThinking() error = %v", err)
	}
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "enabled" {
		t.Fatalf("thinking.type = %q, want enabled; body=%s", got, out)
	}
	if got := gjson.GetBytes(out, "reasoning_effort").String(); got != "max" {
		t.Fatalf("reasoning_effort = %q, want max; body=%s", got, out)
	}
}

func TestApplyThinking_DeepSeekNoneSuffixDisablesThinking(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-deepseek-thinking-none-" + t.Name()
	modelID := "deepseek-v4-pro"
	reg.RegisterClient(clientID, "deepseek", []*registry.ModelInfo{{
		ID:       modelID,
		Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh", "max"}},
	}})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})

	out, err := thinking.ApplyThinking(
		[]byte(`{"model":"deepseek-v4-pro","reasoning_effort":"high"}`),
		"deepseek-v4-pro(none)",
		"openai",
		"openai",
		"deepseek",
	)
	if err != nil {
		t.Fatalf("ApplyThinking() error = %v", err)
	}
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "disabled" {
		t.Fatalf("thinking.type = %q, want disabled; body=%s", got, out)
	}
	if gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort should be removed; body=%s", out)
	}
}
