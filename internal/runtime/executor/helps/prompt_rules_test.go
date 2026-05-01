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
func applyGeminiCLI(payload string) []byte {
	return ApplyPromptRules("gemini-cli", "gemini-3-pro", []byte(payload), "/v1internal:generateContent", "")
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
		Content: " disabled.",
	}})
	in := `{"messages":[{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	if strings.Contains(string(out), "disabled.") {
		t.Fatalf("disabled rule should not have fired: %s", string(out))
	}
}

func TestPromptRules_EmptyModels_MatchesAll(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name:    "match-all",
		Enabled: true,
		Target:  "system",
		Action:  "inject",
		Content: "Always answer in JSON.",
	}})
	out := applyOpenAI(`{"messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(gjson.GetBytes(out, "messages.0.content").String(), "Always answer in JSON.") {
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
		Content: "claude-only marker",
	}})
	// openai source format does not match — rule skipped
	out := applyOpenAI(`{"messages":[{"role":"user","content":"hi"}]}`)
	if strings.Contains(string(out), "claude-only marker") {
		t.Fatalf("rule scoped to claude must not fire on openai; got: %s", string(out))
	}
	// claude source format matches — rule fires
	out = applyClaude(`{"messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(string(out), "claude-only marker") {
		t.Fatalf("rule scoped to claude must fire on claude; got: %s", string(out))
	}
}

func TestPromptRules_RequestPathSkip_ImageEndpoint(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "match-all", Enabled: true, Target: "system", Action: "inject",
		Content: "image-endpoint-skip-test",
	}})
	out := ApplyPromptRules("openai", "gpt-5", []byte(`{"messages":[]}`), "/v1/images/generations", "")
	if strings.Contains(string(out), "image-endpoint-skip-test") {
		t.Fatalf("image endpoint should be skipped; got: %s", string(out))
	}
}

func TestPromptRules_AltSkip_Compact(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "match-all", Enabled: true, Target: "system", Action: "inject",
		Content: "compact-alt-skip-test",
	}})
	out := ApplyPromptRules("openai-response", "gpt-5", []byte(`{"input":""}`), "/v1/responses/compact", "responses/compact")
	if strings.Contains(string(out), "compact-alt-skip-test") {
		t.Fatalf("compact alt should be skipped; got: %s", string(out))
	}
}

// === OpenAI source format ===

func TestPromptRules_OpenAI_Inject_System_String_Boundary(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: " JSON only.", Position: "append",
	}})
	in := `{"messages":[{"role":"system","content":"You are helpful."},{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	got := gjson.GetBytes(out, "messages.0.content").String()
	if got != "You are helpful. JSON only." {
		t.Fatalf("unexpected system content: %q", got)
	}
}

func TestPromptRules_OpenAI_Inject_System_PrependsWhenAbsent(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "Be terse.",
	}})
	in := `{"messages":[{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	first := gjson.GetBytes(out, "messages.0")
	if first.Get("role").String() != "system" {
		t.Fatalf("expected first message role=system; got role=%s", first.Get("role").String())
	}
	if !strings.Contains(first.Get("content").String(), "Be terse.") {
		t.Fatalf("expected synthesized system content; got: %s", first.Get("content").String())
	}
	if gjson.GetBytes(out, "messages.1.role").String() != "user" {
		t.Fatalf("user message should be preserved at index 1: %s", string(out))
	}
}

func TestPromptRules_OpenAI_MarkerMode_NoSystem_Skips(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: " (proxy)", Marker: "helpful",
	}})
	// No system message exists — marker mode has no anchor, so no synthesis.
	in := `{"messages":[{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	if first := gjson.GetBytes(out, "messages.0").Get("role").String(); first == "system" {
		t.Fatalf("marker mode must not synthesize a system message; got role=%s in %s", first, string(out))
	}
	if string(out) != in {
		t.Fatalf("expected unchanged payload; got %s", string(out))
	}
}

func TestPromptRules_OpenAI_Inject_LastUserMessage_StringContent(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "user", Enabled: true, Target: "user", Action: "inject",
		Content: " Reply concisely.", Position: "append",
	}})
	in := `{"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"ok"},{"role":"user","content":"second"}]}`
	out := applyOpenAI(in)
	if got := gjson.GetBytes(out, "messages.0.content").String(); got != "first" {
		t.Fatalf("first user message must not be modified; got %q", got)
	}
	if got := gjson.GetBytes(out, "messages.2.content").String(); got != "second Reply concisely." {
		t.Fatalf("last user message expected to be appended; got %q", got)
	}
}

