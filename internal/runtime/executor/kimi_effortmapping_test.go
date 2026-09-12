package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// TestKimiExecutorAppliesKimiScopedEffortMapping proves a kimi-scoped effort
// mapping is applied for a Kimi executor. Adapted from the upstream feature test
// to v7.2.147's KimiExecutor, which reaches thinking through the shared helps
// entry points; the executor's own config carries the opt-in mapping policy.
func TestKimiExecutorAppliesKimiScopedEffortMapping(t *testing.T) {
	models := registry.GetKimiModels()
	reg := registry.GetGlobalRegistry()
	clientID := "test-kimi-scoped-effort-mapping"
	reg.RegisterClient(clientID, "kimi", models)
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	cfg := &config.Config{Thinking: config.ThinkingPolicyConfig{
		EffortMapping: []config.ThinkingEffortMappingRule{
			{
				From:           "max",
				To:             "low",
				TargetProtocol: "claude",
				TargetProvider: "kimi",
				Models:         []string{"kimi-*"},
			},
		},
	}}
	executor := NewKimiExecutor(cfg)
	body := []byte(`{"model":"kimi-k2.5","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`)

	out, err := helps.ApplyThinking(
		executor.cfg,
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")},
		body,
		"kimi-k2.5",
		"claude",
		"claude",
		executor.Identifier(),
	)
	if err != nil {
		t.Fatalf("ApplyThinking() error = %v", err)
	}
	if got := gjson.GetBytes(out, "output_config.effort").String(); got != "low" {
		t.Fatalf("output_config.effort = %q, want low; body = %s", got, out)
	}
}
