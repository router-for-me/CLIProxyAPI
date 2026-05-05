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

// TestExtractFingerprint_ChatMessagesSystemRole verifies that
// extractSystemText reads the system/developer entry from messages[]
// for OpenAI-Chat-style payloads (groq, kimi, openrouter, fireworks
// compat providers, etc.) when no top-level `system` field is set.
func TestExtractFingerprint_ChatMessagesSystemRole(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.4",
		"messages":[
			{"role":"system","content":"You are the Oracle - an expert AI advisor with advanced reasoning capabilities."},
			{"role":"user","content":"hello"}
		]
	}`)
	if got := ExtractFingerprint(body).Feature(); got != "oracle" {
		t.Fatalf("Feature = %q, want oracle (chat messages[] system entry must be parsed)", got)
	}
}

// TestExtractFingerprint_OracleArrayInstructions verifies that an Oracle
// request using array-form `instructions` (instead of a plain string)
// still has its system text extracted and feature detection works.
// OpenAI Responses accepts both shapes.
func TestExtractFingerprint_OracleArrayInstructions(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.4",
		"instructions":[{"type":"text","text":"You are the Oracle - an expert AI advisor with advanced reasoning capabilities."}],
		"input":[{"role":"user","content":[{"type":"input_text","text":"Task: review fingerprint.go"}]}]
	}`)
	fp := ExtractFingerprint(body)
	if got := fp.Feature(); got != "oracle" {
		t.Fatalf("Feature = %q, want oracle (array-form instructions must be parsed)", got)
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

// TestExtractFingerprint_Classifier matches the internal yes/no classifier
// (Anthropic claude-haiku-4-5 with tool_choice = answer_question, system
// "You are a classifier that answers yes/no questions...").
func TestExtractFingerprint_Classifier(t *testing.T) {
	body := []byte(`{
		"model":"claude-haiku-4-5-20251001",
		"system":"You are a classifier that answers yes/no questions. You must use the provided tool to give your answer with reasoning.",
		"messages":[{"role":"user","content":"Should we compact this thread?"}],
		"tool_choice":{"type":"tool","name":"answer_question"},
		"tools":[{"name":"answer_question"}]
	}`)
	fp := ExtractFingerprint(body)
	if got := fp.Feature(); got != "classifier" {
		t.Fatalf("Feature = %q, want classifier", got)
	}
}

// TestExtractFingerprint_GitListHelper matches the amp.review git-list
// preparation call (claude-haiku-4-5).
func TestExtractFingerprint_GitListHelper(t *testing.T) {
	body := []byte(`{
		"model":"claude-haiku-4-5-20251001",
		"system":"You generate git commands to list changed files. Output commands wrapped in <command></command> tags.",
		"messages":[{"role":"user","content":"HEAD~1"}]
	}`)
	if got := ExtractFingerprint(body).Feature(); got != "git_list" {
		t.Fatalf("Feature = %q, want git_list", got)
	}
}

// TestExtractFingerprint_GitDiffHelper matches the amp.review git-diff
// preparation call (claude-haiku-4-5).
func TestExtractFingerprint_GitDiffHelper(t *testing.T) {
	body := []byte(`{
		"model":"claude-haiku-4-5-20251001",
		"system":"You generate git diff commands that show the actual diff content. Output commands in XML format:\n<command>...</command>",
		"messages":[{"role":"user","content":"HEAD~1"}]
	}`)
	if got := ExtractFingerprint(body).Feature(); got != "git_diff" {
		t.Fatalf("Feature = %q, want git_diff", got)
	}
}

// TestExtractFingerprint_CodereviewCheck matches the per-check review
// subagent invoked by `amp review --checks-only` when a check is found.
// User message template: `Run the "${name}" code review check.`
func TestExtractFingerprint_CodereviewCheck(t *testing.T) {
	body := []byte(`{
		"model":"claude-haiku-4-5-20251001",
		"system":"You are an expert senior engineer with deep knowledge of software engineering best practices, security, performance, and maintainability.\n\n## Files to Review\n...",
		"messages":[{"role":"user","content":"Run the \"sql-injection-checker\" code review check."}]
	}`)
	if got := ExtractFingerprint(body).Feature(); got != "codereview_check" {
		t.Fatalf("Feature = %q, want codereview_check", got)
	}
}

// TestExtractFingerprint_CodereviewCheck_DoesNotCollideWithReview ensures
// that loose phrases ending in "code review check" inside a review-main
// request do not get reclassified away from "review".
func TestExtractFingerprint_CodereviewCheck_DoesNotCollideWithReview(t *testing.T) {
	body := []byte(`{
		"contents":[{"role":"user","parts":[{"text":"Please run a thorough code review check."}]}],
		"systemInstruction":{"parts":[{"text":"You are an expert senior engineer with deep knowledge of software engineering best practices."}]}
	}`)
	if got := ExtractFingerprint(body).Feature(); got != "review" {
		t.Fatalf("Feature = %q, want review (codereview_check should not collide)", got)
	}
}

// TestExtractFingerprint_ThreadExtract matches the read_thread tool's
// internal extraction call. Source-confirmed via `await KW(kb, ...)`
// in the binary, which is the same Gemini wrapper used by error_summary
// and review-summary (both observed flowing through /api/provider/google).
func TestExtractFingerprint_ThreadExtract(t *testing.T) {
	body := []byte(`{
		"contents":[{"role":"user","parts":[{"text":"thread content..."}]}],
		"systemInstruction":{"parts":[{"text":"You are helping me extract relevant information from the mentioned thread based on a goal."}]}
	}`)
	if got := ExtractFingerprint(body).Feature(); got != "thread_extract" {
		t.Fatalf("Feature = %q, want thread_extract", got)
	}
}

// TestExtractFingerprint_ErrorSummary matches the subagent error-recovery
// summarizer that fires when a Task subagent hits a fatal error. Source-
// confirmed: same KW(kb, ...) Gemini wrapper as thread_extract, with the
// binary's debug log explicitly saying "Failed to summarize subagent
// work with Gemini".
func TestExtractFingerprint_ErrorSummary(t *testing.T) {
	body := []byte(`{
		"contents":[{"role":"user","parts":[{"text":"work history..."}]}],
		"systemInstruction":{"parts":[{"text":"You are helping summarize work done by an AI coding agent (subagent) before it encountered an error."}]}
	}`)
	if got := ExtractFingerprint(body).Feature(); got != "error_summary" {
		t.Fatalf("Feature = %q, want error_summary", got)
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

// TestModelMapper_DuplicateExactFallbackFirstWins verifies that when
// multiple unconditional rules share the same From, the first one wins
// (top-to-bottom declaration order).
func TestModelMapper_DuplicateExactFallbackFirstWins(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-dup-first", "openai", []*registry.ModelInfo{
		{ID: "first-target", OwnedBy: "openai", Type: "openai"},
		{ID: "second-target", OwnedBy: "openai", Type: "openai"},
	})
	defer reg.UnregisterClient("test-dup-first")

	mapper := NewModelMapper([]config.AmpModelMapping{
		{From: "gemini-3-flash-preview", To: "first-target"},
		{From: "gemini-3-flash-preview", To: "second-target"},
	})

	if got := mapper.MapModel("gemini-3-flash-preview"); got != "first-target" {
		t.Errorf("got %q, want first-target (first-declared rule must win)", got)
	}
}

// TestModelMapper_NonContiguousSameFromFallbackWins verifies that with
// top-to-bottom semantics, an earlier unconditional rule wins over a
// later conditional rule even if they share the same From pattern.
// Users must place conditional rules before the fallback.
func TestModelMapper_NonContiguousSameFromFallbackWins(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-non-contiguous", "anthropic", []*registry.ModelInfo{
		{ID: "early-fallback", OwnedBy: "anthropic", Type: "claude"},
		{ID: "interleaved", OwnedBy: "anthropic", Type: "claude"},
		{ID: "late-conditional", OwnedBy: "anthropic", Type: "claude"},
	})
	defer reg.UnregisterClient("test-non-contiguous")

	mapper := NewModelMapper([]config.AmpModelMapping{
		// Unconditional fallback declared first — wins for all requests.
		{From: "^gemini-3-.*$", To: "early-fallback", Regex: true},
		// Unrelated pattern in between.
		{From: "^.*-flash-.*$", To: "interleaved", Regex: true},
		// Same From, conditional, but declared after fallback — unreachable.
		{From: "^gemini-3-.*$", To: "late-conditional", Regex: true,
			When: &config.AmpMappingCondition{Feature: "handoff"}},
	})

	// Even handoff goes to early-fallback because it is declared first.
	got := mapper.MapModelCtx("gemini-3-flash-preview",
		RequestFingerprint{ToolChoice: "create_handoff_context"})
	if got != "early-fallback" {
		t.Errorf("got %q, want early-fallback (first matching rule wins top-to-bottom)", got)
	}
	if got := mapper.MapModelCtx("gemini-3-flash-preview", RequestFingerprint{}); got != "early-fallback" {
		t.Errorf("got %q, want early-fallback", got)
	}
}