func TestPromptRules_OpenAI_Inject_LastUserMessage_ArrayContent(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "user", Enabled: true, Target: "user", Action: "inject",
		Content: "done", Position: "append",
	}})
	in := `{"messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image_url","image_url":{"url":"x"}}]}]}`
	out := applyOpenAI(in)
	blocks := gjson.GetBytes(out, "messages.0.content").Array()
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks after append (text, image, new-text); got %d in %s", len(blocks), string(out))
	}
	last := blocks[len(blocks)-1]
	if last.Get("type").String() != "text" || last.Get("text").String() != "done" {
		t.Fatalf("last block should be inject; got %s", last.Raw)
	}
}

func TestPromptRules_OpenAI_Skip_ToolMessage(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "user", Enabled: true, Target: "user", Action: "inject",
		Content: " (extra)",
	}})
	// Last message is a tool message — should be skipped, but the second-to-last
	// user message is natural-language and should receive the inject.
	in := `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","tool_calls":[{"id":"c","function":{"name":"f"}}]},{"role":"tool","tool_call_id":"c","content":"42"}]}`
	out := applyOpenAI(in)
	tool := gjson.GetBytes(out, "messages.2.content").String()
	if strings.Contains(tool, "(extra)") {
		t.Fatalf("tool message must not be modified; got %q", tool)
	}
	first := gjson.GetBytes(out, "messages.0.content").String()
	if !strings.Contains(first, "(extra)") {
		t.Fatalf("expected last natural-language user message to receive inject; got %q", first)
	}
}

func TestPromptRules_OpenAI_BoundaryIdempotent(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "JSON only.",
	}})
	// Content already present in system → boundary idempotency skips.
	in := `{"messages":[{"role":"system","content":"prelude JSON only. postlude"},{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	got := gjson.GetBytes(out, "messages.0.content").String()
	if got != "prelude JSON only. postlude" {
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
		Content: " Be brief.",
	}})
	in := `{"instructions":"Original.","input":"hi"}`
	out := applyResponses(in)
	got := gjson.GetBytes(out, "instructions").String()
	if got != "Original. Be brief." {
		t.Fatalf("unexpected instructions: %q", got)
	}
}

func TestPromptRules_Responses_Inject_Instructions_Creates(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "Be brief.",
	}})
	in := `{"input":"hi"}`
	out := applyResponses(in)
	if got := gjson.GetBytes(out, "instructions").String(); got != "Be brief." {
		t.Fatalf("expected instructions to be created with content; got %q", got)
	}
}

func TestPromptRules_Responses_MarkerMode_NoInstructions_Skips(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: " (proxy)", Marker: "anchor",
	}})
	in := `{"input":"hi"}`
	out := applyResponses(in)
	if gjson.GetBytes(out, "instructions").Exists() {
		t.Fatalf("marker mode must not synthesize instructions; got %s", string(out))
	}
}

func TestPromptRules_Responses_Inject_LastUserInputString(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "user", Enabled: true, Target: "user", Action: "inject",
		Content: " ok",
	}})
	in := `{"input":"original"}`
	out := applyResponses(in)
	if got := gjson.GetBytes(out, "input").String(); got != "original ok" {
		t.Fatalf("unexpected input: %q", got)
	}
}

func TestPromptRules_Responses_Inject_LastUserInputArray(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "user", Enabled: true, Target: "user", Action: "inject",
		Content: "done",
	}})
	in := `{"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`
	out := applyResponses(in)
	blocks := gjson.GetBytes(out, "input.0.content").Array()
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks; got %d in %s", len(blocks), string(out))
	}
	last := blocks[len(blocks)-1]
	if last.Get("type").String() != "input_text" || last.Get("text").String() != "done" {
		t.Fatalf("last block should be inject; got %s", last.Raw)
	}
}

// === Claude source format ===

func TestPromptRules_Claude_Inject_System_String(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: " JSON.",
	}})
	in := `{"system":"You are helpful.","messages":[{"role":"user","content":"hi"}]}`
	out := applyClaude(in)
	if got := gjson.GetBytes(out, "system").String(); got != "You are helpful. JSON." {
		t.Fatalf("unexpected system: %q", got)
	}
}

