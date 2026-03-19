package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/tidwall/gjson"
)

func TestApplyPayloadConfigWithRoot_MatchesOriginalRequestedModelAlias(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gpt-5.4-high-fast", Protocol: "codex"},
					},
					Params: map[string]any{
						"service_tier": "priority",
					},
				},
			},
		},
	}

	out := applyPayloadConfigWithRoot(
		cfg,
		"gpt-5.4",
		"codex",
		"",
		[]byte(`{"model":"gpt-5.4"}`),
		nil,
		"gpt-5.4(high)",
		"gpt-5.4-high-fast",
	)

	if got := gjson.GetBytes(out, "service_tier").String(); got != "priority" {
		t.Fatalf("service_tier = %q, want %q", got, "priority")
	}
}

func TestApplyPayloadConfigWithRoot_PreservesNormalizedRequestedModelMatching(t *testing.T) {
	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gpt-5.4(high)", Protocol: "codex"},
					},
					Params: map[string]any{
						"reasoning.summary": "detailed",
					},
				},
			},
		},
	}

	out := applyPayloadConfigWithRoot(
		cfg,
		"gpt-5.4",
		"codex",
		"",
		[]byte(`{"model":"gpt-5.4"}`),
		nil,
		"gpt-5.4(high)",
		"gpt-5.4-high-fast",
	)

	if got := gjson.GetBytes(out, "reasoning.summary").String(); got != "detailed" {
		t.Fatalf("reasoning.summary = %q, want %q", got, "detailed")
	}
}
