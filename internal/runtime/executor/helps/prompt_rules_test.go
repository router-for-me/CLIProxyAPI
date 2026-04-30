package helps

import (
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/tidwall/gjson"
)

// withPromptRules installs the given rules as the active snapshot for the
// duration of the test, restoring an empty snapshot on cleanup.
func withPromptRules(t *testing.T, rules []config.PromptRule) {
	t.Helper()
	// Normalize defaults so tests can pass minimal rules.
	out := make([]config.PromptRule, 0, len(rules))
	for _, r := range rules {
		if r.Action == config.PromptRuleActionInject && r.Position == "" {
			r.Position = config.PromptRulePositionAppend
		}
		out = append(out, r)
	}
	UpdatePromptRulesSnapshot(out)
	t.Cleanup(func() { UpdatePromptRulesSnapshot(nil) })
}

// applyOpenAI is a convenience wrapper for tests targeting the OpenAI source format.
func applyOpenAI(payload string) []byte {
	return ApplyPromptRules("openai", "gpt-5", []byte(payload), "/v1/chat/completions", "")
}
func applyClaude(payload string) []byte {
	return ApplyPromptRules("claude", "claude-sonnet-4-5", []byte(payload), "/v1/messages", "")
}
func applyResponses(payload string) []byte {
	return ApplyPromptRules("openai-response", "gpt-5", []byte(payload), "/v1/responses", "")
}
func applyGemini(payload string) []byte {
	return ApplyPromptRules("gemini", "gemini-3-pro", []byte(payload), "/v1beta/models/gemini-3-pro:generateContent", "")
}

// === Engine-level tests ===

func TestPromptRules_NoRules_PayloadUnchanged(t *testing.T) {
	UpdatePromptRulesSnapshot(nil)
	in := `{"messages":[{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	if string(out) != in {
		t.Fatalf("expected unchanged payload; got %s", string(out))
	}
}

func TestPromptRules_DisabledRuleSkipped(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name:    "noop",
		Enabled: false,
		Target:  "system",
		Action:  "inject",
		Content: "<!-- pr:x --> hi",
		Marker:  "<!-- pr:x -->",
	}})
	in := `{"messages":[{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	if strings.Contains(string(out), "<!-- pr:x -->") {
		t.Fatalf("disabled rule should not have fired: %s", string(out))
	}
}

func TestPromptRules_EmptyModels_MatchesAll(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name:    "match-all",
		Enabled: true,
		Target:  "system",
		Action:  "inject",
		Content: "<!-- pr:m --> code in JSON.",
		Marker:  "<!-- pr:m -->",
	}})
	out := applyOpenAI(`{"messages":[{"role":"user","content":"hi"}]}`)
	// gjson unescapes the JSON-escaped < and > so the marker substring matches.
	if !strings.Contains(gjson.GetBytes(out, "messages.0.content").String(), "<!-- pr:m -->") {
		t.Fatalf("empty models should match all; output: %s", string(out))
	}
}