func TestPromptRules_Claude_Inject_System_BlockArray(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "JSON.",
	}})
	in := `{"system":[{"type":"text","text":"You are helpful."}],"messages":[{"role":"user","content":"hi"}]}`
	out := applyClaude(in)
	blocks := gjson.GetBytes(out, "system").Array()
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks after append; got %d in %s", len(blocks), string(out))
	}
	if blocks[1].Get("text").String() != "JSON." {
		t.Fatalf("unexpected appended block: %s", blocks[1].Raw)
	}
}

func TestPromptRules_Claude_Inject_System_CreatesIfMissing(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "JSON.",
	}})
	in := `{"messages":[{"role":"user","content":"hi"}]}`
	out := applyClaude(in)
	if got := gjson.GetBytes(out, "system").String(); got != "JSON." {
		t.Fatalf("expected system created with content; got %q", got)
	}
}

func TestPromptRules_Claude_MarkerMode_NoSystem_Skips(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: " (proxy)", Marker: "helpful",
	}})
	in := `{"messages":[{"role":"user","content":"hi"}]}`
	out := applyClaude(in)
	if gjson.GetBytes(out, "system").Exists() {
		t.Fatalf("marker mode must not synthesize claude system; got %s", string(out))
	}
}

func TestPromptRules_Claude_Skip_ToolResultUserMessage(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "user", Enabled: true, Target: "user", Action: "inject",
		Content: " x",
	}})
	in := `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"c","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c","content":"42"}]}]}`
	out := applyClaude(in)
	last := gjson.GetBytes(out, "messages.2.content").Raw
	if strings.Contains(last, " x") {
		t.Fatalf("tool-result message should not be modified; got %s", last)
	}
	first := gjson.GetBytes(out, "messages.0.content").String()
	if first != "hi x" {
		t.Fatalf("expected last natural-language user (index 0) to receive inject; got %q", first)
	}
}

// === Gemini source format ===

