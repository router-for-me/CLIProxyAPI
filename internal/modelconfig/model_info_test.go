package modelconfig

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/claude"
	"github.com/tidwall/gjson"
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

// A claude-api-key model entry with its own thinking levels must keep the catalog
// way of turning thinking off: claude-sonnet-5-5 rejects "disabled".
func TestResolveModelInfoConfiguredThinkingKeepsCatalogDisableType(t *testing.T) {
	configured := &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "none"}}
	info := ResolveModelInfo("claude-sonnet-5-5", "claude", configured)
	if info == nil || info.Thinking == nil {
		t.Fatalf("ResolveModelInfo() = %+v, want thinking support", info)
	}
	if got := info.Thinking.DisableType; got != "between_tools" {
		t.Fatalf("DisableType = %q, want catalog between_tools", got)
	}
	if got := info.Thinking.Levels; len(got) != 4 || got[3] != "none" {
		t.Fatalf("levels = %v, want configured [low medium high none]", got)
	}

	body := []byte(`{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"}}`)
	out, err := thinking.ApplyThinkingWithModelInfo(body, nil, "claude-sonnet-5-5", "claude", "claude", "claude", info)
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfo: %v", err)
	}
	if got := gjson.GetBytes(out, "thinking").Raw; got != `{"type":"between_tools"}` {
		t.Fatalf("thinking = %s, want {\"type\":\"between_tools\"}; body=%s", got, out)
	}

	explicit := ResolveModelInfo("claude-sonnet-5-5", "claude", &registry.ThinkingSupport{Levels: []string{"low"}, DisableType: "disabled"})
	if got := explicit.Thinking.DisableType; got != "disabled" {
		t.Fatalf("explicit DisableType = %q, want configured disabled", got)
	}
	if other := ResolveModelInfo("claude-opus-5", "claude", configured); other.Thinking.DisableType != "" {
		t.Fatalf("claude-opus-5 DisableType = %q, want empty", other.Thinking.DisableType)
	}
	if variant := ResolveModelInfo("claude-sonnet-5-5[1m]", "claude", configured); variant.Thinking.DisableType != "between_tools" {
		t.Fatalf("claude-sonnet-5-5[1m] DisableType = %q, want catalog between_tools", variant.Thinking.DisableType)
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
	if info.UserDefined {
		t.Fatal("unknown configured model must use its exact bound capability")
	}
}
