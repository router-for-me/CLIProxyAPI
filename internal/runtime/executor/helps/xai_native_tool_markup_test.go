package helps

import (
	"bytes"
	"strings"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const xaiStalledExecuteMarkup = "Checking the working tree and remaining over-cap files so I can continue the editorial demotions from the last batch.<|tool_calls_begin|><|tool_call_begin|>\n" +
	"Execute\n" +
	"<|tool_sep|>summary\n" +
	"Check git status and remaining diffs\n" +
	"<|tool_sep|>command\n" +
	"cd /tmp && git status\n" +
	"<|tool_sep|>timeout\n" +
	"30\n" +
	"<|tool_sep|>riskLevel\n" +
	"low\n" +
	"<|tool_call_end|><|tool_calls_end|>"

func TestParseXAINativeToolMarkupExecuteFromStalledSession(t *testing.T) {
	parsed, ok := parseXAINativeToolMarkup(xaiStalledExecuteMarkup)
	if !ok {
		t.Fatal("parseXAINativeToolMarkup() = false, want true")
	}
	if !strings.HasPrefix(parsed.Prefix, "Checking the working tree") {
		t.Fatalf("prefix = %q, want stalled-session lead-in", parsed.Prefix)
	}
	if strings.Contains(parsed.Prefix, xaiNativeToolCallsBegin) {
		t.Fatalf("prefix still contains markup: %q", parsed.Prefix)
	}
	if len(parsed.Calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(parsed.Calls))
	}
	if parsed.Calls[0].Name != "Execute" {
		t.Fatalf("name = %q, want Execute", parsed.Calls[0].Name)
	}
	if got := parsed.Calls[0].Arguments["summary"]; got != "Check git status and remaining diffs" {
		t.Fatalf("summary = %#v", got)
	}
	if got := parsed.Calls[0].Arguments["command"]; got != "cd /tmp && git status" {
		t.Fatalf("command = %#v", got)
	}
	if got := parsed.Calls[0].Arguments["timeout"]; got != "30" {
		t.Fatalf("timeout = %#v, want the raw scalar text", got)
	}
	if got := parsed.Calls[0].Arguments["riskLevel"]; got != "low" {
		t.Fatalf("riskLevel = %#v", got)
	}
}

func TestParseXAINativeToolMarkupEndFeatureRunWithJSONHandoff(t *testing.T) {
	text := "Handing that state back now.<|tool_calls_begin|><|tool_call_begin|>\n" +
		"EndFeatureRun\n" +
		"<|tool_sep|>successState\n" +
		"partial\n" +
		"<|tool_sep|>returnToOrchestrator\n" +
		"true\n" +
		"<|tool_sep|>validatorsPassed\n" +
		"false\n" +
		"<|tool_sep|>handoff\n" +
		"{\"salientSummary\": \"Paused mid-feature\", \"whatWasImplemented\": \"Measured counts\"}\n" +
		"<|tool_call_end|><|tool_calls_end|>"

	parsed, ok := parseXAINativeToolMarkup(text)
	if !ok {
		t.Fatal("parseXAINativeToolMarkup() = false, want true")
	}
	if parsed.Prefix != "Handing that state back now." {
		t.Fatalf("prefix = %q", parsed.Prefix)
	}
	if parsed.Calls[0].Name != "EndFeatureRun" {
		t.Fatalf("name = %q, want EndFeatureRun", parsed.Calls[0].Name)
	}
	if parsed.Calls[0].Arguments["successState"] != "partial" {
		t.Fatalf("successState = %#v", parsed.Calls[0].Arguments["successState"])
	}
	if parsed.Calls[0].Arguments["returnToOrchestrator"] != "true" {
		t.Fatalf("returnToOrchestrator = %#v", parsed.Calls[0].Arguments["returnToOrchestrator"])
	}
	if parsed.Calls[0].Arguments["validatorsPassed"] != "false" {
		t.Fatalf("validatorsPassed = %#v", parsed.Calls[0].Arguments["validatorsPassed"])
	}
	handoff, ok := parsed.Calls[0].Arguments["handoff"].(map[string]any)
	if !ok {
		t.Fatalf("handoff type = %T, want map[string]any", parsed.Calls[0].Arguments["handoff"])
	}
	if handoff["salientSummary"] != "Paused mid-feature" {
		t.Fatalf("handoff.salientSummary = %#v", handoff["salientSummary"])
	}
}

