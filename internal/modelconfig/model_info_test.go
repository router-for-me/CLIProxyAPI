package modelconfig

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestResolveModelInfoUsesSuffixFreeStaticCapabilities(t *testing.T) {
	static := registry.LookupStaticModelInfo("gpt-5.6-luna")
	if static == nil || static.Thinking == nil {
		t.Fatal("gpt-5.6-luna static thinking metadata is unavailable")
	}

	info := ResolveModelInfo("gpt-5.6-luna(high)", "openai", nil)
	if info.ID != "gpt-5.6-luna(high)" {
		t.Fatalf("model ID = %q, want configured name", info.ID)
	}
	if info.Thinking == nil || len(info.Thinking.Levels) != len(static.Thinking.Levels) {
		t.Fatalf("thinking = %+v, want static capabilities %+v", info.Thinking, static.Thinking)
	}
}

func TestResolveModelInfoNormalizesConfiguredCapabilities(t *testing.T) {
	info := ResolveModelInfo("custom", "claude", &registry.ThinkingSupport{
		Levels: []string{" XHIGH ", "xhigh", "none", "AUTO"},
	})
	if got, want := len(info.Thinking.Levels), 3; got != want {
		t.Fatalf("thinking levels = %v, want %d unique levels", info.Thinking.Levels, want)
	}
	if !info.Thinking.ZeroAllowed || !info.Thinking.DynamicAllowed {
		t.Fatalf("thinking flags = %+v, want none/auto flags", info.Thinking)
	}
	if got := NormalizeModalities([]string{" TEXT ", "image", "text"}); len(got) != 2 || got[0] != "text" || got[1] != "image" {
		t.Fatalf("modalities = %v, want [text image]", got)
	}
}
