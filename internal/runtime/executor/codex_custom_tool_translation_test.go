package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func codexCustomToolChatPayload() []byte {
	return []byte(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"Apply the patch."}],"tools":[{"type":"custom","name":"ApplyPatch","description":"Apply a freeform patch.","format":{"type":"text"}}]}`)
}

func codexCustomToolStreamEvents() []string {
	return []string{
		`{"type":"response.created","response":{"id":"resp_1","created_at":1700000000,"model":"gpt-5.6-sol"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"ctc_1","type":"custom_tool_call","call_id":"call_apply_patch","name":"ApplyPatch","input":"","status":"in_progress"}}`,
		`{"type":"response.custom_tool_call_input.delta","output_index":0,"item_id":"ctc_1","delta":"abc"}`,
		`{"type":"response.custom_tool_call_input.delta","output_index":0,"item_id":"ctc_1","delta":"def"}`,
		`{"type":"response.custom_tool_call_input.done","output_index":0,"item_id":"ctc_1","input":"abcdef"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"ctc_1","type":"custom_tool_call","call_id":"call_apply_patch","name":"ApplyPatch","input":"abcdef","status":"completed"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","created_at":1700000000,"status":"completed","model":"gpt-5.6-sol","output":[{"id":"ctc_1","type":"custom_tool_call","call_id":"call_apply_patch","name":"ApplyPatch","input":"abcdef","status":"completed"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
	}
}

func codexCustomToolExecutorOptions() cliproxyexecutor.Options {
	return cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	}
}

func assertCodexCustomToolStream(t *testing.T, result *cliproxyexecutor.StreamResult) {
	t.Helper()

	var callID string
	var name string
	var input string
	var finishReason string
	announcements := 0
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		root := gjson.ParseBytes(chunk.Payload)
		for _, toolCall := range root.Get("choices.0.delta.tool_calls").Array() {
			if id := toolCall.Get("id"); id.Exists() && id.String() != "" {
				callID = id.String()
				announcements++
			}
			if toolName := toolCall.Get("function.name"); toolName.Exists() && toolName.String() != "" {
				name = toolName.String()
			}
			if arguments := toolCall.Get("function.arguments"); arguments.Exists() {
				input += arguments.String()
			}
		}
		if reason := root.Get("choices.0.finish_reason"); reason.Exists() && reason.String() != "" {
			finishReason = reason.String()
		}
	}

	if callID != "call_apply_patch" || name != "ApplyPatch" {
		t.Fatalf("custom tool metadata call_id=%q name=%q", callID, name)
	}
	if input != "abcdef" {
		t.Fatalf("custom tool input = %q, want exactly abcdef", input)
	}
	if announcements != 1 {
		t.Fatalf("custom tool announced %d times, want once", announcements)
	}
	if finishReason != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls", finishReason)
	}
}

func TestCodexExecutorCustomToolUsesChatCompletionsTranslator(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range codexCustomToolStreamEvents() {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	defer server.Close()

	exec := NewCodexExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "sk-test",
			"base_url": server.URL,
		},
	}
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: codexCustomToolChatPayload(),
	}, codexCustomToolExecutorOptions())
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	assertCodexCustomToolStream(t, result)
}

func TestCodexWebsocketsExecutorCustomToolUsesChatCompletionsTranslator(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()

		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			t.Errorf("read upstream websocket request: %v", errRead)
			return
		}
		for _, event := range codexCustomToolStreamEvents() {
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(event)); errWrite != nil {
				t.Errorf("write upstream websocket event: %v", errWrite)
				return
			}
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Attributes: map[string]string{
			"api_key":  "sk-test",
			"base_url": server.URL,
		},
	}
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: codexCustomToolChatPayload(),
	}, codexCustomToolExecutorOptions())
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	assertCodexCustomToolStream(t, result)
}
