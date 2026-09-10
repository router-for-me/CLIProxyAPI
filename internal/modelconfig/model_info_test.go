package modelconfig

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestResolveModelInfoUsesSuffixFreeStaticCapabilities(t *testing.T) {
	info := ResolveModelInfo("claude-opus-4-6(high)", "claude", nil)
	if info == nil || info.Thinking == nil {
		t.Fatalf("ResolveModelInfo() = %+v, want inherited thinking support", info)
	}
	if info.ID != "claude-opus-4-6(high)" {
		t.Fatalf("model ID = %q, want configured upstream name", info.ID)
	}
	if info.UserDefined {
		t.Fatal("resolved capability snapshot must not be user-defined")
	}
}

func TestResolveModelInfoExplicitThinkingOverridesAndClones(t *testing.T) {
	support := &registry.ThinkingSupport{Levels: []string{" XHIGH ", "xhigh", " High "}}
	info := ResolveModelInfo("custom-model", "codex", support)
	if info == nil || info.Thinking == nil {
		t.Fatalf("ResolveModelInfo() = %+v, want explicit thinking support", info)
	}
	if got := info.Thinking.Levels; len(got) != 2 || got[0] != "xhigh" || got[1] != "high" {
		t.Fatalf("normalized levels = %v, want [xhigh high]", got)
	}
	support.Levels[0] = "low"
	if info.Thinking.Levels[0] != "xhigh" {
		t.Fatal("resolved thinking support shares mutable config storage")
	}
}

func TestNormalizeThinkingSupportDerivesSpecialLevelFlags(t *testing.T) {
	support := NormalizeThinkingSupport(&registry.ThinkingSupport{Levels: []string{"low", "none", "auto"}})
	if support == nil {
		t.Fatal("NormalizeThinkingSupport() = nil")
	}
	if !support.ZeroAllowed {
		t.Fatal("none level did not enable ZeroAllowed")
	}
	if !support.DynamicAllowed {
		t.Fatal("auto level did not enable DynamicAllowed")
	}
}

func TestResolveModelInfoUnknownModelKeepsMissingCapability(t *testing.T) {
	info := ResolveModelInfo("unknown-configured-model", "claude", nil)
	if info == nil {
		t.Fatal("ResolveModelInfo() = nil")
	}
	if info.Thinking != nil {
		t.Fatalf("unknown model thinking = %+v, want nil", info.Thinking)
	}
	// A model the bundled catalog does not carry has unknown — not proven absent —
	// reasoning capability, so the snapshot stays user-defined and the caller's
	// thinking configuration reaches the upstream for validation. See
	// model_info_unknown_thinking_test.go for the end-to-end guarantee.
	if !info.UserDefined {
		t.Fatal("unknown configured model must keep unknown capability semantics")
	}
}
