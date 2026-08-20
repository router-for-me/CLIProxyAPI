package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAnnotateXAIClaudeToolResults(t *testing.T) {
	t.Parallel()

	source := []byte(`{
		"messages":[{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"call_ok","content":"updated"},
			{"type":"tool_result","tool_use_id":"call_err","is_error":true,"content":"exit 1"}
		]}]
	}`)
	body := []byte(`{"input":[
		{"type":"function_call_output","call_id":"call_ok","output":"updated"},
		{"type":"function_call_output","call_id":"call_err","output":"exit 1"},
		{"type":"function_call_output","call_id":"unmatched","output":"unchanged"}
	]}`)

	got := helps.AnnotateXAIClaudeToolResults(body, source, sdktranslator.FormatClaude)
	if output := gjson.GetBytes(got, "input.0.output").String(); output != helps.XAIClaudeToolResultSuccessPrefix+"updated" {
		t.Fatalf("success output = %q", output)
	}
	if output := gjson.GetBytes(got, "input.1.output").String(); output != helps.XAIClaudeToolResultErrorPrefix+"exit 1" {
		t.Fatalf("error output = %q", output)
	}
	if output := gjson.GetBytes(got, "input.2.output").String(); output != "unchanged" {
		t.Fatalf("unmatched output changed: %q", output)
	}
	if callID := gjson.GetBytes(got, "input.1.call_id").String(); callID != "call_err" {
		t.Fatalf("call_id changed: %q", callID)
	}

	gotAgain := helps.AnnotateXAIClaudeToolResults(got, source, sdktranslator.FormatClaude)
	if string(gotAgain) != string(got) {
		t.Fatalf("annotation must be idempotent:\nfirst: %s\nagain: %s", got, gotAgain)
	}
	if other := helps.AnnotateXAIClaudeToolResults(body, source, sdktranslator.FormatOpenAIResponse); string(other) != string(body) {
		t.Fatalf("non-Claude source changed: %s", other)
	}
}

func TestAnnotateXAIClaudeToolResultPreservesMultimodalOutput(t *testing.T) {
	t.Parallel()
	source := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_image","is_error":true,"content":[]}]}]}`)
	body := []byte(`{"input":[{"type":"function_call_output","call_id":"call_image","output":[{"type":"input_text","text":"failed"},{"type":"input_image","image_url":"data:image/png;base64,AA=="}]}]}`)

	got := helps.AnnotateXAIClaudeToolResults(body, source, sdktranslator.FormatClaude)
	output := gjson.GetBytes(got, "input.0.output")
	if !output.IsArray() || len(output.Array()) != 3 {
		t.Fatalf("multimodal output shape changed: %s", got)
	}
	if marker := output.Get("0.text").String(); marker != strings.TrimSuffix(helps.XAIClaudeToolResultErrorPrefix, "\n") {
		t.Fatalf("error marker = %q", marker)
	}
	if image := output.Get("2.image_url").String(); image != "data:image/png;base64,AA==" {
		t.Fatalf("image output changed: %q", image)
	}
}

func TestXAIExecutorPrepareAnnotatesClaudeToolResultsForStreamModes(t *testing.T) {
	t.Parallel()

	for _, stream := range []bool{false, true} {
		stream := stream
		t.Run(fmt.Sprintf("stream_%t", stream), func(t *testing.T) {
			t.Parallel()
			exec := NewXAIExecutor(&config.Config{})
			prepared, errPrepare := exec.prepareResponsesRequest(context.Background(), cliproxyexecutor.Request{
				Model: "grok-4.6",
				Payload: []byte(`{
					"model":"grok-4.6",
					"messages":[
						{"role":"user","content":"run once"},
						{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"probe","input":{"value":7}}]},
						{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","is_error":true,"content":"failed"}]}
					],
					"tools":[{"name":"probe","description":"probe","input_schema":{"type":"object","properties":{"value":{"type":"integer"}}}}]
				}`),
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: stream}, stream)
			if errPrepare != nil {
				t.Fatalf("prepareResponsesRequest() error = %v", errPrepare)
			}
			output := gjson.GetBytes(prepared.body, `input.#(type=="function_call_output").output`).String()
			if output != helps.XAIClaudeToolResultErrorPrefix+"failed" {
				t.Fatalf("tool output = %q; body=%s", output, prepared.body)
			}
			if callID := gjson.GetBytes(prepared.body, `input.#(type=="function_call_output").call_id`).String(); callID != "call_1" {
				t.Fatalf("call_id = %q; body=%s", callID, prepared.body)
			}
		})
	}
}

func TestXAIClaudeToolRepeatGuard(t *testing.T) {
	t.Parallel()
	body := []byte(`{"input":[
		{"type":"function_call","call_id":"old","name":"Write","arguments":"{\"path\":\"a\",\"text\":\"x\"}"},
		{"type":"function_call_output","call_id":"old","output":"[tool_result status=success] done"}
	]}`)
	guard := helps.NewXAIClaudeToolRepeatGuard(nil, body, sdktranslator.FormatClaude)
	if guard == nil {
		t.Fatal("repeat guard is nil")
	}
	completed := []byte(`{"type":"response.completed","response":{"output":[
		{"type":"function_call","call_id":"new","name":"Write","arguments":"{\"text\":\"x\",\"path\":\"a\"}"}
	]}}`)
	patched, blocked := guard.PatchCompleted(completed)
	if !blocked {
		t.Fatalf("duplicate call was not blocked: %s", patched)
	}
	if gjson.GetBytes(patched, "response.output.0.type").String() != "message" {
		t.Fatalf("duplicate was not replaced with a message: %s", patched)
	}

	changed := []byte(`{"type":"response.completed","response":{"output":[
		{"type":"function_call","call_id":"new","name":"Write","arguments":"{\"text\":\"y\",\"path\":\"a\"}"}
	]}}`)
	unchanged, blocked := guard.PatchCompleted(changed)
	if blocked || string(unchanged) != string(changed) {
		t.Fatalf("changed arguments must be allowed: %s", unchanged)
	}
}

