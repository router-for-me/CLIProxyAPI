package amp

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

func TestExtractFingerprint_AnthropicHandoff(t *testing.T) {
	body := []byte(`{
		"model":"gemini-3-flash-preview",
		"system":"",
		"messages":[{"role":"user","content":[{"type":"text","text":"Earlier we did X.\nUse the create_handoff_context tool to extract relevant information and files."}]}],
		"tool_choice":{"type":"tool","name":"create_handoff_context"}
	}`)
	fp := ExtractFingerprint(body)
	if fp.ToolChoice != "create_handoff_context" {
		t.Fatalf("ToolChoice = %q", fp.ToolChoice)
	}
	if got := fp.Feature(); got != "handoff" {
		t.Fatalf("Feature = %q, want handoff", got)
	}
}

func TestExtractFingerprint_OpenAI(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"function","function":{"name":"create_handoff_context"}}}`)
	fp := ExtractFingerprint(body)
	if fp.ToolChoice != "create_handoff_context" {
		t.Fatalf("ToolChoice = %q", fp.ToolChoice)
	}
}

func TestExtractFingerprint_GeminiAnyMode(t *testing.T) {
	body := []byte(`{
		"contents":[{"role":"user","parts":[{"text":"foo Use the create_handoff_context tool to extract relevant information and files."}]}],
		"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["create_handoff_context"]}}
	}`)
	fp := ExtractFingerprint(body)
	if fp.ToolChoice != "create_handoff_context" {
		t.Fatalf("ToolChoice = %q", fp.ToolChoice)
	}
	if fp.Feature() != "handoff" {
		t.Fatal("expected handoff feature")
	}
}

func TestExtractFingerprint_Empty(t *testing.T) {
	fp := ExtractFingerprint(nil)
	if fp.ToolChoice != "" || fp.LastUserText != "" {
		t.Fatalf("expected empty, got %+v", fp)
	}
}

func TestConditionMatches(t *testing.T) {
	fp := RequestFingerprint{ToolChoice: "create_handoff_context", LastUserText: "blah\nUse the create_handoff_context tool to extract relevant information and files."}
	cases := []struct {
		name string
		cond *config.AmpMappingCondition
		want bool
	}{
		{"nil matches", nil, true},
		{"feature handoff", &config.AmpMappingCondition{Feature: "handoff"}, true},
		{"feature search no match", &config.AmpMappingCondition{Feature: "search"}, false},
		{"tool_choice match", &config.AmpMappingCondition{ToolChoice: "create_handoff_context"}, true},
		{"tool_choice no match", &config.AmpMappingCondition{ToolChoice: "other"}, false},
		{"user_suffix match", &config.AmpMappingCondition{UserSuffix: "extract relevant information and files."}, true},
		{"user_suffix no match", &config.AmpMappingCondition{UserSuffix: "nope"}, false},
		{"AND match", &config.AmpMappingCondition{Feature: "handoff", ToolChoice: "create_handoff_context"}, true},
		{"AND fail", &config.AmpMappingCondition{Feature: "handoff", ToolChoice: "x"}, false},
	}
	for _, c := range cases {
		if got := ConditionMatches(c.cond, fp); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestModelMapper_MapModelCtx_ConditionalThenFallback(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-cond", "openai", []*registry.ModelInfo{
		{ID: "gpt-handoff", OwnedBy: "openai", Type: "openai"},
		{ID: "gpt-default", OwnedBy: "openai", Type: "openai"},
	})
	defer reg.UnregisterClient("test-cond")

	mapper := NewModelMapper([]config.AmpModelMapping{
		{From: "gemini-3-flash-preview", To: "gpt-handoff", When: &config.AmpMappingCondition{Feature: "handoff"}},
		{From: "gemini-3-flash-preview", To: "gpt-default"},
	})

	// Handoff request -> conditional rule wins
	fpHandoff := RequestFingerprint{ToolChoice: "create_handoff_context"}
	if got := mapper.MapModelCtx("gemini-3-flash-preview", fpHandoff); got != "gpt-handoff" {
		t.Errorf("handoff: got %q want gpt-handoff", got)
	}

	// Non-handoff request -> fallback rule
	if got := mapper.MapModelCtx("gemini-3-flash-preview", RequestFingerprint{}); got != "gpt-default" {
		t.Errorf("default: got %q want gpt-default", got)
	}

	// MapModel (no fp) should also pick the fallback
	if got := mapper.MapModel("gemini-3-flash-preview"); got != "gpt-default" {
		t.Errorf("MapModel: got %q want gpt-default", got)
	}
}

func TestModelMapper_MapModelCtx_ConditionalOnlyNoFallback(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-cond2", "openai", []*registry.ModelInfo{
		{ID: "gpt-handoff", OwnedBy: "openai", Type: "openai"},
	})
	defer reg.UnregisterClient("test-cond2")

	mapper := NewModelMapper([]config.AmpModelMapping{
		{From: "gemini-3-flash-preview", To: "gpt-handoff", When: &config.AmpMappingCondition{Feature: "handoff"}},
	})
	// Without matching fingerprint, no mapping
	if got := mapper.MapModelCtx("gemini-3-flash-preview", RequestFingerprint{}); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
