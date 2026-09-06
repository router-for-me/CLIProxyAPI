package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestApplyThinkingUsesConfiguredMappingAndRequestedAlias(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Thinking: config.ThinkingPolicyConfig{
			EffortMapping: []config.ThinkingEffortMappingRule{{
				From:           "max",
				To:             "ultra",
				SourceProtocol: "claude",
				TargetProtocol: "codex",
				TargetProvider: "codex",
				Models:         []string{"coding-*"},
			}},
		},
	}
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "coding-alias(max)",
		},
	}

	got, err := ApplyThinking(cfg, opts, []byte(`{"reasoning":{"effort":"max"}}`), "gpt-5.5", "claude", "codex", "codex")
	if err != nil {
		t.Fatalf("ApplyThinking() error = %v", err)
	}
	if effort := gjson.GetBytes(got, "reasoning.effort").String(); effort != "ultra" {
		t.Fatalf("reasoning.effort = %q, want ultra; body = %s", effort, got)
	}
}
