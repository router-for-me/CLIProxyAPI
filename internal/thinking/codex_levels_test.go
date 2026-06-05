package thinking_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/codex"
	"github.com/tidwall/gjson"
)

func TestApplyThinkingCodexMapsMinimalToLowWhenUnsupported(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := fmt.Sprintf("codex-minimal-known-%d", time.Now().UnixNano())
	modelID := "codex-minimal-known-model"
	reg.RegisterClient(clientID, "codex", []*registry.ModelInfo{{
		ID:       modelID,
		Type:     "openai",
		Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh"}},
	}})
	defer reg.UnregisterClient(clientID)

	out, err := thinking.ApplyThinking([]byte(`{"reasoning":{"effort":"minimal"}}`), modelID, "codex", "codex", "codex")
	if err != nil {
		t.Fatalf("ApplyThinking() error = %v", err)
	}
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "low" {
		t.Fatalf("reasoning.effort = %q, want low; body=%s", got, string(out))
	}
}

func TestApplyThinkingCodexMapsMinimalForUserDefinedModel(t *testing.T) {
	out, err := thinking.ApplyThinking([]byte(`{"reasoning":{"effort":"minimal"}}`), "unknown-codex-model", "codex", "codex", "codex")
	if err != nil {
		t.Fatalf("ApplyThinking() error = %v", err)
	}
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "low" {
		t.Fatalf("reasoning.effort = %q, want low; body=%s", got, string(out))
	}
}

func TestApplyThinkingCodexPreservesAdvertisedMinimal(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := fmt.Sprintf("codex-minimal-preserve-%d", time.Now().UnixNano())
	modelID := "codex-minimal-preserve-model"
	reg.RegisterClient(clientID, "codex", []*registry.ModelInfo{{
		ID:       modelID,
		Type:     "openai",
		Thinking: &registry.ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high"}},
	}})
	defer reg.UnregisterClient(clientID)

	out, err := thinking.ApplyThinking([]byte(`{"reasoning":{"effort":"minimal"}}`), modelID, "codex", "codex", "codex")
	if err != nil {
		t.Fatalf("ApplyThinking() error = %v", err)
	}
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "minimal" {
		t.Fatalf("reasoning.effort = %q, want minimal; body=%s", got, string(out))
	}
}
