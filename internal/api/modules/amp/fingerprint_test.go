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

// TestModelMapper_MapModelCtx_ConditionalWinsRegardlessOfOrder verifies the
// documented "conditional wins" semantics hold even when the unconditional
// fallback appears before the conditional rule in config.
func TestModelMapper_MapModelCtx_ConditionalWinsRegardlessOfOrder(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-cond3", "openai", []*registry.ModelInfo{
		{ID: "gpt-handoff", OwnedBy: "openai", Type: "openai"},
		{ID: "gpt-default", OwnedBy: "openai", Type: "openai"},
	})
	defer reg.UnregisterClient("test-cond3")

	// Fallback FIRST, conditional SECOND
	mapper := NewModelMapper([]config.AmpModelMapping{
		{From: "gemini-3-flash-preview", To: "gpt-default"},
		{From: "gemini-3-flash-preview", To: "gpt-handoff", When: &config.AmpMappingCondition{Feature: "handoff"}},
	})

	if got := mapper.MapModelCtx("gemini-3-flash-preview", RequestFingerprint{ToolChoice: "create_handoff_context"}); got != "gpt-handoff" {
		t.Errorf("handoff (fallback-before-conditional): got %q want gpt-handoff", got)
	}
	if got := mapper.MapModelCtx("gemini-3-flash-preview", RequestFingerprint{}); got != "gpt-default" {
		t.Errorf("default: got %q want gpt-default", got)
	}
}

// TestModelMapper_ExactBeforeRegex verifies that exact rules win over regex
// rules even when the regex rule is declared first.
func TestModelMapper_ExactBeforeRegex(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-exact-regex", "anthropic", []*registry.ModelInfo{
		{ID: "claude-sonnet-4", OwnedBy: "anthropic", Type: "claude"},
		{ID: "claude-opus-4", OwnedBy: "anthropic", Type: "claude"},
	})
	defer reg.UnregisterClient("test-exact-regex")

	mapper := NewModelMapper([]config.AmpModelMapping{
		{From: "^gpt-5.*$", To: "claude-sonnet-4", Regex: true},
		{From: "gpt-5", To: "claude-opus-4"},
	})
	if got := mapper.MapModel("gpt-5"); got != "claude-opus-4" {
		t.Errorf("got %q, want claude-opus-4 (exact must beat regex)", got)
	}
}

// TestModelMapper_DeepCopiesWhen verifies that mutating the original config
// after UpdateMappings does not affect the mapper's internal state.
func TestModelMapper_DeepCopiesWhen(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-deepcopy", "openai", []*registry.ModelInfo{
		{ID: "gpt-handoff", OwnedBy: "openai", Type: "openai"},
	})
	defer reg.UnregisterClient("test-deepcopy")

	cond := &config.AmpMappingCondition{Feature: "handoff"}
	mappings := []config.AmpModelMapping{
		{From: "gemini-3-flash-preview", To: "gpt-handoff", When: cond},
	}
	mapper := NewModelMapper(mappings)

	// Mutate original condition after construction.
	cond.Feature = "search"

	// Mapper should still match the handoff fingerprint (mutation ignored).
	if got := mapper.MapModelCtx("gemini-3-flash-preview", RequestFingerprint{ToolChoice: "create_handoff_context"}); got != "gpt-handoff" {
		t.Errorf("got %q, want gpt-handoff (mapper must deep-copy When)", got)
	}
}

// TestConditionMatches_TrimsConfigWhitespace verifies trailing whitespace in
// configuration values does not break matching.
func TestConditionMatches_TrimsConfigWhitespace(t *testing.T) {
	fp := RequestFingerprint{
		ToolChoice:   "create_handoff_context",
		LastUserText: "Use the create_handoff_context tool to extract relevant information and files.",
	}
	cond := &config.AmpMappingCondition{
		ToolChoice: "  create_handoff_context  ",
		UserSuffix: "  extract relevant information and files.  ",
	}
	if !ConditionMatches(cond, fp) {
		t.Error("expected match after trimming whitespace from config")
	}
}