func TestParseXAINativeToolMarkupSingleArgSkill(t *testing.T) {
	text := "<|tool_calls_begin|><|tool_call_begin|>\n" +
		"Skill\n" +
		"<|tool_sep|>skill\n" +
		"mission-worker-base\n" +
		"<|tool_call_end|><|tool_calls_end|>"

	parsed, ok := parseXAINativeToolMarkup(text)
	if !ok {
		t.Fatal("parseXAINativeToolMarkup() = false, want true")
	}
	if parsed.Prefix != "" {
		t.Fatalf("prefix = %q, want empty", parsed.Prefix)
	}
	if parsed.Calls[0].Name != "Skill" {
		t.Fatalf("name = %q, want Skill", parsed.Calls[0].Name)
	}
	if parsed.Calls[0].Arguments["skill"] != "mission-worker-base" {
		t.Fatalf("skill = %#v", parsed.Calls[0].Arguments["skill"])
	}
}

func TestParseXAINativeToolMarkupMultipleToolCalls(t *testing.T) {
	text := "<|tool_calls_begin|><|tool_call_begin|>\n" +
		"Grep\n" +
		"<|tool_sep|>pattern\n" +
		"uppercase\n" +
		"<|tool_call_end|><|tool_call_begin|>\n" +
		"Read\n" +
		"<|tool_sep|>path\n" +
		"/tmp/a.swift\n" +
		"<|tool_call_end|><|tool_calls_end|>"

	parsed, ok := parseXAINativeToolMarkup(text)
	if !ok {
		t.Fatal("parseXAINativeToolMarkup() = false, want true")
	}
	if len(parsed.Calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(parsed.Calls))
	}
	if parsed.Calls[0].Name != "Grep" || parsed.Calls[1].Name != "Read" {
		t.Fatalf("names = %q, %q", parsed.Calls[0].Name, parsed.Calls[1].Name)
	}
	if parsed.Calls[0].Arguments["pattern"] != "uppercase" {
		t.Fatalf("pattern = %#v", parsed.Calls[0].Arguments["pattern"])
	}
	if parsed.Calls[1].Arguments["path"] != "/tmp/a.swift" {
		t.Fatalf("path = %#v", parsed.Calls[1].Arguments["path"])
	}
}

func TestParseXAINativeToolMarkupInlineCommandKey(t *testing.T) {
	text := "<|tool_calls_begin|><|tool_call_begin|>\n" +
		"Execute\n" +
		"<|tool_sep|>command: pwd\n" +
		"<|tool_sep|>summary\n" +
		"Print working directory\n" +
		"<|tool_call_end|><|tool_calls_end|>"

	parsed, ok := parseXAINativeToolMarkup(text)
	if !ok {
		t.Fatal("parseXAINativeToolMarkup() = false, want true")
	}
	if parsed.Calls[0].Arguments["command"] != "pwd" {
		t.Fatalf("command = %#v", parsed.Calls[0].Arguments["command"])
	}
	if parsed.Calls[0].Arguments["summary"] != "Print working directory" {
		t.Fatalf("summary = %#v", parsed.Calls[0].Arguments["summary"])
	}
}

func TestParseXAINativeToolMarkupRejectsTruncatedCallMissingEndTags(t *testing.T) {
	text := "keep going<|tool_calls_begin|><|tool_call_begin|>\n" +
		"Execute\n" +
		"<|tool_sep|>command\n" +
		"ls\n"

	if _, ok := parseXAINativeToolMarkup(text); ok {
		t.Fatal("truncated markup without a value terminator should not parse")
	}
}

func TestParseXAINativeToolMarkupRejectsBlockWhenLaterCallIsIncomplete(t *testing.T) {
	text := "<|tool_calls_begin|><|tool_call_begin|>\n" +
		"Grep\n" +
		"<|tool_sep|>pattern\n" +
		"uppercase\n" +
		"<|tool_call_end|><|tool_call_begin|>\n" +
		"Execute\n" +
		"<|tool_sep|>command\n" +
		"rm -rf /tmp/project"

	if _, ok := parseXAINativeToolMarkup(text); ok {
		t.Fatal("a complete first call must not be lifted when a later call is truncated")
	}
}