// TestModelMapper_ConditionalUnavailableFallsThroughToFallback verifies
// that when a conditional rule's `to` model has no local providers, the
// mapper continues scanning and returns the same-From unconditional
// fallback, rather than silently giving up and returning "".
func TestModelMapper_ConditionalUnavailableFallsThroughToFallback(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	// Register only the fallback target. The conditional target is
	// intentionally NOT registered so it has no providers.
	reg.RegisterClient("test-cond-unavail", "openai", []*registry.ModelInfo{
		{ID: "gpt-fallback", OwnedBy: "openai", Type: "openai"},
	})
	defer reg.UnregisterClient("test-cond-unavail")

	mapper := NewModelMapper([]config.AmpModelMapping{
		{From: "gemini-3-flash-preview", To: "gpt-handoff-missing",
			When: &config.AmpMappingCondition{Feature: "handoff"}},
		{From: "gemini-3-flash-preview", To: "gpt-fallback"},
	})

	got := mapper.MapModelCtx("gemini-3-flash-preview",
		RequestFingerprint{ToolChoice: "create_handoff_context"})
	if got != "gpt-fallback" {
		t.Errorf("got %q, want gpt-fallback (conditional target unavailable must fall through)", got)
	}
}

// TestModelMapper_RegexConditionalUnavailableFallsThroughToOverlapping
// verifies the same fall-through behavior across overlapping regex
// patterns: when an earlier conditional regex's target is unavailable,
// the next matching regex (or unconditional fallback) is used instead.
func TestModelMapper_RegexConditionalUnavailableFallsThroughToOverlapping(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-regex-unavail", "anthropic", []*registry.ModelInfo{
		{ID: "late-fallback", OwnedBy: "anthropic", Type: "claude"},
	})
	defer reg.UnregisterClient("test-regex-unavail")

	mapper := NewModelMapper([]config.AmpModelMapping{
		{From: "^gemini-3-.*$", To: "missing-conditional", Regex: true,
			When: &config.AmpMappingCondition{Feature: "handoff"}},
		{From: "^.*-flash-.*$", To: "late-fallback", Regex: true},
	})

	got := mapper.MapModelCtx("gemini-3-flash-preview",
		RequestFingerprint{ToolChoice: "create_handoff_context"})
	if got != "late-fallback" {
		t.Errorf("got %q, want late-fallback (unavailable conditional must not block later overlapping rule)", got)
	}
}