func TestPromptRules_ModelProtocolFilter(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name:    "claude-only",
		Enabled: true,
		Models: []config.PayloadModelRule{
			{Name: "*", Protocol: "claude"},
		},
		Target:  "system",
		Action:  "inject",
		Content: "<!-- pr:c --> only claude",
		Marker:  "<!-- pr:c -->",
	}})
	// openai source format does not match — rule skipped
	out := applyOpenAI(`{"messages":[{"role":"user","content":"hi"}]}`)
	if strings.Contains(string(out), "<!-- pr:c -->") {
		t.Fatalf("rule scoped to claude must not fire on openai; got: %s", string(out))
	}
	// claude source format matches — rule fires
	out = applyClaude(`{"messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(string(out), "<!-- pr:c -->") {
		t.Fatalf("rule scoped to claude must fire on claude; got: %s", string(out))
	}
}

func TestPromptRules_RequestPathSkip_ImageEndpoint(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "match-all", Enabled: true, Target: "system", Action: "inject",
		Content: "<!-- pr:i --> hi", Marker: "<!-- pr:i -->",
	}})
	out := ApplyPromptRules("openai", "gpt-5", []byte(`{"messages":[]}`), "/v1/images/generations", "")
	if strings.Contains(string(out), "<!-- pr:i -->") {
		t.Fatalf("image endpoint should be skipped; got: %s", string(out))
	}
}

func TestPromptRules_AltSkip_Compact(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "match-all", Enabled: true, Target: "system", Action: "inject",
		Content: "<!-- pr:c2 --> hi", Marker: "<!-- pr:c2 -->",
	}})
	out := ApplyPromptRules("openai-response", "gpt-5", []byte(`{"input":""}`), "/v1/responses/compact", "responses/compact")
	if strings.Contains(string(out), "<!-- pr:c2 -->") {
		t.Fatalf("compact alt should be skipped; got: %s", string(out))
	}
}

// === OpenAI source format ===

func TestPromptRules_OpenAI_Inject_System_String(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "<!-- pr:s --> JSON only.", Marker: "<!-- pr:s -->",
		Position: "append",
	}})
	in := `{"messages":[{"role":"system","content":"You are helpful."},{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	got := gjson.GetBytes(out, "messages.0.content").String()
	if got != "You are helpful.<!-- pr:s --> JSON only." {
		t.Fatalf("unexpected system content: %q", got)
	}
}

func TestPromptRules_OpenAI_Inject_System_PrependsWhenAbsent(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "<!-- pr:s --> Be terse.", Marker: "<!-- pr:s -->",
	}})
	in := `{"messages":[{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	first := gjson.GetBytes(out, "messages.0")
	if first.Get("role").String() != "system" {
		t.Fatalf("expected first message role=system; got role=%s", first.Get("role").String())
	}
	if !strings.Contains(first.Get("content").String(), "<!-- pr:s -->") {
		t.Fatalf("expected marker in first message content; got: %s", first.Get("content").String())
	}
	// User message preserved at index 1
	if gjson.GetBytes(out, "messages.1.role").String() != "user" {
		t.Fatalf("user message should be preserved at index 1: %s", string(out))
	}
}

func TestPromptRules_OpenAI_Inject_LastUserMessage_StringContent(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "user", Enabled: true, Target: "user", Action: "inject",
		Content: "<!-- pr:u --> Reply concisely.", Marker: "<!-- pr:u -->",
		Position: "append",
	}})
	in := `{"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"ok"},{"role":"user","content":"second"}]}`
	out := applyOpenAI(in)
	if got := gjson.GetBytes(out, "messages.0.content").String(); got != "first" {
		t.Fatalf("first user message must not be modified; got %q", got)
	}
	if got := gjson.GetBytes(out, "messages.2.content").String(); got != "second<!-- pr:u --> Reply concisely." {
		t.Fatalf("last user message expected to be appended; got %q", got)
	}
}

func TestPromptRules_OpenAI_Inject_LastUserMessage_ArrayContent(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "user", Enabled: true, Target: "user", Action: "inject",
		Content: "<!-- pr:ua --> done", Marker: "<!-- pr:ua -->",
		Position: "append",
	}})
	in := `{"messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image_url","image_url":{"url":"x"}}]}]}`
	out := applyOpenAI(in)
	blocks := gjson.GetBytes(out, "messages.0.content").Array()
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks after append (text, image, new-text); got %d in %s", len(blocks), string(out))
	}
	last := blocks[len(blocks)-1]
	if last.Get("type").String() != "text" || last.Get("text").String() != "<!-- pr:ua --> done" {
		t.Fatalf("last block should be inject; got %s", last.Raw)
	}
}

func TestPromptRules_OpenAI_Skip_ToolMessage(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "user", Enabled: true, Target: "user", Action: "inject",
		Content: "<!-- pr:nope --> x", Marker: "<!-- pr:nope -->",
	}})
	// Last message is a tool message — should be skipped, but the second-to-last
	// user message is natural-language and should receive the inject.
	in := `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","tool_calls":[{"id":"c","function":{"name":"f"}}]},{"role":"tool","tool_call_id":"c","content":"42"}]}`
	out := applyOpenAI(in)
	// The tool message must not contain marker
	tool := gjson.GetBytes(out, "messages.2.content").String()
	if strings.Contains(tool, "<!-- pr:nope -->") {
		t.Fatalf("tool message must not be modified; got %q", tool)
	}
	// The user message at index 0 receives the inject
	first := gjson.GetBytes(out, "messages.0.content").String()
	if !strings.Contains(first, "<!-- pr:nope -->") {
		t.Fatalf("expected last natural-language user message to receive inject; got %q", first)
	}
}

