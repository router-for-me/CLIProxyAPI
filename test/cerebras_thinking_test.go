package test

import (
	"testing"

	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/cerebras"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/openai"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCerebrasGLM_StripsReasoningEffort_FromClaudeThinkingEnabled(t *testing.T) {
	in := []byte(`{
		"model":"zai-glm-4.7",
		"thinking":{"type":"enabled","budget_tokens":24576},
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
	}`)

	translated := sdktranslator.TranslateRequest(
		sdktranslator.FromString("claude"),
		sdktranslator.FromString("openai"),
		"zai-glm-4.7",
		in,
		true,
	)
	out, err := thinking.ApplyThinking(translated, "zai-glm-4.7", "claude", "openai", "cerebras")
	if err != nil {
		t.Fatalf("unexpected error: %v, body=%s", err, string(out))
	}
	if gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("expected reasoning_effort to be stripped for GLM models, body=%s", string(out))
	}
	if gjson.GetBytes(out, "disable_reasoning").Exists() {
		t.Fatalf("expected disable_reasoning to be unset when thinking is enabled, body=%s", string(out))
	}
}

func TestCerebrasGLM_MapsNoneToDisableReasoning_FromClaudeThinkingDisabled(t *testing.T) {
	in := []byte(`{
		"model":"zai-glm-4.7",
		"thinking":{"type":"disabled"},
		"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
	}`)

	translated := sdktranslator.TranslateRequest(
		sdktranslator.FromString("claude"),
		sdktranslator.FromString("openai"),
		"zai-glm-4.7",
		in,
		true,
	)
	out, err := thinking.ApplyThinking(translated, "zai-glm-4.7", "claude", "openai", "cerebras")
	if err != nil {
		t.Fatalf("unexpected error: %v, body=%s", err, string(out))
	}
	if gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("expected reasoning_effort to be stripped for GLM models, body=%s", string(out))
	}
	if !gjson.GetBytes(out, "disable_reasoning").Bool() {
		t.Fatalf("expected disable_reasoning=true when thinking is disabled, body=%s", string(out))
	}
}

func TestCerebrasNonGLM_KeepsReasoningEffort(t *testing.T) {
	in := []byte(`{"model":"gpt-oss-120b","messages":[{"role":"user","content":"hi"}]}`)
	out, err := thinking.ApplyThinking(in, "gpt-oss-120b(high)", "openai", "openai", "cerebras")
	if err != nil {
		t.Fatalf("unexpected error: %v, body=%s", err, string(out))
	}
	if gjson.GetBytes(out, "reasoning_effort").String() != "high" {
		t.Fatalf("expected reasoning_effort=high for non-GLM models, body=%s", string(out))
	}
}

