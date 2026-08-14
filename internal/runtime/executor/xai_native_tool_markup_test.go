package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
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
	if got := parsed.Calls[0].Arguments["timeout"]; got != int64(30) {
		t.Fatalf("timeout = %#v, want int64(30)", got)
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
	if parsed.Calls[0].Arguments["returnToOrchestrator"] != true {
		t.Fatalf("returnToOrchestrator = %#v", parsed.Calls[0].Arguments["returnToOrchestrator"])
	}
	if parsed.Calls[0].Arguments["validatorsPassed"] != false {
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

func TestParseXAINativeToolMarkupTruncatedCallMissingEndTags(t *testing.T) {
	text := "keep going<|tool_calls_begin|><|tool_call_begin|>\n" +
		"Execute\n" +
		"<|tool_sep|>command\n" +
		"ls\n"

	parsed, ok := parseXAINativeToolMarkup(text)
	if !ok {
		t.Fatal("parseXAINativeToolMarkup() = false, want true")
	}
	if parsed.Prefix != "keep going" {
		t.Fatalf("prefix = %q", parsed.Prefix)
	}
	if parsed.Calls[0].Name != "Execute" {
		t.Fatalf("name = %q", parsed.Calls[0].Name)
	}
	if parsed.Calls[0].Arguments["command"] != "ls" {
		t.Fatalf("command = %#v", parsed.Calls[0].Arguments["command"])
	}
}

func TestParseXAINativeToolMarkupLeavesPlainTextUnchanged(t *testing.T) {
	if _, ok := parseXAINativeToolMarkup("just a sentence"); ok {
		t.Fatal("plain text should not parse as markup")
	}
	if _, ok := rewriteXAINativeToolMarkupChatJSON([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`)); ok {
		t.Fatal("plain chat completion should not rewrite")
	}
}

func TestRewriteXAINativeToolMarkupChatJSON(t *testing.T) {
	body := []byte(`{"id":"chatcmpl_abc","choices":[{"index":0,"message":{"role":"assistant","content":"Working.<|tool_calls_begin|><|tool_call_begin|>\nExecute\n<|tool_sep|>command\nls\n<|tool_call_end|><|tool_calls_end|>"},"finish_reason":"stop"}]}`)

	rewritten, ok := rewriteXAINativeToolMarkupChatJSON(body)
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
	rewritten, ok := rewriteXAINativeToolMarkupChatJSON(body)
	if !ok {
		t.Fatal("null tool_calls should still rewrite leaked markup")
	}
	if gjson.GetBytes(rewritten, "choices.0.message.tool_calls.0.function.name").String() != "Execute" {
		t.Fatalf("rewritten = %s", rewritten)
	}
}

func TestRewriteXAINativeToolMarkupChatJSONSkipsWhenToolCallsPresent(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"x<|tool_calls_begin|>","tool_calls":[{"id":"call_1","type":"function","function":{"name":"Read","arguments":"{}"}}]}}]}`)
	if _, ok := rewriteXAINativeToolMarkupChatJSON(body); ok {
		t.Fatal("already-present tool_calls should not rewrite")
	}
}

func TestRewriteXAINativeToolMarkupSSE(t *testing.T) {
	sse := "" +
		"data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"grok-4.6-fast\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Working.\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"grok-4.6-fast\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"<|tool_calls_begin|><|tool_call_begin|>\\nExecute\\n<|tool_sep|>command\\nls\\n<|tool_call_end|><|tool_calls_end|>\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"grok-4.6-fast\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	rewritten, ok := rewriteXAINativeToolMarkupSSE([]byte(sse))
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
}

func TestRewriteXAINativeToolMarkupChatChunksSplitAcrossDeltas(t *testing.T) {
	chunks := [][]byte{
		[]byte(`{"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"grok-4.6","choices":[{"index":0,"delta":{"role":"assistant","content":"Working.<|tool_cal"},"finish_reason":null}]}`),
		[]byte(`{"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"grok-4.6","choices":[{"index":0,"delta":{"content":"ls_begin|><|tool_call_begin|>\nExecute\n<|tool_sep|>command\nls\n<|tool_call_end|><|tool_calls_end|>"},"finish_reason":null}]}`),
		[]byte(`{"id":"chatcmpl_1","object":"chat.completion.chunk","created":1,"model":"grok-4.6","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`),
	}
	rewritten, ok := rewriteXAINativeToolMarkupChatChunks(chunks)
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
	stream := newXAINativeToolMarkupChatStream(sdktranslator.FormatOpenAI)
	first := []byte(`{"choices":[{"delta":{"content":"hello"}}]}`)
	got := stream.ingest(first)
	if len(got) != 1 || !bytes.Equal(got[0], first) {
		t.Fatalf("plain chunk should pass through immediately, got %#v", got)
	}
	if flushed := stream.flush(); len(flushed) != 0 {
		t.Fatalf("flush() = %#v, want empty", flushed)
	}
}

func TestXAINativeToolMarkupChatStreamBuffersUntilComplete(t *testing.T) {
	stream := newXAINativeToolMarkupChatStream(sdktranslator.FormatOpenAI)
	prefix := []byte(`{"id":"chatcmpl_1","choices":[{"delta":{"content":"Working."}}]}`)
	if got := stream.ingest(prefix); len(got) != 1 {
		t.Fatalf("prefix should pass through, got %#v", got)
	}
	markup := []byte(`{"id":"chatcmpl_1","choices":[{"delta":{"content":"<|tool_calls_begin|><|tool_call_begin|>\nExecute\n<|tool_sep|>command\nls\n<|tool_call_end|><|tool_calls_end|>"}}]}`)
	if got := stream.ingest(markup); len(got) != 0 {
		t.Fatalf("markup chunk should buffer, got %#v", got)
	}
	stop := []byte(`{"id":"chatcmpl_1","choices":[{"delta":{},"finish_reason":"stop"}]}`)
	if got := stream.ingest(stop); len(got) != 0 {
		t.Fatalf("stop chunk should buffer after markup, got %#v", got)
	}
	flushed := stream.flush()
	joined := string(bytes.Join(flushed, []byte("\n")))
	if !strings.Contains(joined, `"name":"Execute"`) {
		t.Fatalf("flush missing Execute: %s", joined)
	}
	if strings.Contains(joined, "tool_calls_begin") {
		t.Fatalf("flush leaked markup: %s", joined)
	}
}

func TestApplyXAINativeToolMarkupChatJSONIgnoresResponsesFormat(t *testing.T) {
	body := []byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"Working.<|tool_calls_begin|>"}]}]}`)
	if got := applyXAINativeToolMarkupChatJSON(sdktranslator.FormatOpenAIResponse, body); !bytes.Equal(got, body) {
		t.Fatal("Responses payloads must not be rewritten by the chat-completion hook")
	}
}

func TestXAIExecutorExecuteLiftsNativeToolMarkupForOpenAIChat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		completed := xaiNativeCompletedResponse(xaiStalledExecuteMarkup)
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n", xaiNativeMessageItem(xaiStalledExecuteMarkup))
		_, _ = fmt.Fprintf(w, "data: %s\n\n", completed)
	}))
	defer server.Close()

	resp, err := NewXAIExecutor(&config.Config{}).Execute(context.Background(), xaiNativeTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "grok-4.6",
		Payload: []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"continue"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gjson.GetBytes(resp.Payload, "choices.0.finish_reason").String() != "tool_calls" {
		t.Fatalf("finish_reason = %s", resp.Payload)
	}
	if !strings.HasPrefix(gjson.GetBytes(resp.Payload, "choices.0.message.content").String(), "Checking the working tree") {
		t.Fatalf("content = %s", resp.Payload)
	}
	if gjson.GetBytes(resp.Payload, "choices.0.message.tool_calls.0.function.name").String() != "Execute" {
		t.Fatalf("tool name = %s", resp.Payload)
	}
	args := gjson.GetBytes(resp.Payload, "choices.0.message.tool_calls.0.function.arguments").String()
	if gjson.Get(args, "command").String() != "cd /tmp && git status" {
		t.Fatalf("arguments = %s", args)
	}
	if strings.Contains(string(resp.Payload), "tool_calls_begin") {
		t.Fatalf("markup leaked into Execute payload: %s", resp.Payload)
	}
}

func TestXAIExecutorExecuteLeavesPlainOpenAIChatUnchanged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n", xaiNativeMessageItem("hello"))
		_, _ = fmt.Fprintf(w, "data: %s\n\n", xaiNativeCompletedResponse("hello"))
	}))
	defer server.Close()

	resp, err := NewXAIExecutor(&config.Config{}).Execute(context.Background(), xaiNativeTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "grok-4.6",
		Payload: []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if xaiNativeHasToolCalls(gjson.GetBytes(resp.Payload, "choices.0.message.tool_calls")) {
		t.Fatalf("plain text grew tool_calls: %s", resp.Payload)
	}
	if !strings.Contains(gjson.GetBytes(resp.Payload, "choices.0.message.content").String(), "hello") {
		t.Fatalf("content = %s", resp.Payload)
	}
}

func TestXAIExecutorExecuteStreamLiftsSplitNativeToolMarkup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"item_id\":\"msg_1\",\"delta\":\"Working.\"}\n\n"))
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"item_id\":\"msg_1\",\"delta\":\"<|tool_calls_begin|><|tool_call_begin|>\\nExecute\\n<|tool_sep|>command\\nls\\n<|tool_call_end|><|tool_calls_end|>\"}\n\n"))
		_, _ = fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", xaiNativeCompletedResponse("Working.<|tool_calls_begin|><|tool_call_begin|>\nExecute\n<|tool_sep|>command\nls\n<|tool_call_end|><|tool_calls_end|>"))
	}))
	defer server.Close()

	result, err := NewXAIExecutor(&config.Config{}).ExecuteStream(context.Background(), xaiNativeTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "grok-4.6",
		Payload: []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"continue"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var stream bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		stream.Write(chunk.Payload)
		stream.WriteByte('\n')
	}
	text := stream.String()
	if !strings.Contains(text, `"name":"Execute"`) {
		t.Fatalf("stream missing Execute tool call: %s", text)
	}
	if !strings.Contains(text, `"finish_reason":"tool_calls"`) {
		t.Fatalf("stream missing finish_reason tool_calls: %s", text)
	}
	if strings.Contains(text, "tool_calls_begin") {
		t.Fatalf("markup leaked into stream: %s", text)
	}
	if !strings.Contains(text, "Working.") {
		t.Fatalf("stream dropped prefix text: %s", text)
	}
}

func TestXAIExecutorExecuteDoesNotRewriteWhenFunctionCallAlreadyPresent(t *testing.T) {
	functionCall := `{"id":"fc_1","type":"function_call","call_id":"call_1","name":"Read","arguments":"{\"path\":\"/tmp/a\"}","status":"completed"}`
	message := xaiNativeMessageItem("x<|tool_calls_begin|>")
	completed := []byte(`{"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"grok-4.6","output":[]}}`)
	completed, _ = sjson.SetRawBytes(completed, "response.output.-1", []byte(functionCall))
	completed, _ = sjson.SetRawBytes(completed, "response.output.-1", []byte(message))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\n", functionCall)
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":%s}\n\n", message)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", completed)
	}))
	defer server.Close()

	resp, err := NewXAIExecutor(&config.Config{}).Execute(context.Background(), xaiNativeTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "grok-4.6",
		Payload: []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"continue"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gjson.GetBytes(resp.Payload, "choices.0.message.tool_calls.0.function.name").String() != "Read" {
		t.Fatalf("existing tool call was rewritten: %s", resp.Payload)
	}
	if gjson.GetBytes(resp.Payload, "choices.0.message.tool_calls.#").Int() != 1 {
		t.Fatalf("tool_calls count = %s", resp.Payload)
	}
}

func xaiNativeTestAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider:   "xai",
		Attributes: map[string]string{"base_url": baseURL},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}
}

func xaiNativeMessageItem(text string) string {
	item := []byte(`{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":""}]}`)
	item, _ = sjson.SetBytes(item, "content.0.text", text)
	return string(item)
}

func xaiNativeCompletedResponse(text string) string {
	completed := []byte(`{"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"grok-4.6","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
	completed, _ = sjson.SetRawBytes(completed, "response.output.-1", []byte(xaiNativeMessageItem(text)))
	return string(completed)
}