// TestModelMapper_RegexOrderRespectedAcrossOverlappingPatterns verifies
// that when two regex rules with different From patterns both match the
// same model, the earlier rule's unconditional target wins over the
// later rule, regardless of conditional rules attached to the later
// pattern. Cross-group declaration order must be preserved.
func TestModelMapper_RegexOrderRespectedAcrossOverlappingPatterns(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-regex-order", "anthropic", []*registry.ModelInfo{
		{ID: "early-target", OwnedBy: "anthropic", Type: "claude"},
		{ID: "late-handoff-target", OwnedBy: "anthropic", Type: "claude"},
	})
	defer reg.UnregisterClient("test-regex-order")

	// Two distinct regex patterns that both match "gemini-3-flash-preview".
	// Earlier rule is unconditional; later rule has a handoff condition.
	// Groups are evaluated in declaration order, so the earlier group's
	// unconditional fallback is returned before the later group's
	// conditional rule is considered.
	mapper := NewModelMapper([]config.AmpModelMapping{
		{From: "^gemini-3-.*$", To: "early-target", Regex: true},
		{From: "^.*-flash-.*$", To: "late-handoff-target", Regex: true,
			When: &config.AmpMappingCondition{Feature: "handoff"}},
	})

	got := mapper.MapModelCtx("gemini-3-flash-preview",
		RequestFingerprint{ToolChoice: "create_handoff_context"})
	if got != "early-target" {
		t.Errorf("got %q, want early-target (declaration order must win across overlapping patterns)", got)
	}
}

// TestModelMapper_RegexConditionalAfterFallbackIsUnreachable verifies
// that with top-to-bottom semantics, a regex conditional rule declared
// after an unconditional rule sharing the same pattern is unreachable.
func TestModelMapper_RegexConditionalAfterFallbackIsUnreachable(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-regex-group", "anthropic", []*registry.ModelInfo{
		{ID: "conditional-target", OwnedBy: "anthropic", Type: "claude"},
		{ID: "fallback-target", OwnedBy: "anthropic", Type: "claude"},
	})
	defer reg.UnregisterClient("test-regex-group")

	mapper := NewModelMapper([]config.AmpModelMapping{
		{From: "^gemini-3-.*$", To: "fallback-target", Regex: true},
		{From: "^gemini-3-.*$", To: "conditional-target", Regex: true,
			When: &config.AmpMappingCondition{Feature: "handoff"}},
	})

	// Fallback is first, so it wins even for handoff requests.
	if got := mapper.MapModelCtx("gemini-3-flash-preview",
		RequestFingerprint{ToolChoice: "create_handoff_context"}); got != "fallback-target" {
		t.Errorf("got %q, want fallback-target (first rule wins)", got)
	}
	if got := mapper.MapModelCtx("gemini-3-flash-preview", RequestFingerprint{}); got != "fallback-target" {
		t.Errorf("got %q, want fallback-target", got)
	}
}

