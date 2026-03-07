package test

import (
	"testing"

	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/codex"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/thinking/provider/openai"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/tidwall/gjson"
)

func TestLookupStaticModelInfo_GPT54SupportsReasoningNone(t *testing.T) {
	modelInfo := registry.LookupStaticModelInfo("gpt-5.4")
	if modelInfo == nil {
		t.Fatal("expected static model info for gpt-5.4")
	}
	if modelInfo.Thinking == nil {
		t.Fatal("expected thinking support for gpt-5.4")
	}
	if !thinking.HasLevel(modelInfo.Thinking.Levels, string(thinking.LevelNone)) {
		t.Fatalf("expected gpt-5.4 levels %v to include %q", modelInfo.Thinking.Levels, thinking.LevelNone)
	}
}

func TestApplyThinking_GPT54OpenAISupportsReasoningNone(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`)

	out, err := thinking.ApplyThinking(body, "gpt-5.4", "openai", "openai", "openai")
	if err != nil {
		t.Fatalf("ApplyThinking returned error: %v", err)
	}

	if got := gjson.GetBytes(out, "reasoning_effort").String(); got != "none" {
		t.Fatalf("reasoning_effort = %q, want %q, body=%s", got, "none", string(out))
	}
}

func TestApplyThinking_GPT54CodexSupportsReasoningNone(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":"hi"}],"reasoning":{"effort":"none"}}`)

	out, err := thinking.ApplyThinking(body, "gpt-5.4", "codex", "codex", "codex")
	if err != nil {
		t.Fatalf("ApplyThinking returned error: %v", err)
	}

	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "none" {
		t.Fatalf("reasoning.effort = %q, want %q, body=%s", got, "none", string(out))
	}
}
