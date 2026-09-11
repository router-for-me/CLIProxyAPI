package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/kimi"
	"github.com/tidwall/gjson"
)

// TestKimiK28ClaudeMessagesMaxPreservesMax verifies that K2.8 models preserve effort=max
// because their model definition includes "max" in thinking levels.
func TestKimiK28ClaudeMessagesMaxPreservesMax(t *testing.T) {
	models := registry.GetKimiModels()
	reg := registry.GetGlobalRegistry()
	clientID := "test-kimi-k28-max"
	reg.RegisterClient(clientID, "kimi", models)
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	body := []byte(`{"model":"kimi-k2.8","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`)
	out, err := thinking.ApplyThinking(body, "kimi-k2.8", "claude", "claude", "claude")
	if err != nil {
		t.Fatalf("ApplyThinking returned error: %v", err)
	}
	if got := gjson.GetBytes(out, "output_config.effort").String(); got != "max" {
		t.Fatalf("output_config.effort = %q, want max", got)
	}
}

// Reproduces Claude Code -> Kimi /v1/messages with effort=max on K2.5 (clamps to high).
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