func TestParseXAINativeToolMarkupRejectsMidValueCutoff(t *testing.T) {
	text := "<|tool_calls_begin|><|tool_call_begin|>\n" +
		"Execute\n" +
		"<|tool_sep|>command\n" +
		"rm -rf /tmp/project"

	if _, ok := parseXAINativeToolMarkup(text); ok {
		t.Fatal("a command cut off mid-value should not be lifted")
	}
}

func TestParseXAINativeToolMarkupPreservesLargeJSONIntegers(t *testing.T) {
	text := "<|tool_calls_begin|><|tool_call_begin|>\n" +
		"Read\n" +
		"<|tool_sep|>meta\n" +
		"{\"id\":9007199254740993}\n" +
		"<|tool_call_end|><|tool_calls_end|>"

	parsed, ok := parseXAINativeToolMarkup(text)
	if !ok {
		t.Fatal("parseXAINativeToolMarkup() = false, want true")
	}
	args := xaiNativeArgumentsJSON(parsed.Calls[0].Arguments)
	if gjson.Get(args, "meta.id").Raw != "9007199254740993" {
		t.Fatalf("large integer was rounded: %s", args)
	}
}

func TestParseXAINativeToolMarkupPreservesTextAfterBlock(t *testing.T) {
	text := "Before.<|tool_calls_begin|><|tool_call_begin|>\n" +
		"Execute\n" +
		"<|tool_sep|>command\n" +
		"ls\n" +
		"<|tool_call_end|><|tool_calls_end|>After."

	parsed, ok := parseXAINativeToolMarkup(text)
	if !ok {
		t.Fatal("parseXAINativeToolMarkup() = false, want true")
	}
	if parsed.Prefix != "Before." {
		t.Fatalf("prefix = %q", parsed.Prefix)
	}
	if parsed.Suffix != "After." {
		t.Fatalf("suffix = %q", parsed.Suffix)
	}
}

func TestRewriteXAINativeToolMarkupChatJSONPreservesTextAfterBlock(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"Before.<|tool_calls_begin|><|tool_call_begin|>\nExecute\n<|tool_sep|>command\nls\n<|tool_call_end|><|tool_calls_end|>After."}}]}`)
	rewritten, ok := rewriteXAINativeToolMarkupChatJSON(body, xaiNativeDeclared("Execute"))
	if !ok {
		t.Fatal("rewrite should succeed")
	}
	if got := gjson.GetBytes(rewritten, "choices.0.message.content").String(); got != "Before.After." {
		t.Fatalf("content = %q, want Before.After.", got)
	}
}

func TestRewriteXAINativeToolMarkupChatJSONRejectsUndeclaredCallInBlock(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"<|tool_calls_begin|><|tool_call_begin|>\nExecute\n<|tool_sep|>command\nls\n<|tool_call_end|><|tool_call_begin|>\nHallucinated\n<|tool_sep|>path\n/tmp/a\n<|tool_call_end|><|tool_calls_end|>"}}]}`)
	if _, ok := rewriteXAINativeToolMarkupChatJSON(body, xaiNativeDeclared("Execute")); ok {
		t.Fatal("a block with any undeclared call should not be lifted")
	}
}