func TestPromptRules_OpenAI_Inject_Idempotent(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "<!-- pr:idm --> JSON only.", Marker: "<!-- pr:idm -->",
	}})
	// Marker already in system content — should be no-op.
	in := `{"messages":[{"role":"system","content":"prelude <!-- pr:idm --> postlude"},{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	got := gjson.GetBytes(out, "messages.0.content").String()
	if got != "prelude <!-- pr:idm --> postlude" {
		t.Fatalf("idempotent inject should leave content untouched; got %q", got)
	}
}

func TestPromptRules_OpenAI_Strip_System(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "buddy", Enabled: true, Target: "system", Action: "strip",
		Pattern: `Buddy[^\n]*\n?`,
	}})
	in := `{"messages":[{"role":"system","content":"Buddy is a coding assistant.\nYou are helpful."},{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	got := gjson.GetBytes(out, "messages.0.content").String()
	if got != "You are helpful." {
		t.Fatalf("strip should remove buddy line; got %q", got)
	}
}

// === OpenAI Responses source format ===

func TestPromptRules_Responses_Inject_Instructions(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "<!-- pr:r --> Be brief.", Marker: "<!-- pr:r -->",
	}})
	in := `{"instructions":"Original.","input":"hi"}`
	out := applyResponses(in)
	got := gjson.GetBytes(out, "instructions").String()
	if got != "Original.<!-- pr:r --> Be brief." {
		t.Fatalf("unexpected instructions: %q", got)
	}
}

func TestPromptRules_Responses_Inject_Instructions_Creates(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "<!-- pr:r --> Be brief.", Marker: "<!-- pr:r -->",
	}})
	in := `{"input":"hi"}`
	out := applyResponses(in)
	if got := gjson.GetBytes(out, "instructions").String(); got != "<!-- pr:r --> Be brief." {
		t.Fatalf("expected instructions to be created with content; got %q", got)
	}
}

func TestPromptRules_Responses_Inject_LastUserInputString(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "user", Enabled: true, Target: "user", Action: "inject",
		Content: "<!-- pr:ru --> ok", Marker: "<!-- pr:ru -->",
	}})
	in := `{"input":"original"}`
	out := applyResponses(in)
	if got := gjson.GetBytes(out, "input").String(); got != "original<!-- pr:ru --> ok" {
		t.Fatalf("unexpected input: %q", got)
	}
}

func TestPromptRules_Responses_Inject_LastUserInputArray(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "user", Enabled: true, Target: "user", Action: "inject",
		Content: "<!-- pr:ria --> done", Marker: "<!-- pr:ria -->",
	}})
	in := `{"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`
	out := applyResponses(in)
	blocks := gjson.GetBytes(out, "input.0.content").Array()
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks; got %d in %s", len(blocks), string(out))
	}
	last := blocks[len(blocks)-1]
	if last.Get("type").String() != "input_text" || last.Get("text").String() != "<!-- pr:ria --> done" {
		t.Fatalf("last block should be inject; got %s", last.Raw)
	}
}

// === Claude source format ===

func TestPromptRules_Claude_Inject_System_String(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "<!-- pr:cs --> JSON.", Marker: "<!-- pr:cs -->",
	}})
	in := `{"system":"You are helpful.","messages":[{"role":"user","content":"hi"}]}`
	out := applyClaude(in)
	if got := gjson.GetBytes(out, "system").String(); got != "You are helpful.<!-- pr:cs --> JSON." {
		t.Fatalf("unexpected system: %q", got)
	}
}

func TestPromptRules_Claude_Inject_System_BlockArray(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "<!-- pr:cb --> JSON.", Marker: "<!-- pr:cb -->",
	}})
	in := `{"system":[{"type":"text","text":"You are helpful."}],"messages":[{"role":"user","content":"hi"}]}`
	out := applyClaude(in)
	blocks := gjson.GetBytes(out, "system").Array()
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks after append; got %d in %s", len(blocks), string(out))
	}
	if blocks[1].Get("text").String() != "<!-- pr:cb --> JSON." {
		t.Fatalf("unexpected appended block: %s", blocks[1].Raw)
	}
}