func TestPromptRules_Gemini_Inject_SystemInstruction_CamelCase(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "JSON.",
	}})
	in := `{"systemInstruction":{"role":"system","parts":[{"text":"You are helpful."}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	out := applyGemini(in)
	parts := gjson.GetBytes(out, "systemInstruction.parts").Array()
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts after append; got %d in %s", len(parts), string(out))
	}
	if parts[1].Get("text").String() != "JSON." {
		t.Fatalf("unexpected appended part: %s", parts[1].Raw)
	}
}

func TestPromptRules_Gemini_Inject_SystemInstruction_SnakeCase(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "JSON.",
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
		Content: "JSON.",
	}})
	in := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	out := applyGemini(in)
	got := gjson.GetBytes(out, "systemInstruction.parts.0.text").String()
	if got != "JSON." {
		t.Fatalf("expected camelCase systemInstruction created; got %q in %s", got, string(out))
	}
}

func TestPromptRules_Gemini_MarkerMode_NoSystem_Skips(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: " (proxy)", Marker: "helpful",
	}})
	in := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	out := applyGemini(in)
	if gjson.GetBytes(out, "systemInstruction").Exists() || gjson.GetBytes(out, "system_instruction").Exists() {
		t.Fatalf("marker mode must not synthesize gemini systemInstruction; got %s", string(out))
	}
}

func TestPromptRules_Gemini_Skip_FunctionResponseUserMessage(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "user", Enabled: true, Target: "user", Action: "inject",
		Content: " x",
	}})
	in := `{"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"model","parts":[{"functionCall":{"name":"f","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"f","response":{}}}]}]}`
	out := applyGemini(in)
	last := gjson.GetBytes(out, "contents.2.parts").Raw
	if strings.Contains(last, " x") {
		t.Fatalf("functionResponse message should not be modified; got %s", last)
	}
	first := gjson.GetBytes(out, "contents.0.parts").Array()
	if len(first) != 2 {
		t.Fatalf("expected first user contents to gain a part; got %d in %s", len(first), string(out))
	}
}

// === Ordering & idempotency ===

func TestPromptRules_StripBeforeInject_PreservesNewInject(t *testing.T) {
	// Strip removes the to-be-injected content if it pre-exists; inject runs
	// after strip and re-creates content. Verifies pass ordering.
	withPromptRules(t, []config.PromptRule{
		{
			Name: "strip-keep", Enabled: true, Target: "system", Action: "strip",
			Pattern: `keep[^\n]*`,
		},
		{
			Name: "inject", Enabled: true, Target: "system", Action: "inject",
			Content: "keep this.",
		},
	})
	in := `{"messages":[{"role":"system","content":"keep prior"},{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	sys := gjson.GetBytes(out, "messages.0.content").String()
	if !strings.Contains(sys, "keep this.") {
		t.Fatalf("inject after strip should produce content; got %q", sys)
	}
}

func TestPromptRules_StripBeforeInject_BoundaryReinjects(t *testing.T) {
	// In v2 boundary mode, if a prior inject's content gets fully stripped,
	// the next request re-injects (boundary idempotency is content-based).
	withPromptRules(t, []config.PromptRule{
		{
			Name: "strip-content", Enabled: true, Target: "system", Action: "strip",
			Pattern: `keep this\.`,
		},
		{
			Name: "inject", Enabled: true, Target: "system", Action: "inject",
			Content: "keep this.",
		},
	})
	in := `{"messages":[{"role":"system","content":"prelude keep this. postlude"},{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	sys := gjson.GetBytes(out, "messages.0.content").String()
	if !strings.Contains(sys, "keep this.") {
		t.Fatalf("expected reinject after strip removed prior content; got %q", sys)
	}
}

// === v2 marker-anchor mode ===

func TestPromptRules_MarkerMode_Adjacent_Skip(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: " (proxy)", Marker: "qwen", Position: "append",
	}})
	// Content already directly after the marker — idempotent skip.
	in := `{"messages":[{"role":"system","content":"You are qwen (proxy)-99."},{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	got := gjson.GetBytes(out, "messages.0.content").String()
	if got != "You are qwen (proxy)-99." {
		t.Fatalf("adjacent content must be idempotent; got %q", got)
	}
}

func TestPromptRules_MarkerMode_NotAdjacent_InjectsAtFirst(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: " (proxy)", Marker: "qwen", Position: "append",
	}})
	in := `{"messages":[{"role":"system","content":"You are qwen-99."},{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	got := gjson.GetBytes(out, "messages.0.content").String()
	if got != "You are qwen (proxy)-99." {
		t.Fatalf("first run should inject after marker; got %q", got)
	}
	out2 := applyOpenAI(string(out))
	got2 := gjson.GetBytes(out2, "messages.0.content").String()
	if got2 != got {
		t.Fatalf("second run must be idempotent; got %q vs %q", got, got2)
	}
}

func TestPromptRules_MarkerMode_PrependBefore_Adjacent(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "[proxy] ", Marker: "qwen", Position: "prepend",
	}})
	// Content already directly before marker — skip.
	in := `{"messages":[{"role":"system","content":"You are [proxy] qwen."},{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	got := gjson.GetBytes(out, "messages.0.content").String()
	if got != "You are [proxy] qwen." {
		t.Fatalf("prepend adjacency must be idempotent; got %q", got)
	}
}

func TestPromptRules_MarkerMode_PrependBefore_NotAdjacent(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "[proxy] ", Marker: "qwen", Position: "prepend",
	}})
	in := `{"messages":[{"role":"system","content":"You are qwen."},{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	got := gjson.GetBytes(out, "messages.0.content").String()
	if got != "You are [proxy] qwen." {
		t.Fatalf("prepend should land immediately before marker; got %q", got)
	}
}

func TestPromptRules_MarkerMode_AbsentMarker_Skip(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: " (proxy)", Marker: "qwen",
	}})
	in := `{"messages":[{"role":"system","content":"You are something else."},{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	if string(out) != in {
		t.Fatalf("marker mode without anchor must skip; got %s", string(out))
	}
}

func TestPromptRules_MarkerMode_MultiOccurrence_InjectsAtFirst(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "*", Marker: "X", Position: "append",
	}})
	in := `{"messages":[{"role":"system","content":"X.X.X"},{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	got := gjson.GetBytes(out, "messages.0.content").String()
	if got != "X*.X.X" {
		t.Fatalf("expected inject after FIRST occurrence only; got %q", got)
	}
	// Re-run: first occurrence now has content adjacent → skip
	out2 := applyOpenAI(string(out))
	if gjson.GetBytes(out2, "messages.0.content").String() != got {
		t.Fatalf("multi-occurrence run must be idempotent")
	}
}

func TestPromptRules_MarkerMode_MultiOccurrence_AnyAdjacencySuppresses(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "*", Marker: "X", Position: "append",
	}})
	// Last occurrence already has content adjacent — whole rule no-ops.
	in := `{"messages":[{"role":"system","content":"X.X.X*"},{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	got := gjson.GetBytes(out, "messages.0.content").String()
	if got != "X.X.X*" {
		t.Fatalf("any adjacency must suppress whole rule; got %q", got)
	}
}

func TestPromptRules_MarkerMode_OverlapWithContent_StableAcrossRuns(t *testing.T) {
	// marker="foo", content="foofoo": after first inject, the inserted text
	// itself contains marker substrings, so subsequent runs see adjacency and
	// skip. Documents the runaway-inject mitigation.
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "foofoo", Marker: "foo", Position: "append",
	}})
	in := `{"messages":[{"role":"system","content":"X foo Y"},{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	got := gjson.GetBytes(out, "messages.0.content").String()
	if got != "X foofoofoo Y" {
		t.Fatalf("first run unexpected: %q", got)
	}
	out2 := applyOpenAI(string(out))
	got2 := gjson.GetBytes(out2, "messages.0.content").String()
	if got2 != got {
		t.Fatalf("overlap inject must be stable across runs; got %q vs %q", got, got2)
	}
	out3 := applyOpenAI(string(out2))
	if gjson.GetBytes(out3, "messages.0.content").String() != got {
		t.Fatalf("overlap inject must remain stable on the third run too")
	}
}

func TestPromptRules_BlockArray_Marker_AdjacencyInOtherBlock_Suppresses(t *testing.T) {
	// Marker present in two text blocks. Block 1 already has content adjacent
	// → whole rule no-ops, block 0 stays untouched.
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "*", Marker: "anchor", Position: "append",
	}})
	in := `{"system":[{"type":"text","text":"anchor here"},{"type":"text","text":"anchor*"}],"messages":[{"role":"user","content":"hi"}]}`
	out := applyClaude(in)
	if string(out) != in {
		t.Fatalf("block-array adjacency in any block must suppress whole rule; got %s", string(out))
	}
}

func TestPromptRules_BlockArray_Marker_FirstBlockHasMarker_InjectsThere(t *testing.T) {
	// Marker in block 1 only. Inject lands inside that block.
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "*", Marker: "anchor", Position: "append",
	}})
	in := `{"system":[{"type":"text","text":"no marker here"},{"type":"text","text":"anchor X"}],"messages":[{"role":"user","content":"hi"}]}`
	out := applyClaude(in)
	blocks := gjson.GetBytes(out, "system").Array()
	if len(blocks) != 2 {
		t.Fatalf("inject must mutate existing block, not add a new one; got %d", len(blocks))
	}
	if blocks[0].Get("text").String() != "no marker here" {
		t.Fatalf("block 0 must be untouched; got %s", blocks[0].Raw)
	}
	if blocks[1].Get("text").String() != "anchor* X" {
		t.Fatalf("block 1 should have inject after marker; got %s", blocks[1].Raw)
	}
}

func TestPromptRules_BoundaryMode_BlockArrayContentPresent_Skip(t *testing.T) {
	// Boundary mode, content already in some block → skip.
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "JSON only.",
	}})
	in := `{"system":[{"type":"text","text":"prelude"},{"type":"text","text":"... JSON only. ..."}],"messages":[{"role":"user","content":"hi"}]}`
	out := applyClaude(in)
	if string(out) != in {
		t.Fatalf("boundary block-array idempotency: content present must skip; got %s", string(out))
	}
}

func TestPromptRules_BoundaryMode_BlockArrayContentAbsent_AppendsBlock(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "JSON only.",
	}})
	in := `{"system":[{"type":"text","text":"prelude"}],"messages":[{"role":"user","content":"hi"}]}`
	out := applyClaude(in)
	blocks := gjson.GetBytes(out, "system").Array()
	if len(blocks) != 2 {
		t.Fatalf("expected new text block appended; got %d in %s", len(blocks), string(out))
	}
	if blocks[1].Get("text").String() != "JSON only." {
		t.Fatalf("appended block content mismatch: %s", blocks[1].Raw)
	}
}

func TestPromptRules_BoundaryMode_BlockArrayContentAbsent_PrependsBlock(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "JSON only.", Position: "prepend",
	}})
	in := `{"system":[{"type":"text","text":"prelude"}],"messages":[{"role":"user","content":"hi"}]}`
	out := applyClaude(in)
	blocks := gjson.GetBytes(out, "system").Array()
	if len(blocks) != 2 {
		t.Fatalf("expected new text block prepended; got %d in %s", len(blocks), string(out))
	}
	if blocks[0].Get("text").String() != "JSON only." {
		t.Fatalf("prepended block content mismatch: %s", blocks[0].Raw)
	}
}

// === gemini-cli rooted under request.* ===

func TestPromptRules_GeminiCLI_Inject_System_NestedRequest(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "JSON.",
	}})
	in := `{"request":{"systemInstruction":{"role":"system","parts":[{"text":"You are helpful."}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`
	out := applyGeminiCLI(in)
	parts := gjson.GetBytes(out, "request.systemInstruction.parts").Array()
	if len(parts) != 2 {
		t.Fatalf("expected systemInstruction under request.* to gain a part; got %d in %s", len(parts), string(out))
	}
	if parts[1].Get("text").String() != "JSON." {
		t.Fatalf("unexpected appended part: %s", parts[1].Raw)
	}
}

func TestPromptRules_GeminiCLI_Inject_LastUser_NestedRequest(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "user", Enabled: true, Target: "user", Action: "inject",
		Content: "done",
	}})
	in := `{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`
	out := applyGeminiCLI(in)
	parts := gjson.GetBytes(out, "request.contents.0.parts").Array()
	if len(parts) != 2 {
		t.Fatalf("expected user parts to gain a text part; got %d in %s", len(parts), string(out))
	}
}

// Plain "gemini" must NOT touch request.* — confirms the two formats are
// dispatched to different handlers.
func TestPromptRules_PlainGemini_DoesNotTouchNestedRequest(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "hi",
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

func TestPromptRules_OpenAI_Inject_System_ContentNull_Boundary(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "hi",
	}})
	in := `{"messages":[{"role":"system","content":null},{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	got := gjson.GetBytes(out, "messages.0.content").String()
	if got != "hi" {
		t.Fatalf("null content should be replaced with injected string; got %q", got)
	}
}

func TestPromptRules_OpenAI_Inject_System_ContentNull_MarkerSkips(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: " hi", Marker: "anchor",
	}})
	in := `{"messages":[{"role":"system","content":null},{"role":"user","content":"hi"}]}`
	out := applyOpenAI(in)
	if got := gjson.GetBytes(out, "messages.0.content").Type; got != gjson.Null {
		t.Fatalf("null content under marker mode must remain null; got type=%v in %s", got, string(out))
	}
}

// === Edge: empty text in array block ===

func TestPromptRules_OpenAI_LastUser_EmptyTextBlockSkipped(t *testing.T) {
	withPromptRules(t, []config.PromptRule{{
		Name: "user", Enabled: true, Target: "user", Action: "inject",
		Content: " ok",
	}})
	in := `{"messages":[{"role":"user","content":"first"},{"role":"user","content":[{"type":"text","text":""}]}]}`
	out := applyOpenAI(in)
	first := gjson.GetBytes(out, "messages.0.content").String()
	if first != "first ok" {
		t.Fatalf("expected first user (only natural-language one) to receive inject; got %q", first)
	}
}

// === Snapshot clearing on empty input ===

func TestPromptRules_Sanitize_EmptyClearsSnapshot(t *testing.T) {
	UpdatePromptRulesSnapshot([]config.PromptRule{{
		Name: "x", Enabled: true, Target: "system", Action: "inject",
		Content: "hi", Position: "append",
	}})
	t.Cleanup(func() { UpdatePromptRulesSnapshot(nil) })
	cfg := &config.Config{PromptRules: nil}
	cfg.SanitizePromptRules()
	out := applyOpenAI(`{"messages":[{"role":"user","content":"hi"}]}`)
	// After Sanitize wipes snapshot, the rule should not fire — the only
	// content the user message has is "hi", which the system would have
	// injected as well. Verify there is no role=system message.
	if gjson.GetBytes(out, "messages.0.role").String() == "system" {
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
	rulesA := []config.PromptRule{{
		Name: "a", Enabled: true, Target: "system", Action: "inject",
		Content: "a.", Position: "append",
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

// === Pure-helper tests for hasAdjacentContent / injectIntoText ===

func TestInjectIntoText_BoundaryMode_Append(t *testing.T) {
	out, mut := injectIntoText("base", "X", "", "append")
	if !mut || out != "baseX" {
		t.Fatalf("append boundary: got (%q, %v)", out, mut)
	}
}

func TestInjectIntoText_BoundaryMode_Prepend(t *testing.T) {
	out, mut := injectIntoText("base", "X", "", "prepend")
	if !mut || out != "Xbase" {
		t.Fatalf("prepend boundary: got (%q, %v)", out, mut)
	}
}

func TestInjectIntoText_BoundaryMode_ContentPresent_Skip(t *testing.T) {
	out, mut := injectIntoText("preX-suf", "X", "", "append")
	if mut || out != "preX-suf" {
		t.Fatalf("expected skip; got (%q, %v)", out, mut)
	}
}

func TestInjectIntoText_MarkerMode_Append_FirstOccurrence(t *testing.T) {
	out, mut := injectIntoText("X.X.X", "*", "X", "append")
	if !mut || out != "X*.X.X" {
		t.Fatalf("expected first-occurrence append; got (%q, %v)", out, mut)
	}
}

func TestInjectIntoText_MarkerMode_AdjacentAtAnyOccurrence_Skip(t *testing.T) {
	// Last X has content "*" adjacent → whole op skips.
	out, mut := injectIntoText("X.X.X*", "*", "X", "append")
	if mut || out != "X.X.X*" {
		t.Fatalf("expected adjacency skip; got (%q, %v)", out, mut)
	}
}

func TestInjectIntoText_MarkerMode_NotInText_Skip(t *testing.T) {
	out, mut := injectIntoText("nothing here", "*", "X", "append")
	if mut || out != "nothing here" {
		t.Fatalf("expected no-anchor skip; got (%q, %v)", out, mut)
	}
}

func TestInjectIntoText_EmptyContent_Skip(t *testing.T) {
	out, mut := injectIntoText("base", "", "", "append")
	if mut || out != "base" {
		t.Fatalf("empty content must be skip; got (%q, %v)", out, mut)
	}
}

func TestInjectIntoText_EmptyTarget_Boundary(t *testing.T) {
	out, mut := injectIntoText("", "X", "", "append")
	if !mut || out != "X" {
		t.Fatalf("empty target boundary append: got (%q, %v)", out, mut)
	}
}

func TestInjectIntoText_EmptyTarget_MarkerMode_Skip(t *testing.T) {
	out, mut := injectIntoText("", "X", "anchor", "append")
	if mut || out != "" {
		t.Fatalf("empty target marker mode: got (%q, %v)", out, mut)
	}
}

func TestInjectIntoText_PrependMarker_NotEnoughRoom_Inserts(t *testing.T) {
	// content " bar" length 4; marker "foo" at index 2; not enough room before
	// (only "X " = 2 chars). hasAdjacentContent must return false.
	out, mut := injectIntoText("X foo Y", " bar", "foo", "prepend")
	if !mut || out != "X  barfoo Y" {
		t.Fatalf("prepend with insufficient prior room: got (%q, %v)", out, mut)
	}
}

func TestHasAdjacentContent_Append_DirectlyAfter(t *testing.T) {
	if !hasAdjacentContent("X foobar Y", "bar", "foo", "append") {
		t.Fatal("expected adjacent (foo|bar)")
	}
	if hasAdjacentContent("X foo bar Y", "bar", "foo", "append") {
		t.Fatal("space between marker and content must NOT be adjacent")
	}
}

func TestHasAdjacentContent_Prepend_DirectlyBefore(t *testing.T) {
	if !hasAdjacentContent("X barfoo Y", "bar", "foo", "prepend") {
		t.Fatal("expected adjacent (bar|foo)")
	}
	if hasAdjacentContent("X bar foo Y", "bar", "foo", "prepend") {
		t.Fatal("space between content and marker must NOT be adjacent")
	}
}

func TestHasAdjacentContent_EmptyArgs(t *testing.T) {
	if hasAdjacentContent("text", "", "marker", "append") {
		t.Fatal("empty content must be non-adjacent")
	}
	if hasAdjacentContent("text", "x", "", "append") {
		t.Fatal("empty marker must be non-adjacent")
	}
}

func TestHasAdjacentContent_OverlappingMarker_Append(t *testing.T) {
	// marker="aa" appears at indices 0 AND 1 inside "aaab". The byte-level scan
	// must visit both, otherwise it misses the adjacency at index 1.
	if !hasAdjacentContent("aaab", "b", "aa", "append") {
		t.Fatal("scan must detect overlapping marker occurrences (append)")
	}
}

func TestHasAdjacentContent_OverlappingMarker_Prepend(t *testing.T) {
	// "baa" with marker="aa": occurrences at indices 1 and 2 (overlap of one).
	// Content "b" precedes the FIRST occurrence at index 1.
	if !hasAdjacentContent("baa", "b", "aa", "prepend") {
		t.Fatal("scan must detect overlapping marker occurrences (prepend)")
	}
}

func TestHasAdjacentContent_MultiByteUTF8(t *testing.T) {
	// "X 日本 hello Y": marker is the multi-byte CJK string, content is ASCII.
	// strings.Index works on bytes, and the adjacency check must respect that
	// length math without truncating in the middle of a code point.
	const marker = "日本"      // 6 bytes
	const text = "X 日本hello Y" // marker followed immediately by "hello"
	if !hasAdjacentContent(text, "hello", marker, "append") {
		t.Fatalf("multi-byte marker adjacency: expected match")
	}
	if hasAdjacentContent("X 日本 hello Y", "hello", marker, "append") {
		t.Fatalf("multi-byte marker adjacency: space between must be non-adjacent")
	}
}

func TestInjectIntoText_MarkerMode_OverlappingMarker_FirstRunSkips(t *testing.T) {
	// "aaab" with marker="aa" has occurrences at indices 0 AND 1. Content "b"
	// is adjacent at index 1. Without the byte-by-byte scan the adjacency at
	// index 1 would be missed and the rule would (incorrectly) inject. With
	// the fix the rule correctly skips on the FIRST run.
	out, mut := injectIntoText("aaab", "b", "aa", "append")
	if mut || out != "aaab" {
		t.Fatalf("overlap adjacency must skip on first run; got (%q, %v)", out, mut)
	}
}

func TestInjectIntoText_MarkerMode_NoOverlapAdjacency_StableAcrossRuns(t *testing.T) {
	// "aaa" with marker="aa": occurrences at 0 and 1; content "X" is adjacent
	// to neither initially. First run inserts at index 0; second run sees
	// adjacency at index 0 and skips.
	out, mut := injectIntoText("aaa", "X", "aa", "append")
	if !mut || out != "aaXa" {
		t.Fatalf("first run should inject after first marker; got (%q, %v)", out, mut)
	}
	out2, mut2 := injectIntoText(out, "X", "aa", "append")
	if mut2 || out2 != out {
		t.Fatalf("second run must be idempotent; got (%q, %v)", out2, mut2)
	}
}

func TestInjectIntoText_BoundaryMode_AbAbContentPresent(t *testing.T) {
	// content="ab" already present in target — boundary mode skips.
	out, mut := injectIntoText("abab", "ab", "", "append")
	if mut || out != "abab" {
		t.Fatalf("boundary content present must skip; got (%q, %v)", out, mut)
	}
}

func TestPromptRules_BlockArray_Marker_TwoMarkerBlocks_InjectsBlock0Only(t *testing.T) {
	// Two text blocks each contain the marker; neither has content adjacent.
	// Inject must land in block 0 only; block 2 keeps the marker untouched.
	withPromptRules(t, []config.PromptRule{{
		Name: "sys", Enabled: true, Target: "system", Action: "inject",
		Content: "*", Marker: "anchor", Position: "append",
	}})
	in := `{"system":[{"type":"text","text":"anchor first"},{"type":"text","text":"anchor second"}],"messages":[{"role":"user","content":"hi"}]}`
	out := applyClaude(in)
	blocks := gjson.GetBytes(out, "system").Array()
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks; got %d in %s", len(blocks), string(out))
	}
	if blocks[0].Get("text").String() != "anchor* first" {
		t.Fatalf("block 0 should have inject after marker; got %s", blocks[0].Raw)
	}
	if blocks[1].Get("text").String() != "anchor second" {
		t.Fatalf("block 1 must be untouched; got %s", blocks[1].Raw)
	}
}