func TestParseXAINativeToolMarkupLeavesPlainTextUnchanged(t *testing.T) {
	if _, ok := parseXAINativeToolMarkup("just a sentence"); ok {
		t.Fatal("plain text should not parse as markup")
	}
	if _, ok := rewriteXAINativeToolMarkupChatJSON([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`), xaiNativeDeclared("Execute")); ok {
		t.Fatal("plain chat completion should not rewrite")
	}
}

func TestRewriteXAINativeToolMarkupChatJSON(t *testing.T) {
	body := []byte(`{"id":"chatcmpl_abc","choices":[{"index":0,"message":{"role":"assistant","content":"Working.<|tool_calls_begin|><|tool_call_begin|>\nExecute\n<|tool_sep|>command\nls\n<|tool_call_end|><|tool_calls_end|>"},"finish_reason":"stop"}]}`)

	rewritten, ok := rewriteXAINativeToolMarkupChatJSON(body, xaiNativeDeclared("Execute"))
	if !ok {
		t.Fatal("rewriteXAINativeToolMarkupChatJSON() = false, want true")
	}
	if gjson.GetBytes(rewritten, "choices.0.finish_reason").String() != "tool_calls" {
		t.Fatalf("finish_reason = %s", rewritten)
	}
	if gjson.GetBytes(rewritten, "choices.0.message.content").String() != "Working." {
		t.Fatalf("content = %s", rewritten)
	}
	if gjson.GetBytes(rewritten, "choices.0.message.tool_calls.#").Int() != 1 {
		t.Fatalf("tool_calls count = %s", rewritten)
	}
	if gjson.GetBytes(rewritten, "choices.0.message.tool_calls.0.type").String() != "function" {
		t.Fatalf("tool type = %s", rewritten)
	}
	if gjson.GetBytes(rewritten, "choices.0.message.tool_calls.0.function.name").String() != "Execute" {
		t.Fatalf("function name = %s", rewritten)
	}
	args := gjson.GetBytes(rewritten, "choices.0.message.tool_calls.0.function.arguments").String()
	if gjson.Get(args, "command").String() != "ls" {
		t.Fatalf("arguments = %s", args)
	}
	if strings.Contains(string(rewritten), "tool_calls_begin") {
		t.Fatalf("markup leaked into rewritten body: %s", rewritten)
	}
}

func TestRewriteXAINativeToolMarkupChatJSONTreatsNullToolCallsAsAbsent(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"Working.<|tool_calls_begin|><|tool_call_begin|>\nExecute\n<|tool_sep|>command\nls\n<|tool_call_end|><|tool_calls_end|>","tool_calls":null},"finish_reason":"stop"}]}`)
	rewritten, ok := rewriteXAINativeToolMarkupChatJSON(body, xaiNativeDeclared("Execute"))
	if !ok {
		t.Fatal("null tool_calls should still rewrite leaked markup")
	}
	if gjson.GetBytes(rewritten, "choices.0.message.tool_calls.0.function.name").String() != "Execute" {
		t.Fatalf("rewritten = %s", rewritten)
	}
}

func TestRewriteXAINativeToolMarkupChatJSONSkipsWhenToolCallsPresent(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"x<|tool_calls_begin|>","tool_calls":[{"id":"call_1","type":"function","function":{"name":"Read","arguments":"{}"}}]}}]}`)
	if _, ok := rewriteXAINativeToolMarkupChatJSON(body, xaiNativeDeclared("Read")); ok {
		t.Fatal("already-present tool_calls should not rewrite")
	}
}

func TestRewriteXAINativeToolMarkupChatJSONSkipsWhenNoToolsDeclared(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"Example:<|tool_calls_begin|><|tool_call_begin|>\nExecute\n<|tool_sep|>command\nls\n<|tool_call_end|><|tool_calls_end|>"}}]}`)
	if _, ok := rewriteXAINativeToolMarkupChatJSON(body, xaiNativeDeclaredTools{}); ok {
		t.Fatal("markup without declared tools should stay text")
	}
}

func TestRewriteXAINativeToolMarkupChatJSONSkipsUnmatchedToolName(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"Example:<|tool_calls_begin|><|tool_call_begin|>\nExecute\n<|tool_sep|>command\nls\n<|tool_call_end|><|tool_calls_end|>"}}]}`)
	if _, ok := rewriteXAINativeToolMarkupChatJSON(body, xaiNativeDeclared("Read")); ok {
		t.Fatal("markup whose name is not a declared tool should stay text")
	}
}

func TestRewriteXAINativeToolMarkupChatJSONPreservesSchemaStringScalars(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"<|tool_calls_begin|><|tool_call_begin|>\nExecute\n<|tool_sep|>command\ntrue\n<|tool_sep|>timeout\n30\n<|tool_call_end|><|tool_calls_end|>"}}]}`)
	declared := parseXAINativeDeclaredTools(xaiNativeOpenAIRequestWithToolSchema("Execute", `{"command":{"type":"string"},"timeout":{"type":"number"}}`))
	rewritten, ok := rewriteXAINativeToolMarkupChatJSON(body, declared)
	if !ok {
		t.Fatal("schema-backed markup should rewrite")
	}
	args := gjson.GetBytes(rewritten, "choices.0.message.tool_calls.0.function.arguments").String()
	if gjson.Get(args, "command").Type != gjson.String || gjson.Get(args, "command").String() != "true" {
		t.Fatalf("command should stay a string: %s", args)
	}
	if gjson.Get(args, "timeout").Type != gjson.Number || gjson.Get(args, "timeout").Int() != 30 {
		t.Fatalf("timeout should follow number schema: %s", args)
	}
}

