package amp

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

func TestExtractFingerprint_Titling(t *testing.T) {
	// Real Amp titling request observed: Anthropic Claude Haiku with
	// tool_choice forcing set_title, message wrapped in <message>...</message>.
	body := []byte(`{
		"model":"claude-haiku-4-5-20251001",
		"max_tokens":60,
		"system":"You are an assistant that generates short, descriptive titles (maximum 5 words) ...",
		"messages":[{"role":"user","content":"<message>say hi in one word</message>"}],
		"tool_choice":{"type":"tool","name":"set_title","disable_parallel_tool_use":true}
	}`)
	fp := ExtractFingerprint(body)
	if fp.ToolChoice != "set_title" {
		t.Fatalf("ToolChoice = %q", fp.ToolChoice)
	}
	if got := fp.Feature(); got != "titling" {
		t.Fatalf("Feature = %q, want titling", got)
	}
}

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

// TestExtractFingerprint_Oracle uses a real OpenAI Responses payload
// captured from `amp -x "use the oracle tool ..."`. Oracle has no
// tool_choice; it is identified by its hardcoded system prompt.
func TestExtractFingerprint_Oracle(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.4",
		"input":[
			{"role":"system","content":"You are the Oracle - an expert AI advisor with advanced reasoning capabilities.\n\nYour role is to provide high-quality technical guidance, code reviews, architectural advice, and strategic planning for software engineering tasks."},
			{"role":"user","content":[{"type":"input_text","text":"Task: review fingerprint.go"}]}
		],
		"reasoning":{"effort":"high","summary":"auto"}
	}`)
	fp := ExtractFingerprint(body)
	if fp.ToolChoice != "" {
		t.Fatalf("ToolChoice = %q, want empty", fp.ToolChoice)
	}
	if got := fp.Feature(); got != "oracle" {
		t.Fatalf("Feature = %q, want oracle", got)
	}
}

// TestExtractFingerprint_SearchSubagent uses the real Gemini payload
// captured from the Amp finder (search subagent) tool. Identified by
// its hardcoded systemInstruction prefix.
func TestExtractFingerprint_SearchSubagent(t *testing.T) {
	body := []byte(`{
		"contents":[{"role":"user","parts":[{"text":"Find OAuth callback handlers."}]}],
		"systemInstruction":{"parts":[{"text":"You are a fast, parallel code search agent.\n\n## Task\nFind files and line ranges relevant to the user's query (provided in the first message)."}]},
		"tools":[{"functionDeclarations":[{"name":"glob"},{"name":"Grep"},{"name":"Read"}]}]
	}`)
	fp := ExtractFingerprint(body)
	if got := fp.Feature(); got != "search" {
		t.Fatalf("Feature = %q, want search", got)
	}
}

// TestExtractFingerprint_LookAt uses the real Gemini payload captured
// from the Amp look_at tool. Identified by its hardcoded
// systemInstruction prefix.
func TestExtractFingerprint_LookAt(t *testing.T) {
	body := []byte(`{
		"contents":[{"role":"user","parts":[
			{"inlineData":{"mimeType":"image/png","data":"AA=="}},
			{"text":"User asked for a brief description of the image.\n\nAnalyze this file with the following objective:\n\nBriefly describe the contents of the image."}
		]}],
		"systemInstruction":{"parts":[{"text":"You are an AI assistant that analyzes files for a software engineer.\n\n# Core Principles\n\n- Be concise and direct."}]}
	}`)
	fp := ExtractFingerprint(body)
	if got := fp.Feature(); got != "look_at" {
		t.Fatalf("Feature = %q, want look_at", got)
	}
}

// TestExtractFingerprint_Review uses the Amp review systemInstruction
// (compiled into the Amp binary at amp.review).
func TestExtractFingerprint_Review(t *testing.T) {
	body := []byte(`{
		"contents":[{"role":"user","parts":[{"text":"diff..."}]}],
		"systemInstruction":{"parts":[{"text":"You are an expert software engineer reviewing code changes."}]}
	}`)
	fp := ExtractFingerprint(body)
	if got := fp.Feature(); got != "review" {
		t.Fatalf("Feature = %q, want review", got)
	}
}

// TestExtractFingerprint_Painter uses the real Gemini image-generation
// payload captured from the Amp painter tool. Identified by
// generationConfig.responseModalities containing "IMAGE".
func TestExtractFingerprint_Painter(t *testing.T) {
	body := []byte(`{
		"contents":[{"role":"user","parts":[{"text":"A small solid red circle centered on a plain white background."}]}],
		"generationConfig":{"responseModalities":["TEXT","IMAGE"],"imageConfig":{"imageSize":"1K"}}
	}`)
	fp := ExtractFingerprint(body)
	if !fp.HasImageOutput {
		t.Fatalf("HasImageOutput = false, want true")
	}
	if got := fp.Feature(); got != "painter" {
		t.Fatalf("Feature = %q, want painter", got)
	}
}

// TestExtractFingerprint_ReviewMain captures the `amp review` main
// analysis prompt (gemini-3.1-pro-preview). Folded into the same
// "review" feature alias as the summary call so users can route the
// whole review feature with one rule.
func TestExtractFingerprint_ReviewMain(t *testing.T) {
	body := []byte(`{
		"contents":[{"role":"user","parts":[{"text":"diff..."}]}],
		"systemInstruction":{"parts":[{"text":"You are an expert senior engineer with deep knowledge of software engineering best practices, security, performance, and maintainability."}]}
	}`)
	fp := ExtractFingerprint(body)
	if got := fp.Feature(); got != "review" {
		t.Fatalf("Feature = %q, want review", got)
	}
}

// TestExtractFingerprint_Librarian uses the real Anthropic payload
// captured from the Amp librarian tool (claude-sonnet-4-6).
func TestExtractFingerprint_Librarian(t *testing.T) {
	body := []byte(`{
		"model":"claude-sonnet-4-6",
		"system":"You are the Librarian, a specialized codebase understanding agent that helps users answer questions about large, complex codebases across repositories.",
		"messages":[{"role":"user","content":"Briefly describe what the github.com/router-for-me/CLIProxyAPI repository does — its purpose, main features, and how it works."}]
	}`)
	fp := ExtractFingerprint(body)
	if got := fp.Feature(); got != "librarian" {
		t.Fatalf("Feature = %q, want librarian", got)
	}
}

// TestExtractFingerprint_SystemPrefixCustom verifies users can match an
// arbitrary system prompt prefix that is not encoded in Feature().
func TestExtractFingerprint_SystemPrefixCustom(t *testing.T) {
	body := []byte(`{
		"contents":[{"role":"user","parts":[{"text":"hi"}]}],
		"systemInstruction":{"parts":[{"text":"You are Agg Man, Amp's platform control-plane assistant."}]}
	}`)
	fp := ExtractFingerprint(body)
	if got := fp.Feature(); got != "" {
		t.Fatalf("Feature = %q, want empty (unrecognized)", got)
	}
	cond := &config.AmpMappingCondition{SystemPrefix: "you are agg man"}
	if !ConditionMatches(cond, fp) {
		t.Fatalf("expected SystemPrefix to match")
	}
}

func TestConditionMatches(t *testing.T) {
	fp := RequestFingerprint{
		ToolChoice:   "create_handoff_context",
		LastUserText: "blah\nUse the create_handoff_context tool to extract relevant information and files.",
		SystemText:   "You are an expert senior engineer with deep knowledge.",
	}
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
		{"system_prefix match", &config.AmpMappingCondition{SystemPrefix: "you are an expert senior engineer"}, true},
		{"system_prefix no match", &config.AmpMappingCondition{SystemPrefix: "you are batman"}, false},
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