func TestXAIClaudeToolRepeatGuardUsesOriginalAnthropicHistory(t *testing.T) {
	t.Parallel()
	source := []byte(`{"messages":[
		{"role":"user","content":"update both"},
		{"role":"assistant","content":[
			{"type":"tool_use","id":"call_write","name":"Write","input":{"path":"a","text":"x"}},
			{"type":"tool_use","id":"call_bash","name":"Bash","input":{"command":"compile"}}
		]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"call_write","content":"updated"},
			{"type":"tool_result","tool_use_id":"call_bash","is_error":true,"content":"exit 1"}
		]}
	]}`)
	// Replay/sanitization may leave the output without its historical call. The
	// guard must use the original Anthropic history rather than this body.
	body := []byte(`{"input":[
		{"type":"reasoning","encrypted_content":"opaque"},
		{"type":"function_call_output","call_id":"call_write","output":"updated"},
		{"type":"function_call_output","call_id":"call_bash","output":"exit 1"}
	]}`)
	guard := helps.NewXAIClaudeToolRepeatGuard(source, body, sdktranslator.FormatClaude)
	if guard == nil {
		t.Fatal("original history did not create a repeat guard")
	}

	completed := []byte(`{"type":"response.completed","response":{"output":[
		{"type":"function_call","id":"item_new","call_id":"new","name":"Write","arguments":"{\"text\":\"x\",\"path\":\"a\"}"},
		{"type":"function_call","id":"item_changed","call_id":"changed","name":"Bash","arguments":"{\"command\":\"compile --verbose\"}"}
	]}}`)
	patched, blocked := guard.PatchCompleted(completed)
	if !blocked {
		t.Fatalf("duplicate from original history was not blocked: %s", patched)
	}
	if gjson.GetBytes(patched, `response.output.#(call_id=="new")`).Exists() {
		t.Fatalf("duplicate call remained: %s", patched)
	}
	if !gjson.GetBytes(patched, `response.output.#(call_id=="changed")`).Exists() {
		t.Fatalf("changed call was removed: %s", patched)
	}
	if !guard.IsBlockedCallEvent(gjson.Parse(`{"type":"response.function_call_arguments.delta","item_id":"item_new"}`)) {
		t.Fatal("blocked output item id was not tracked for stream filtering")
	}
}

func TestXAIExecutorBlocksRepeatedClaudeToolCall(t *testing.T) {
	t.Parallel()

	for _, stream := range []bool{false, true} {
		stream := stream
		t.Run(fmt.Sprintf("stream_%t", stream), func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"new\",\"name\":\"Write\",\"arguments\":\"{\\\"text\\\":\\\"x\\\",\\\"path\\\":\\\"a\\\"}\"}}\n")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"call_id\":\"new\",\"name\":\"Write\",\"arguments\":\"{\\\"text\\\":\\\"x\\\",\\\"path\\\":\\\"a\\\"}\"}}\n")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"grok-4.6\",\"output\":[{\"type\":\"function_call\",\"call_id\":\"new\",\"name\":\"Write\",\"arguments\":\"{\\\"text\\\":\\\"x\\\",\\\"path\\\":\\\"a\\\"}\"}],\"usage\":{}}}\n")
			}))
			defer server.Close()

			executor := NewXAIExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Provider: "xai", Attributes: map[string]string{"base_url": server.URL, "auth_kind": "oauth"}}
			payload := []byte(`{
				"model":"grok-4.6",
				"messages":[
					{"role":"assistant","content":[{"type":"tool_use","id":"old","name":"Write","input":{"path":"a","text":"x"}}]},
					{"role":"user","content":[{"type":"tool_result","tool_use_id":"old","content":"updated","is_error":false}]}
				],
				"tools":[{"name":"Write","input_schema":{"type":"object","properties":{"path":{"type":"string"},"text":{"type":"string"}}}}]
			}`)
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: stream}
			if stream {
				result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: "grok-4.6", Payload: payload}, opts)
				if err != nil {
					t.Fatalf("ExecuteStream() error = %v", err)
				}
				var output strings.Builder
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error = %v", chunk.Err)
					}
					output.Write(chunk.Payload)
				}
				assertRepeatedToolBlocked(t, output.String())
				return
			}

			response, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "grok-4.6", Payload: payload}, opts)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			assertRepeatedToolBlocked(t, string(response.Payload))
		})
	}
}

func assertRepeatedToolBlocked(t *testing.T, output string) {
	t.Helper()
	if strings.Contains(output, `"type":"tool_use"`) {
		t.Fatalf("repeated tool_use reached Claude output: %s", output)
	}
	if !strings.Contains(output, helps.XAIClaudeRepeatedToolMessage) {
		t.Fatalf("blocked-repeat message missing: %s", output)
	}
}