func TestRewriteXAINativeToolMarkupChatJSONResolvesShortNameCollisions(t *testing.T) {
	longName := "mcp__factory__" + strings.Repeat("collect_workspace_diagnostics_detail", 2)
	shortName := buildXAINativeShortNameMap([]string{longName})[longName]
	if shortName == "" || shortName == longName {
		t.Fatalf("short name = %q, want a distinct shortened form", shortName)
	}

	declared := parseXAINativeDeclaredTools(xaiNativeOpenAIRequestWithTools(longName, shortName))
	shortMap := buildXAINativeShortNameMap([]string{longName, shortName})
	if shortMap[longName] != shortName {
		t.Fatalf("long tool upstream name = %q, want %q", shortMap[longName], shortName)
	}
	collidingUpstream := shortMap[shortName]
	if collidingUpstream == "" || collidingUpstream == shortName {
		t.Fatalf("colliding tool upstream name = %q, want a suffixed form", collidingUpstream)
	}
	if got, ok := declared.resolve(shortName); !ok || got != longName {
		t.Fatalf("resolve(%q) = %q, %v; want the long tool that owns the short upstream name", shortName, got, ok)
	}
	if got, ok := declared.resolve(collidingUpstream); !ok || got != shortName {
		t.Fatalf("resolve(%q) = %q, %v; want the colliding original tool", collidingUpstream, got, ok)
	}

	body := []byte(`{"choices":[{"message":{"role":"assistant","content":""}}]}`)
	body, _ = sjson.SetBytes(body, "choices.0.message.content", "<|tool_calls_begin|><|tool_call_begin|>\n"+shortName+"\n<|tool_sep|>path\n/tmp/a\n<|tool_call_end|><|tool_calls_end|>")
	rewritten, ok := rewriteXAINativeToolMarkupChatJSON(body, declared)
	if !ok {
		t.Fatal("colliding short markup name should rewrite")
	}
	if got := gjson.GetBytes(rewritten, "choices.0.message.tool_calls.0.function.name").String(); got != longName {
		t.Fatalf("function name = %q, want the long tool that was sent as %q", got, shortName)
	}
}