// TestModelMapper_RegexCaseVariantLinearOrder verifies that two regex
// rules whose From patterns differ only by letter case are both valid
// rules. With top-to-bottom semantics, the first matching rule wins.
func TestModelMapper_RegexCaseVariantLinearOrder(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-regex-case", "anthropic", []*registry.ModelInfo{
		{ID: "case-conditional-target", OwnedBy: "anthropic", Type: "claude"},
		{ID: "case-fallback-target", OwnedBy: "anthropic", Type: "claude"},
	})
	defer reg.UnregisterClient("test-regex-case")

	mapper := NewModelMapper([]config.AmpModelMapping{
		{From: "^GEMINI-3-.*$", To: "case-fallback-target", Regex: true},
		{From: "^gemini-3-.*$", To: "case-conditional-target", Regex: true,
			When: &config.AmpMappingCondition{Feature: "handoff"}},
	})

	// First rule (unconditional) wins for all requests since it is declared first.
	if got := mapper.MapModelCtx("gemini-3-flash-preview",
		RequestFingerprint{ToolChoice: "create_handoff_context"}); got != "case-fallback-target" {
		t.Errorf("got %q, want case-fallback-target (first rule wins)", got)
	}
	if got := mapper.MapModelCtx("gemini-3-flash-preview", RequestFingerprint{}); got != "case-fallback-target" {
		t.Errorf("got %q, want case-fallback-target", got)
	}
}

// TestModelMapper_EmptyWhenTreatedAsUnconditional verifies that a
// mapping with `When: &AmpMappingCondition{}` (all fields empty) is
// treated as unconditional, same as `When: nil`. With top-to-bottom
// semantics, the first declared rule wins.
func TestModelMapper_EmptyWhenTreatedAsUnconditional(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-empty-when", "openai", []*registry.ModelInfo{
		{ID: "gpt-first", OwnedBy: "openai", Type: "openai"},
		{ID: "gpt-second", OwnedBy: "openai", Type: "openai"},
	})
	defer reg.UnregisterClient("test-empty-when")

	mapper := NewModelMapper([]config.AmpModelMapping{
		{From: "gemini-3-flash-preview", To: "gpt-first"},
		{From: "gemini-3-flash-preview", To: "gpt-second", When: &config.AmpMappingCondition{}},
	})

	// First rule wins for both calls.
	if got := mapper.MapModelCtx("gemini-3-flash-preview", RequestFingerprint{}); got != "gpt-first" {
		t.Errorf("got %q, want gpt-first (first rule wins, no fingerprint)", got)
	}
	if got := mapper.MapModelCtx("gemini-3-flash-preview", RequestFingerprint{ToolChoice: "create_handoff_context"}); got != "gpt-first" {
		t.Errorf("got %q, want gpt-first (first rule wins, with fingerprint)", got)
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

// TestModelMapper_MapModelCtx_ConditionalMustBeDeclaredFirst verifies that
// with top-to-bottom semantics, a conditional rule must be declared before
// the fallback to take effect. When fallback is first, it always wins.
func TestModelMapper_MapModelCtx_ConditionalMustBeDeclaredFirst(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-cond3", "openai", []*registry.ModelInfo{
		{ID: "gpt-handoff", OwnedBy: "openai", Type: "openai"},
		{ID: "gpt-default", OwnedBy: "openai", Type: "openai"},
	})
	defer reg.UnregisterClient("test-cond3")

	// Fallback FIRST, conditional SECOND — fallback always wins.
	mapper := NewModelMapper([]config.AmpModelMapping{
		{From: "gemini-3-flash-preview", To: "gpt-default"},
		{From: "gemini-3-flash-preview", To: "gpt-handoff", When: &config.AmpMappingCondition{Feature: "handoff"}},
	})

	if got := mapper.MapModelCtx("gemini-3-flash-preview", RequestFingerprint{ToolChoice: "create_handoff_context"}); got != "gpt-default" {
		t.Errorf("handoff (fallback-before-conditional): got %q want gpt-default (first rule wins)", got)
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
