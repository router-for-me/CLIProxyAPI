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

func TestResolveModelInfoDoesNotLeakStaticImageAPIToOtherProviders(t *testing.T) {
	static := registry.LookupStaticModelInfo("gpt-image-2")
	if static == nil || !static.SupportsImageAPI {
		t.Fatal("gpt-image-2 static image capability is unavailable")
	}

	for _, modelType := range []string{"claude", "gemini", "vertex"} {
		t.Run(modelType, func(t *testing.T) {
			info := ResolveModelInfo("gpt-image-2", modelType, nil)
			if info.SupportsImageAPI {
				t.Fatalf("%s model inherited provider-specific image API support", modelType)
			}
			if info.ChatDisabled {
				t.Fatalf("%s model inherited provider-specific chat exclusion", modelType)
			}
		})
	}
}

func TestResolveModelInfoPreservesNativeStaticImageAPI(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		modelType string
		provider  string
		want      bool
	}{
		{name: "codex public type", model: "gpt-image-2", modelType: "openai", provider: "codex", want: true},
		{name: "codex native", model: "gpt-image-2", modelType: "codex", provider: "codex", want: true},
		{name: "xai native", model: "grok-imagine-image", modelType: "xai", provider: "xai", want: true},
		{name: "xai cannot inherit codex", model: "gpt-image-2", modelType: "xai", provider: "xai"},
		{name: "codex cannot inherit xai", model: "grok-imagine-image", modelType: "codex", provider: "codex"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info := ResolveModelInfoForProvider(tc.model, tc.modelType, tc.provider, nil)
			if info.SupportsImageAPI != tc.want {
				t.Fatalf("%s image API support = %t, want %t", tc.modelType, info.SupportsImageAPI, tc.want)
			}
			if info.ChatDisabled != tc.want {
				t.Fatalf("%s chat-disabled capability = %t, want %t", tc.modelType, info.ChatDisabled, tc.want)
			}
		})
	}
}

func TestResolveModelInfoPreservesNativeStaticVideoAPI(t *testing.T) {
	info := ResolveModelInfoForProvider("grok-imagine-video", "xai", "xai", nil)
	if !info.SupportsVideoAPI || !info.ChatDisabled {
		t.Fatalf("xAI video metadata = %+v, want video-only execution", info)
	}
	if info.SupportsImageAPI {
		t.Fatal("xAI video model unexpectedly supports image execution")
	}

	foreign := ResolveModelInfoForProvider("grok-imagine-video", "claude", "claude", nil)
	if foreign.SupportsVideoAPI || foreign.ChatDisabled {
		t.Fatalf("Claude model inherited xAI video capability: %+v", foreign)
	}
}

func TestRebindModelInfoUpdatesResourceName(t *testing.T) {
	info := &registry.ModelInfo{ID: "gemini-2.5-flash", Name: "models/gemini-2.5-flash"}
	RebindModelInfo(info, info.ID, "tenant/public-flash")
	if info.ID != "tenant/public-flash" || info.Name != "models/tenant/public-flash" {
		t.Fatalf("rebound identity = %#v, want tenant alias in ID and resource name", info)
	}
}
