package codex

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
)

func TestApplyUserDefinedMapsMinimalToLow(t *testing.T) {
	applier := NewApplier()
	modelInfo := &registry.ModelInfo{
		ID:          "custom-codex-model",
		UserDefined: true,
	}

	out, err := applier.Apply([]byte(`{"input":"hello"}`), thinking.ThinkingConfig{
		Mode:  thinking.ModeLevel,
		Level: thinking.LevelMinimal,
	}, modelInfo)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "low" {
		t.Fatalf("reasoning.effort = %q, want low; body=%s", got, string(out))
	}
}
