package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/kimi"
	"github.com/tidwall/gjson"
)

// Reproduces Claude Code -> Kimi /v1/messages with effort=max.
// KimiExecutor delegates to ClaudeExecutor, so ApplyThinking sees claude/claude.
func TestKimiClaudeMessagesMaxClampsToHigh(t *testing.T) {
	models := registry.GetKimiModels()
	reg := registry.GetGlobalRegistry()
	clientID := "test-kimi-max-clamp"
	reg.RegisterClient(clientID, "kimi", models)
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	body := []byte(`{"model":"kimi-k2.5","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`)
	out, err := thinking.ApplyThinking(body, "kimi-k2.5", "claude", "claude", "claude")
	if err != nil {
		t.Fatalf("ApplyThinking returned error: %v", err)
	}
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want adaptive", got)
	}
	if got := gjson.GetBytes(out, "output_config.effort").String(); got != "high" {
		t.Fatalf("output_config.effort = %q, want high", got)
	}
}