func TestPromptRules_Claude_Inject_System_CreatesIfMissing(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "<!-- pr:cn --> JSON.", Marker: "<!-- pr:cn -->",
	}})
	in := `{"messages":[{"role":"user","content":"hi"}]}`
	out := applyClaude(in)
	if got := gjson.GetBytes(out, "system").String(); got != "<!-- pr:cn --> JSON." {
		t.Fatalf("expected system created with content; got %q", got)
	}
}

func TestPromptRules_Claude_Skip_ToolResultUserMessage(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "user", Enabled: true, Target: "user", Action: "inject",
		Content: "<!-- pr:cu --> x", Marker: "<!-- pr:cu -->",
	}})
	// Last user message has only tool_result blocks — should be skipped.
	// The earlier user message (index 0) should receive the inject.
	in := `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"c","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c","content":"42"}]}]}`
	out := applyClaude(in)
	// Last message untouched
	last := gjson.GetBytes(out, "messages.2.content").Raw
	if strings.Contains(last, "<!-- pr:cu -->") {
		t.Fatalf("tool-result message should not be modified; got %s", last)
	}
	// First user message receives inject
	first := gjson.GetBytes(out, "messages.0.content").String()
	if !strings.Contains(first, "<!-- pr:cu -->") {
		t.Fatalf("expected last natural-language user (index 0) to receive inject; got %q", first)
	}
}

// === Gemini source format ===

func TestPromptRules_Gemini_Inject_SystemInstruction_CamelCase(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "<!-- pr:gc --> JSON.", Marker: "<!-- pr:gc -->",
	}})
	in := `{"systemInstruction":{"role":"system","parts":[{"text":"You are helpful."}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	out := applyGemini(in)
	parts := gjson.GetBytes(out, "systemInstruction.parts").Array()
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts after append; got %d in %s", len(parts), string(out))
	}
	if parts[1].Get("text").String() != "<!-- pr:gc --> JSON." {
		t.Fatalf("unexpected appended part: %s", parts[1].Raw)
	}
}

func TestPromptRules_Gemini_Inject_SystemInstruction_SnakeCase(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "<!-- pr:gs --> JSON.", Marker: "<!-- pr:gs -->",
	}})
	in := `{"system_instruction":{"role":"system","parts":[{"text":"You are helpful."}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	out := applyGemini(in)
	parts := gjson.GetBytes(out, "system_instruction.parts").Array()
	if len(parts) != 2 {
		t.Fatalf("snake_case branch must mutate same field; got %d parts in %s", len(parts), string(out))
	}
}

func TestPromptRules_Gemini_Inject_SystemInstruction_CreatesIfMissing(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "<!-- pr:gn --> JSON.", Marker: "<!-- pr:gn -->",
	}})
	in := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	out := applyGemini(in)
	got := gjson.GetBytes(out, "systemInstruction.parts.0.text").String()
	if got != "<!-- pr:gn --> JSON." {
		t.Fatalf("expected camelCase systemInstruction created; got %q in %s", got, string(out))
	}
}

func TestPromptRules_Gemini_Skip_FunctionResponseUserMessage(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "user", Enabled: true, Target: "user", Action: "inject",
		Content: "<!-- pr:gu --> x", Marker: "<!-- pr:gu -->",
	}})
	in := `{"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"model","parts":[{"functionCall":{"name":"f","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"f","response":{}}}]}]}`
	out := applyGemini(in)
	// Last contents item (functionResponse) untouched
	last := gjson.GetBytes(out, "contents.2.parts").Raw
	if strings.Contains(last, "<!-- pr:gu -->") {
		t.Fatalf("functionResponse message should not be modified; got %s", last)
	}
	// First user contents receives inject
	first := gjson.GetBytes(out, "contents.0.parts").Array()
	if len(first) != 2 {
		t.Fatalf("expected first user contents to gain a part; got %d in %s", len(first), string(out))
	}
}

// === Ordering & idempotency ===

