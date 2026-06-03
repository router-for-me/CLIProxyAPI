package opencode

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestNewModelMapper(t *testing.T) {
	mapper := NewModelMapper([]config.OpenCodeModelMapping{
		{From: "claude-sonnet-4-5", To: "gpt-5"},
		{From: "gpt-5-codex", To: "gemini-2.5-pro"},
	})
	if mapper == nil {
		t.Fatal("expected non-nil mapper")
	}
	if got := len(mapper.GetMappings()); got != 2 {
		t.Errorf("expected 2 mappings, got %d", got)
	}
}

func TestModelMapper_MapModel_NoProvider(t *testing.T) {
	mapper := NewModelMapper([]config.OpenCodeModelMapping{
		{From: "claude-sonnet-4-5", To: "model-without-provider"},
	})
	if got := mapper.MapModel("claude-sonnet-4-5"); got != "" {
		t.Errorf("expected empty result when target has no provider, got %q", got)
	}
}

func TestModelMapper_MapModel_WithProvider(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("opencode-test-client", "claude", []*registry.ModelInfo{
		{ID: "claude-sonnet-4", OwnedBy: "anthropic", Type: "claude"},
	})
	defer reg.UnregisterClient("opencode-test-client")

	mapper := NewModelMapper([]config.OpenCodeModelMapping{
		{From: "claude-sonnet-4-5", To: "claude-sonnet-4"},
	})
	if got := mapper.MapModel("claude-sonnet-4-5"); got != "claude-sonnet-4" {
		t.Errorf("expected claude-sonnet-4, got %q", got)
	}
}

func TestModelMapper_MapModel_CaseInsensitive(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("opencode-test-client-ci", "claude", []*registry.ModelInfo{
		{ID: "claude-sonnet-4", OwnedBy: "anthropic", Type: "claude"},
	})
	defer reg.UnregisterClient("opencode-test-client-ci")

	mapper := NewModelMapper([]config.OpenCodeModelMapping{
		{From: "Claude-Sonnet-4-5", To: "claude-sonnet-4"},
	})
	if got := mapper.MapModel("claude-sonnet-4-5"); got != "claude-sonnet-4" {
		t.Errorf("expected claude-sonnet-4, got %q", got)
	}
}

func TestModelMapper_MapModel_Regex(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("opencode-test-client-rx", "claude", []*registry.ModelInfo{
		{ID: "claude-sonnet-4", OwnedBy: "anthropic", Type: "claude"},
	})
	defer reg.UnregisterClient("opencode-test-client-rx")

	mapper := NewModelMapper([]config.OpenCodeModelMapping{
		{From: "^claude-.*-4-5$", To: "claude-sonnet-4", Regex: true},
	})
	if got := mapper.MapModel("claude-opus-4-5"); got != "claude-sonnet-4" {
		t.Errorf("expected claude-sonnet-4 via regex, got %q", got)
	}
}

func TestModelMapper_MapModel_PreservesThinkingSuffix(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("opencode-test-client-suffix", "codex", []*registry.ModelInfo{
		{ID: "gpt-5.2", OwnedBy: "openai", Type: "codex"},
	})
	defer reg.UnregisterClient("opencode-test-client-suffix")

	mapper := NewModelMapper([]config.OpenCodeModelMapping{
		{From: "gpt-5-codex", To: "gpt-5.2"},
	})
	if got := mapper.MapModel("gpt-5-codex(xhigh)"); got != "gpt-5.2(xhigh)" {
		t.Errorf("expected gpt-5.2(xhigh), got %q", got)
	}
}

func TestModelMapper_MapModel_NotFound(t *testing.T) {
	mapper := NewModelMapper([]config.OpenCodeModelMapping{
		{From: "claude-sonnet-4-5", To: "gpt-5"},
	})
	if got := mapper.MapModel("unknown-model"); got != "" {
		t.Errorf("expected empty result for unknown model, got %q", got)
	}
}

func TestModelMapper_UpdateMappings_HotReload(t *testing.T) {
	mapper := NewModelMapper(nil)
	if got := len(mapper.GetMappings()); got != 0 {
		t.Fatalf("expected 0 initial mappings, got %d", got)
	}
	mapper.UpdateMappings([]config.OpenCodeModelMapping{
		{From: "a", To: "b"},
		{From: "c", To: "d"},
	})
	if got := len(mapper.GetMappings()); got != 2 {
		t.Errorf("expected 2 mappings after reload, got %d", got)
	}
}
