package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/kimi"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// Reproduces Claude Code -> Kimi /v1/messages with effort=max through
// Kimi's OpenAI-compatible chat completions path.
func TestKimiClaudeMessagesMaxClampsToHigh(t *testing.T) {
	models := registry.GetKimiModels()
	reg := registry.GetGlobalRegistry()
	clientID := "test-kimi-max-clamp"
	reg.RegisterClient(clientID, "kimi", models)
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	body := []byte(`{"model":"kimi-k2.5","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`)
	body = sdktranslator.TranslateRequest(sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, "kimi-k2.5", body, false)
	out, err := thinking.ApplyThinking(body, "kimi-k2.5", "claude", "kimi", "kimi")
	if err != nil {
		t.Fatalf("ApplyThinking returned error: %v", err)
	}
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "enabled" {
		t.Fatalf("thinking.type = %q, want enabled", got)
	}
	if got := gjson.GetBytes(out, "thinking.effort").String(); got != "high" {
		t.Fatalf("thinking.effort = %q, want high", got)
	}
	if gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatal("reasoning_effort should be removed from the Kimi payload")
	}
}