func TestPromptRules_StripBeforeInject_PreservesNewInject(t *testing.T) {
	// A strip that would have removed the marker if it ran AFTER inject.
	// Strip runs first, then inject — so the new inject survives.
	withPromptRules(t, []config.PromptRule{
		{
			Name: "strip-marker", Enabled: true, Target: "system", Action: "strip",
			Pattern: `<!-- pr:keep -->[^\n]*`,
		},
		{
			Name: "inject", Enabled: true, Target: "system", Action: "inject",
			Content: "<!-- pr:keep --> stays.", Marker: "<!-- pr:keep -->",
		},
	})
	in := `{"messages":[{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	sys := gjson.GetBytes(out, "messages.0.content").String()
	if !strings.Contains(sys, "<!-- pr:keep -->") {
		t.Fatalf("inject after strip should produce the marker; got %q", sys)
	}
}

func TestPromptRules_StripRemovesMarker_RecreatesContent(t *testing.T) {
	// Documents the deliberate edge-case: if a strip rule removes the marker
	// from existing injected content (but not the rest), the next request will
	// inject again. This codifies the "documented warning" from the plan.
	withPromptRules(t, []config.PromptRule{
		{
			Name: "strip-marker", Enabled: true, Target: "system", Action: "strip",
			Pattern: `<!-- pr:dup -->`, // strips marker only
		},
		{
			Name: "inject", Enabled: true, Target: "system", Action: "inject",
			Content: "<!-- pr:dup --> stays.", Marker: "<!-- pr:dup -->",
		},
	})
	in := `{"messages":[{"role":"system","content":"<!-- pr:dup --> stays."},{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	sys := gjson.GetBytes(out, "messages.0.content").String()
	// Strip removes marker; inject finds no marker; injects again. Result:
	// " stays.<!-- pr:dup --> stays."
	if !strings.Contains(sys, "<!-- pr:dup --> stays.") {
		t.Fatalf("expected duplicate content after strip-then-inject; got %q", sys)
	}
}

func TestPromptRules_Idempotency_MarkerInOtherTargetDoesNotBlock(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "<!-- pr:scope --> sys.", Marker: "<!-- pr:scope -->",
	}})
	// Marker present in user content but not in system — system inject should still fire.
	in := `{"messages":[{"role":"user","content":"<!-- pr:scope --> embedded"}]}`
	out := applyOpenAI(in)
	first := gjson.GetBytes(out, "messages.0").Get("role").String()
	if first != "system" {
		t.Fatalf("expected new system message at index 0; got role=%s in %s", first, string(out))
	}
}

// === gemini-cli rooted under request.* ===

func applyGeminiCLI(payload string) []byte {
	return ApplyPromptRules("gemini-cli", "gemini-3-pro", []byte(payload), "/v1internal:generateContent", "")
}

func TestPromptRules_GeminiCLI_Inject_System_NestedRequest(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "<!-- pr:gci --> JSON.", Marker: "<!-- pr:gci -->",
	}})
	in := `{"request":{"systemInstruction":{"role":"system","parts":[{"text":"You are helpful."}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`
	out := applyGeminiCLI(in)
	parts := gjson.GetBytes(out, "request.systemInstruction.parts").Array()
	if len(parts) != 2 {
		t.Fatalf("expected systemInstruction under request.* to gain a part; got %d in %s", len(parts), string(out))
	}
	if parts[1].Get("text").String() != "<!-- pr:gci --> JSON." {
		t.Fatalf("unexpected appended part: %s", parts[1].Raw)
	}
}

func TestPromptRules_GeminiCLI_Inject_LastUser_NestedRequest(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "user", Enabled: true, Target: "user", Action: "inject",
		Content: "<!-- pr:gcu --> done", Marker: "<!-- pr:gcu -->",
	}})
	in := `{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`
	out := applyGeminiCLI(in)
	parts := gjson.GetBytes(out, "request.contents.0.parts").Array()
	if len(parts) != 2 {
		t.Fatalf("expected user parts to gain a text part; got %d in %s", len(parts), string(out))
	}
}

// Plain "gemini" must NOT match request.* paths — confirms the two formats are
// dispatched to different handlers.
func TestPromptRules_PlainGemini_DoesNotTouchNestedRequest(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "<!-- pr:pg --> hi", Marker: "<!-- pr:pg -->",
	}})
	in := `{"request":{"systemInstruction":{"role":"system","parts":[{"text":"x"}]}}}`
	out := applyGemini(in)
	nestedParts := gjson.GetBytes(out, "request.systemInstruction.parts").Array()
	if len(nestedParts) != 1 {
		t.Fatalf("plain gemini handler must not modify request.systemInstruction; got %d parts", len(nestedParts))
	}
	if !gjson.GetBytes(out, "systemInstruction").Exists() {
		t.Fatalf("plain gemini handler should have created top-level systemInstruction; got %s", string(out))
	}
}

