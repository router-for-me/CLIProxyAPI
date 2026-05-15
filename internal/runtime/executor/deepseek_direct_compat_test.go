package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
)

func TestConsumeDeepSeekSSEParsesTaggedToolCalls(t *testing.T) {
	raw := `user<tool_call name="TodoWrite">{"todos":"1. [in_progress] Eksplorasi"}</tool_call>
<tool_call name="Execute">{"command":"pwd"}</tool_call>`
	result, err := consumeDeepSeekSSE(context.Background(), strings.NewReader(deepSeekSSELine(t, raw)), false, &deepSeekContinueState{}, nil)
	if err != nil {
		t.Fatalf("consumeDeepSeekSSE() error = %v", err)
	}
	if result.Content != "" {
		t.Fatalf("Content = %q, want empty", result.Content)
	}
	if len(result.ToolCalls) != 2 {
		t.Fatalf("len(ToolCalls) = %d, want 2", len(result.ToolCalls))
	}
	if got := result.ToolCalls[0].Name; got != "TodoWrite" {
		t.Fatalf("ToolCalls[0].Name = %q, want TodoWrite", got)
	}
	if got := result.ToolCalls[0].Arguments; got != `{"todos":"1. [in_progress] Eksplorasi"}` {
		t.Fatalf("ToolCalls[0].Arguments = %q", got)
	}
	if got := result.ToolCalls[1].Name; got != "Execute" {
		t.Fatalf("ToolCalls[1].Name = %q, want Execute", got)
	}
}

func TestConsumeDeepSeekSSEParsesSplitToolCall(t *testing.T) {
	sse := deepSeekSSELine(t, "Intro <too") +
		deepSeekSSELine(t, `l_call name="Read">`) +
		deepSeekSSELine(t, `{"file_path":"c:\\repo\\README.md"}</tool_call> done`)
	result, err := consumeDeepSeekSSE(context.Background(), strings.NewReader(sse), false, &deepSeekContinueState{}, nil)
	if err != nil {
		t.Fatalf("consumeDeepSeekSSE() error = %v", err)
	}
	if strings.Contains(result.Content, "<tool_call") {
		t.Fatalf("Content leaked tool tag: %q", result.Content)
	}
	if result.Content != "Intro  done" {
		t.Fatalf("Content = %q, want %q", result.Content, "Intro  done")
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(result.ToolCalls))
	}
	if got := result.ToolCalls[0].Arguments; got != `{"file_path":"c:\\repo\\README.md"}` {
		t.Fatalf("ToolCalls[0].Arguments = %q", got)
	}
}

func TestConsumeDeepSeekSSEParsesMCPChildTagToolCall(t *testing.T) {
	raw := `wantsSaya akan menampilkan semua tabel di database menggunakan MySQL MCP.

<tool_call>
<tool_name>mysql___list_tables</tool_name>
</tool_call>`
	result, err := consumeDeepSeekSSE(context.Background(), strings.NewReader(deepSeekSSELine(t, raw)), false, &deepSeekContinueState{}, nil)
	if err != nil {
		t.Fatalf("consumeDeepSeekSSE() error = %v", err)
	}
	if strings.Contains(result.Content, "<tool_call") || strings.Contains(result.Content, "<tool_name") {
		t.Fatalf("Content leaked MCP tool tag: %q", result.Content)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(result.ToolCalls))
	}
	if got := result.ToolCalls[0].Name; got != "mysql___list_tables" {
		t.Fatalf("ToolCalls[0].Name = %q, want mysql___list_tables", got)
	}
	if got := result.ToolCalls[0].Arguments; got != "{}" {
		t.Fatalf("ToolCalls[0].Arguments = %q, want {}", got)
	}
}

func TestConsumeDeepSeekSSEToolModeSuppressesMCPPreamble(t *testing.T) {
	sse := deepSeekSSELine(t, "Saya") +
		deepSeekSSELine(t, " akan menampilkan semua tabel sekarang.\n\n") +
		deepSeekSSELine(t, "<") +
		deepSeekSSELine(t, "tool") +
		deepSeekSSELine(t, "_call") +
		deepSeekSSELine(t, ">\n<tool_name>mysql___list_tables</tool_name>\n</tool_call>")
	result, err := consumeDeepSeekSSEWithToolMode(context.Background(), strings.NewReader(sse), false, true, &deepSeekContinueState{}, nil)
	if err != nil {
		t.Fatalf("consumeDeepSeekSSEWithToolMode() error = %v", err)
	}
	if result.Content != "" {
		t.Fatalf("Content = %q, want empty when tool call is parsed in tool mode", result.Content)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(result.ToolCalls))
	}
	if got := result.ToolCalls[0].Name; got != "mysql___list_tables" {
		t.Fatalf("ToolCalls[0].Name = %q, want mysql___list_tables", got)
	}
}