func TestRewriteXAINativeToolMarkupChatJSONPreservesSchemaNumberLexeme(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"<|tool_calls_begin|><|tool_call_begin|>\nRead\n<|tool_sep|>id\n9007199254740993.0\n<|tool_call_end|><|tool_calls_end|>"}}]}`)
	declared := parseXAINativeDeclaredTools(xaiNativeOpenAIRequestWithToolSchema("Read", `{"id":{"type":"number"}}`))
	rewritten, ok := rewriteXAINativeToolMarkupChatJSON(body, declared)
	if !ok {
		t.Fatal("number-schema markup should rewrite")
	}
	args := gjson.GetBytes(rewritten, "choices.0.message.tool_calls.0.function.arguments").String()
	if gjson.Get(args, "id").Raw != "9007199254740993.0" {
		t.Fatalf("number lexeme was rewritten: %s", args)
	}
}

func TestRewriteXAINativeToolMarkupChatJSONRestoresShortenedToolName(t *testing.T) {
	longName := "mcp__factory__" + strings.Repeat("collect_workspace_diagnostics_detail", 2)
	if len(longName) <= 64 {
		t.Fatalf("test setup: longName len=%d, want > 64", len(longName))
	}
	declared := parseXAINativeDeclaredTools(xaiNativeOpenAIRequestWithTools(longName))
	shortName := buildXAINativeShortNameMap([]string{longName})[longName]
	if shortName == "" || shortName == longName {
		t.Fatalf("short name = %q, want a distinct shortened form", shortName)
	}

	body := []byte(`{"choices":[{"message":{"role":"assistant","content":""}}]}`)
	body, _ = sjson.SetBytes(body, "choices.0.message.content", "<|tool_calls_begin|><|tool_call_begin|>\n"+shortName+"\n<|tool_sep|>path\n/tmp/a\n<|tool_call_end|><|tool_calls_end|>")
	rewritten, ok := rewriteXAINativeToolMarkupChatJSON(body, declared)
	if !ok {
		t.Fatal("shortened markup name should rewrite")
	}
	if got := gjson.GetBytes(rewritten, "choices.0.message.tool_calls.0.function.name").String(); got != longName {
		t.Fatalf("function name = %q, want original %q", got, longName)
	}
}

func TestRewriteXAINativeToolMarkupSSE(t *testing.T) {
	sse := "" +
		"data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"grok-4.6-fast\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Working.\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"grok-4.6-fast\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"<|tool_calls_begin|><|tool_call_begin|>\\nExecute\\n<|tool_sep|>command\\nls\\n<|tool_call_end|><|tool_calls_end|>\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"grok-4.6-fast\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	rewritten, ok := rewriteXAINativeToolMarkupSSE([]byte(sse), xaiNativeDeclared("Execute"))
	if !ok {
		t.Fatal("rewriteXAINativeToolMarkupSSE() = false, want true")
	}
	text := string(rewritten)
	if !strings.Contains(text, `"finish_reason":"tool_calls"`) {
		t.Fatalf("missing finish_reason tool_calls: %s", text)
	}
	if !strings.Contains(text, `"name":"Execute"`) {
		t.Fatalf("missing Execute tool call: %s", text)
	}
	if !strings.Contains(text, "Working.") {
		t.Fatalf("missing prefix text: %s", text)
	}
	if strings.Contains(text, "tool_calls_begin") {
		t.Fatalf("markup leaked into rewritten SSE: %s", text)
	}
	if !strings.Contains(text, "data: [DONE]") {
		t.Fatalf("missing [DONE]: %s", text)
	}
	if !strings.Contains(text, `"native_finish_reason":"tool_calls"`) {
		t.Fatalf("missing native_finish_reason tool_calls: %s", text)
	}
}

func TestRewriteXAINativeToolMarkupChatChunksSplitAcrossDeltas(t *testing.T) {
	chunks := [][]byte{
		[]byte(`{"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"grok-4.6","choices":[{"index":0,"delta":{"role":"assistant","content":"Working.<|tool_cal"},"finish_reason":null}]}`),
		[]byte(`{"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"grok-4.6","choices":[{"index":0,"delta":{"content":"ls_begin|><|tool_call_begin|>\nExecute\n<|tool_sep|>command\nls\n<|tool_call_end|><|tool_calls_end|>"},"finish_reason":null}]}`),
		[]byte(`{"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"grok-4.6","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`),
	}
	rewritten, ok := rewriteXAINativeToolMarkupChatChunks(chunks, xaiNativeDeclared("Execute"))
	if !ok {
		t.Fatal("split markup should rewrite after assembly")
	}
	joined := string(bytes.Join(rewritten, []byte("\n")))
	if strings.Contains(joined, "tool_calls_begin") || strings.Contains(joined, xaiNativeToolCallBegin) {
		t.Fatalf("split markup leaked: %s", joined)
	}
	if !strings.Contains(joined, `"name":"Execute"`) {
		t.Fatalf("missing Execute: %s", joined)
	}
	if !strings.Contains(joined, `"finish_reason":"tool_calls"`) {
		t.Fatalf("missing finish_reason: %s", joined)
	}
}

func TestXAINativeToolMarkupChatStreamPassThroughWithoutMarkup(t *testing.T) {
	stream := NewXAINativeToolMarkupChatStream(sdktranslator.FormatOpenAI, xaiNativeOpenAIRequestWithTools("Execute"))
	first := []byte(`{"choices":[{"delta":{"content":"hello"}}]}`)
	got := stream.Ingest(first)
	if len(got) != 1 || !bytes.Equal(got[0], first) {
		t.Fatalf("plain chunk should pass through immediately, got %#v", got)
	}
	if flushed := stream.Flush(); len(flushed) != 0 {
		t.Fatalf("Flush() = %#v, want empty", flushed)
	}
}

