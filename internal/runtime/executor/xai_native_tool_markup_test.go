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
	if gjson.GetBytes(resp.Payload, "choices.0.message.tool_calls.0").Exists() {
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