// === Edge: null content ===

func TestPromptRules_OpenAI_Inject_System_ContentNull(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "<!-- pr:n --> hi", Marker: "<!-- pr:n -->",
	}})
	in := `{"messages":[{"role":"system","content":null},{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	got := gjson.GetBytes(out, "messages.0.content").String()
	if got != "<!-- pr:n --> hi" {
		t.Fatalf("null content should be replaced with injected string; got %q", got)
	}
}

// === Edge: empty text in array block ===

func TestPromptRules_OpenAI_LastUser_EmptyTextBlockSkipped(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "user", Enabled: true, Target: "user", Action: "inject",
		Content: "<!-- pr:e --> ok", Marker: "<!-- pr:e -->",
	}})
	in := `{"messages":[{"role":"user","content":"first"},{"role":"user","content":[{"type":"text","text":""}]}]}`
	out := applyOpenAI(in)
	first := gjson.GetBytes(out, "messages.0.content").String()
	if !strings.Contains(first, "<!-- pr:e -->") {
		t.Fatalf("expected first user (only natural-language one) to receive inject; got %q", first)
	}
}

// === Snapshot clearing on empty input ===

func TestPromptRules_Sanitize_EmptyClearsSnapshot(t *testing.T) {
	UpdatePromptRulesSnapshot([]config.PromptRule{{
		Name: "x", Enabled: true, Target: "system", Action: "inject",
		Content: "<!-- pr:c --> hi", Marker: "<!-- pr:c -->", Position: "append",
	}})
	t.Cleanup(func() { UpdatePromptRulesSnapshot(nil) })
	cfg := &config.Config{PromptRules: nil}
	cfg.SanitizePromptRules()
	out := applyOpenAI(`{"messages":[{"role":"user","content":"hi"}]}`)
	if strings.Contains(gjson.GetBytes(out, "messages.0.content").String(), "<!-- pr:c -->") {
		t.Fatalf("empty Sanitize must clear snapshot; rule still fired in %s", string(out))
	}
}

// === Allowed-protocol set ===

func TestPromptRules_AllowedProtocols_RejectsUnknown(t *testing.T) {
	if IsAllowedPromptRuleProtocol("bogus") {
		t.Fatal("bogus protocol should not be allowed")
	}
	if !IsAllowedPromptRuleProtocol("") {
		t.Fatal("empty protocol must be allowed (means any)")
	}
	for _, p := range AllowedPromptRuleProtocols {
		if !IsAllowedPromptRuleProtocol(p) {
			t.Fatalf("canonical protocol %q must be allowed", p)
		}
	}
}

// === Concurrency ===

func TestPromptRules_ConcurrentSnapshotRebuild(t *testing.T) {
	// Race-detector run: many readers calling ApplyPromptRules while a writer
	// rebuilds the snapshot. The atomic.Pointer should make this safe.
	rulesA := []config.PromptRule{{
		Name: "a", Enabled: true, Target: "system", Action: "inject",
		Content: "<!-- pr:a --> a.", Marker: "<!-- pr:a -->", Position: "append",
	}}
	rulesB := []config.PromptRule{{
		Name: "b", Enabled: true, Target: "system", Action: "strip",
		Pattern: `b+`,
	}}
	UpdatePromptRulesSnapshot(rulesA)
	t.Cleanup(func() { UpdatePromptRulesSnapshot(nil) })

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := []byte(`{"messages":[{"role":"user","content":"bbbb"}]}`)
			for {
				select {
				case <-stop:
					return
				default:
					_ = ApplyPromptRules("openai", "gpt-5", body, "/v1/chat/completions", "")
				}
			}
		}()
	}
	for i := 0; i < 1000; i++ {
		if i%2 == 0 {
			UpdatePromptRulesSnapshot(rulesA)
		} else {
			UpdatePromptRulesSnapshot(rulesB)
		}
	}
	close(stop)
	wg.Wait()
}
