package thinking_test

import (
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/kimi"
	"github.com/tidwall/gjson"
)

// Reproduces Claude Code -> Kimi k3[1m] /v1/messages with an adaptive effort.
// Kimi's K3 guidance maps medium/xhigh upward (medium->high, xhigh->max);
// the generic nearest-level clamp would break ties downward (medium->low,
// xhigh->high), silently weakening the requested reasoning mode.
func TestKimiK31MClaudeEffortMapsUpward(t *testing.T) {
	models := registry.GetKimiModels()
	reg := registry.GetGlobalRegistry()
	clientID := "test-kimi-k3-1m-mapping"
	reg.RegisterClient(clientID, "kimi", models)
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	cases := map[string]string{
		"medium": "high",
		"xhigh":  "max",
		"low":    "low",
		"high":   "high",
		"max":    "max",
	}
	for effort, want := range cases {
		t.Run(fmt.Sprintf("effort_%s", effort), func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":"k3[1m]","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"adaptive"},"output_config":{"effort":%q}}`, effort))
			out, err := thinking.ApplyThinking(body, "k3[1m]", "claude", "claude", "claude")
			if err != nil {
				t.Fatalf("ApplyThinking returned error: %v", err)
			}
			if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
				t.Fatalf("thinking.type = %q, want adaptive", got)
			}
			if got := gjson.GetBytes(out, "output_config.effort").String(); got != want {
				t.Fatalf("output_config.effort = %q, want %q (request effort %q)", got, want, effort)
			}
		})
	}
}

// A manual thinking budget that derives "medium" (8192 tokens) must follow the
// same upward mapping on the Claude-compatible path.
func TestKimiK31MClaudeBudgetMapsUpward(t *testing.T) {
	models := registry.GetKimiModels()
	reg := registry.GetGlobalRegistry()
	clientID := "test-kimi-k3-1m-budget"
	reg.RegisterClient(clientID, "kimi", models)
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	body := []byte(`{"model":"k3[1m]","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":8192}}`)
	out, err := thinking.ApplyThinking(body, "k3[1m]", "claude", "claude", "claude")
	if err != nil {
		t.Fatalf("ApplyThinking returned error: %v", err)
	}
	if got := gjson.GetBytes(out, "output_config.effort").String(); got != "high" {
		t.Fatalf("output_config.effort = %q, want high (budget 8192 -> medium -> high)", got)
	}
}

// On the strict same-family path (native Kimi format), a mapped level must
// resolve through LevelMapping instead of failing with ErrLevelNotSupported.
func TestKimiK31MNativeStrictPathMapsInsteadOfError(t *testing.T) {
	var modelInfo *registry.ModelInfo
	for _, model := range registry.GetKimiModels() {
		if model != nil && model.ID == "k3[1m]" {
			modelInfo = model
			break
		}
	}
	if modelInfo == nil {
		t.Fatal(`expected GetKimiModels to include builtin model k3[1m]`)
	}

	validated, err := thinking.ValidateConfig(
		thinking.ThinkingConfig{Mode: thinking.ModeLevel, Level: thinking.LevelMedium},
		modelInfo, "kimi", "kimi", false)
	if err != nil {
		t.Fatalf("ValidateConfig returned error: %v", err)
	}
	if validated.Level != thinking.LevelHigh {
		t.Fatalf("level = %q, want %q", validated.Level, thinking.LevelHigh)
	}
}
