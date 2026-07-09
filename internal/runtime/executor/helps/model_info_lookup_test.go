package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestRequestModelInfoUsesRequestScopedPrefixedModel(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-model-info-lookup-prefixed"
	provider := "test-prefixed-provider"
	reg.RegisterClient(clientID, provider, []*registry.ModelInfo{{
		ID: "free-provider/gpt-test",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"xhigh", "high"},
		},
		MaxCompletionTokens: 12345,
	}})
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	lookup := RequestModelInfo(
		&cliproxyauth.Auth{Prefix: "free-provider"},
		provider,
		"gpt-test(xhigh)",
		cliproxyexecutor.Options{
			Metadata: map[string]any{
				cliproxyexecutor.ModelInfoLookupModelMetadataKey: "free-provider/gpt-test(xhigh)",
			},
		},
	)

	if lookup.Model != "free-provider/gpt-test(xhigh)" {
		t.Fatalf("lookup model = %q, want prefixed request-scoped model", lookup.Model)
	}
	if lookup.Info == nil {
		t.Fatal("expected prefixed ModelInfo")
	}
	if got := lookup.Info.MaxCompletionTokens; got != 12345 {
		t.Fatalf("MaxCompletionTokens = %d, want 12345", got)
	}
	if got := lookup.Info.Thinking.Levels[0]; got != "xhigh" {
		t.Fatalf("first thinking level = %q, want xhigh", got)
	}
}

func TestRequestModelInfoDoesNotUseOtherProviderPrefixedModel(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-model-info-lookup-other-provider"
	reg.RegisterClient(clientID, "provider-a", []*registry.ModelInfo{{
		ID: "free-provider/provider-specific-model",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"xhigh"},
		},
	}})
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	lookup := RequestModelInfo(
		&cliproxyauth.Auth{Prefix: "free-provider"},
		"provider-b",
		"provider-specific-model",
		cliproxyexecutor.Options{
			Metadata: map[string]any{
				cliproxyexecutor.ModelInfoLookupModelMetadataKey: "free-provider/provider-specific-model",
			},
		},
	)

	if lookup.Model != "provider-specific-model" {
		t.Fatalf("lookup model = %q, want upstream model fallback", lookup.Model)
	}
	if lookup.Info != nil {
		t.Fatalf("unexpected ModelInfo from another provider: %+v", lookup.Info)
	}
}

func TestRequestModelInfoDoesNotUseOtherProviderUnprefixedFallback(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-model-info-lookup-other-provider-unprefixed"
	reg.RegisterClient(clientID, "provider-a", []*registry.ModelInfo{{
		ID: "provider-specific-model",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"xhigh"},
		},
	}})
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	lookup := RequestModelInfo(
		nil,
		"provider-b",
		"provider-specific-model",
		cliproxyexecutor.Options{},
	)

	if lookup.Model != "provider-specific-model" {
		t.Fatalf("lookup model = %q, want upstream model", lookup.Model)
	}
	if lookup.Info != nil {
		t.Fatalf("unexpected unprefixed ModelInfo from another provider: %+v", lookup.Info)
	}
}

func TestRequestModelInfoFallsBackToStaticModelInfo(t *testing.T) {
	lookup := RequestModelInfo(
		nil,
		"antigravity",
		"gemini-3.5-flash-low",
		cliproxyexecutor.Options{},
	)

	if lookup.Info == nil {
		t.Fatal("expected static ModelInfo fallback")
	}
	if lookup.Info.Thinking == nil {
		t.Fatal("expected static thinking metadata")
	}
	if got := lookup.Info.ID; got != "gemini-3.5-flash-low" {
		t.Fatalf("ModelInfo.ID = %q, want gemini-3.5-flash-low", got)
	}
}

func TestRequestModelInfoFillsMissingThinkingFromUpstreamModel(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-model-info-lookup-prefixed-static-thinking"
	provider := "gemini"
	reg.RegisterClient(clientID, provider, []*registry.ModelInfo{{
		ID: "free-provider/gemini-3.1-pro-preview",
	}})
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	lookup := RequestModelInfo(
		&cliproxyauth.Auth{Prefix: "free-provider"},
		provider,
		"gemini-3.1-pro-preview",
		cliproxyexecutor.Options{
			Metadata: map[string]any{
				cliproxyexecutor.ModelInfoLookupModelMetadataKey: "free-provider/gemini-3.1-pro-preview",
			},
		},
	)

	if lookup.Model != "free-provider/gemini-3.1-pro-preview" {
		t.Fatalf("lookup model = %q, want prefixed request-scoped model", lookup.Model)
	}
	if lookup.Info == nil {
		t.Fatal("expected prefixed ModelInfo")
	}
	if lookup.Info.ID != "free-provider/gemini-3.1-pro-preview" {
		t.Fatalf("ModelInfo.ID = %q, want prefixed registration", lookup.Info.ID)
	}
	if lookup.Info.Thinking == nil {
		t.Fatal("expected static upstream thinking fallback")
	}
	levels := lookup.Info.Thinking.Levels
	if len(levels) != 3 || levels[0] != "low" || levels[1] != "medium" || levels[2] != "high" {
		t.Fatalf("Thinking.Levels = %v, want [low medium high]", levels)
	}
}

func TestApplyRequestThinkingDoesNotUseOtherProviderFallback(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-apply-thinking-other-provider-unprefixed"
	model := "provider-specific-model"
	reg.RegisterClient(clientID, "provider-a", []*registry.ModelInfo{{
		ID: model,
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"high"},
		},
	}})
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	source := []byte(`{"model":"provider-specific-model","reasoning_effort":"xhigh","messages":[{"role":"user","content":"hi"}]}`)
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)

	out, err := ApplyRequestThinkingWithSource(
		body,
		source,
		nil,
		"provider-b",
		model,
		cliproxyexecutor.Options{},
		"openai",
		"claude",
	)
	if err != nil {
		t.Fatalf("ApplyRequestThinkingWithSource() error = %v", err)
	}
	if got := gjson.GetBytes(out, "output_config.effort").String(); got != "xhigh" {
		t.Fatalf("output_config.effort = %q, want xhigh passthrough without other provider metadata; body=%s", got, out)
	}
}

func TestRequestModelInfoDoesNotDoublePrefixCaseInsensitiveModel(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	clientID := "test-model-info-lookup-case-prefix"
	provider := "test-case-prefix-provider"
	reg.RegisterClient(clientID, provider, []*registry.ModelInfo{{
		ID: "Free-Provider/gpt-test",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"high"},
		},
	}})
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	lookup := RequestModelInfo(
		&cliproxyauth.Auth{Prefix: "free-provider"},
		provider,
		"Free-Provider/gpt-test(high)",
		cliproxyexecutor.Options{},
	)

	if lookup.Model != "Free-Provider/gpt-test(high)" {
		t.Fatalf("lookup model = %q, want original case-preserved prefixed model", lookup.Model)
	}
	if lookup.Info == nil {
		t.Fatal("expected case-preserved prefixed ModelInfo")
	}
}