func TestXAINativeToolMarkupChatStreamBuffersUntilComplete(t *testing.T) {
	stream := NewXAINativeToolMarkupChatStream(sdktranslator.FormatOpenAI, xaiNativeOpenAIRequestWithTools("Execute"))
	prefix := []byte(`{"id":"chatcmpl_1","choices":[{"delta":{"content":"Working."}}]}`)
	if got := stream.Ingest(prefix); len(got) != 1 {
		t.Fatalf("prefix should pass through, got %#v", got)
	}
	markup := []byte(`{"id":"chatcmpl_1","choices":[{"delta":{"content":"<|tool_calls_begin|><|tool_call_begin|>\nExecute\n<|tool_sep|>command\nls\n<|tool_call_end|><|tool_calls_end|>"}}]}`)
	if got := stream.Ingest(markup); len(got) != 0 {
		t.Fatalf("markup chunk should buffer, got %#v", got)
	}
	stop := []byte(`{"id":"chatcmpl_1","choices":[{"delta":{},"finish_reason":"stop"}]}`)
	if got := stream.Ingest(stop); len(got) != 0 {
		t.Fatalf("stop chunk should buffer after markup, got %#v", got)
	}
	flushed := stream.Flush()
	joined := string(bytes.Join(flushed, []byte("\n")))
	if !strings.Contains(joined, `"name":"Execute"`) {
		t.Fatalf("flush missing Execute: %s", joined)
	}
	if strings.Contains(joined, "tool_calls_begin") {
		t.Fatalf("flush leaked markup: %s", joined)
	}
}

func TestXAINativeToolMarkupChatStreamPassThroughWhenNoToolsDeclared(t *testing.T) {
	stream := NewXAINativeToolMarkupChatStream(sdktranslator.FormatOpenAI, []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"explain"}]}`))
	markup := []byte(`{"choices":[{"delta":{"content":"<|tool_calls_begin|><|tool_call_begin|>\nExecute\n<|tool_sep|>command\nls\n<|tool_call_end|><|tool_calls_end|>"}}]}`)
	got := stream.Ingest(markup)
	if len(got) != 1 || !bytes.Equal(got[0], markup) {
		t.Fatalf("quoted markup with no declared tools should stream immediately, got %#v", got)
	}
	if flushed := stream.Flush(); len(flushed) != 0 {
		t.Fatalf("Flush() = %#v, want empty", flushed)
	}
}

func TestXAINativeToolMarkupChatStreamReleasesFalseMarkerPrefix(t *testing.T) {
	stream := NewXAINativeToolMarkupChatStream(sdktranslator.FormatOpenAI, xaiNativeOpenAIRequestWithTools("Execute"))
	prefix := []byte(`{"id":"chatcmpl_1","choices":[{"delta":{"content":"compare a <"}}]}`)
	if got := stream.Ingest(prefix); len(got) != 0 {
		t.Fatalf("trailing < should buffer pending a marker check, got %#v", got)
	}
	rest := []byte(`{"id":"chatcmpl_1","choices":[{"delta":{"content":" b"}}]}`)
	got := stream.Ingest(rest)
	if len(got) != 2 {
		t.Fatalf("false marker prefix should release immediately, got %#v", got)
	}
	if flushed := stream.Flush(); len(flushed) != 0 {
		t.Fatalf("Flush() = %#v, want empty after false-prefix release", flushed)
	}
}

func TestApplyXAINativeToolMarkupChatJSONIgnoresResponsesFormat(t *testing.T) {
	body := []byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"Working.<|tool_calls_begin|>"}]}]}`)
	if got := ApplyXAINativeToolMarkupChatJSON(sdktranslator.FormatOpenAIResponse, body, xaiNativeOpenAIRequestWithTools("Execute")); !bytes.Equal(got, body) {
		t.Fatal("Responses payloads must not be rewritten by the chat-completion hook")
	}
}

func xaiNativeDeclared(names ...string) xaiNativeDeclaredTools {
	return parseXAINativeDeclaredTools(xaiNativeOpenAIRequestWithTools(names...))
}

func xaiNativeOpenAIRequestWithTools(names ...string) []byte {
	req := []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"continue"}]}`)
	for _, name := range names {
		tool := []byte(`{"type":"function","function":{"name":"","parameters":{"type":"object"}}}`)
		tool, _ = sjson.SetBytes(tool, "function.name", name)
		req, _ = sjson.SetRawBytes(req, "tools.-1", tool)
	}
	return req
}

func xaiNativeOpenAIRequestWithToolSchema(name, properties string) []byte {
	req := []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"continue"}]}`)
	tool := []byte(`{"type":"function","function":{"name":"","parameters":{"type":"object","properties":{}}}}`)
	tool, _ = sjson.SetBytes(tool, "function.name", name)
	tool, _ = sjson.SetRawBytes(tool, "function.parameters.properties", []byte(properties))
	req, _ = sjson.SetRawBytes(req, "tools.-1", tool)
	return req
}