func TestDeepSeekToolCallParserSupportsJSONEnvelope(t *testing.T) {
	parser := newDeepSeekToolCallParser()
	segments := parser.Push(`<tool_call>{"name":"Read","arguments":{"file_path":"x"}}</tool_call>`)
	segments = append(segments, parser.Finish()...)
	if len(segments) != 1 || segments[0].ToolCall == nil {
		t.Fatalf("segments = %#v, want one tool call", segments)
	}
	if got := segments[0].ToolCall.Name; got != "Read" {
		t.Fatalf("Name = %q, want Read", got)
	}
	if got := segments[0].ToolCall.Arguments; got != `{"file_path":"x"}` {
		t.Fatalf("Arguments = %q", got)
	}
}

func TestBuildOpenAIResponsesIncludeToolCalls(t *testing.T) {
	body := buildOpenAINonStreamResponse("deepseek-chat", "", "", []deepSeekToolCall{{
		ID:        "call_1",
		Name:      "TodoWrite",
		Arguments: `{"todos":"x"}`,
	}})
	if got := gjson.GetBytes(body, "choices.0.finish_reason").String(); got != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls", got)
	}
	if got := gjson.GetBytes(body, "choices.0.message.tool_calls.0.function.name").String(); got != "TodoWrite" {
		t.Fatalf("tool call name = %q, want TodoWrite", got)
	}

	chunk := strings.TrimSpace(strings.TrimPrefix(string(buildOpenAIToolCallStreamChunk("chatcmpl-1", "deepseek-chat", 1, deepSeekToolCall{
		ID:        "call_1",
		Name:      "TodoWrite",
		Arguments: `{"todos":"x"}`,
	}, 0, true)), "data: "))
	if got := gjson.Get(chunk, "choices.0.delta.tool_calls.0.function.name").String(); got != "TodoWrite" {
		t.Fatalf("stream tool call name = %q, want TodoWrite", got)
	}
	if got := gjson.Get(chunk, "choices.0.delta.role").String(); got != "assistant" {
		t.Fatalf("stream role = %q, want assistant", got)
	}
}

func TestStreamDeepSeekAsOpenAIEmitsStructuredToolCalls(t *testing.T) {
	exec := &DeepSeekProxyExecutor{cfg: &config.Config{}}
	upstream := &http.Response{
		Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(deepSeekSSELine(t,
			`wantsSaya akan menampilkan semua tabel.

<tool_call>
<tool_name>mysql___list_tables</tool_name>
</tool_call>`))),
	}
	var chunks []string
	err := exec.streamDeepSeekAsOpenAI(context.Background(), nil, &deepSeekAuth{}, upstream, "session-1", deepSeekRequest{
		RequestedModel: "deepseek-chat",
		ToolMode:       true,
	}, func(payload []byte) bool {
		chunks = append(chunks, string(payload))
		return true
	})
	if err != nil {
		t.Fatalf("streamDeepSeekAsOpenAI() error = %v", err)
	}
	joined := strings.Join(chunks, "")
	if strings.Contains(joined, "<tool_call") || strings.Contains(joined, "<tool_name") {
		t.Fatalf("stream output leaked tool tag: %s", joined)
	}
	if strings.Contains(joined, "Saya akan") || strings.Contains(joined, "menampilkan semua tabel") {
		t.Fatalf("stream output should suppress tool-call preamble in tool mode: %s", joined)
	}
	if !strings.Contains(joined, `"tool_calls"`) {
		t.Fatalf("stream output missing tool_calls: %s", joined)
	}
	if !strings.Contains(joined, `"name":"mysql___list_tables"`) {
		t.Fatalf("stream output missing MCP tool name: %s", joined)
	}
	if !strings.Contains(joined, `"finish_reason":"tool_calls"`) {
		t.Fatalf("stream output missing tool_calls finish reason: %s", joined)
	}
}

func TestBuildDeepSeekPromptIncludesAssistantToolCalls(t *testing.T) {
	prompt, err := buildDeepSeekPrompt(map[string]any{
		"messages": []any{map[string]any{
			"role": "assistant",
			"tool_calls": []any{map[string]any{
				"id":   "call_1",
				"type": "function",
				"function": map[string]any{
					"name":      "Read",
					"arguments": `{"file_path":"x"}`,
				},
			}},
		}},
	})
	if err != nil {
		t.Fatalf("buildDeepSeekPrompt() error = %v", err)
	}
	if !strings.Contains(prompt, `<tool_call name="Read">{"file_path":"x"}</tool_call>`) {
		t.Fatalf("prompt missing assistant tool call: %s", prompt)
	}
}

func deepSeekSSELine(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"p": "response/content", "v": value})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return "data: " + string(encoded) + "\n\n"
}
