package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorPreservesResponsesInstructionRoles(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			bodies := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, errRead := io.ReadAll(request.Body)
				if errRead != nil {
					http.Error(writer, "cannot read request", http.StatusBadRequest)
					return
				}
				bodies <- body
				if stream {
					writer.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(writer, "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					return
				}
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, `{"id":"chatcmpl-test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			}))
			defer server.Close()
			executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
			auth := &cliproxyauth.Auth{Provider: "openai-compatibility", Attributes: map[string]string{"base_url": server.URL + "/v1", "api_key": "test-key"}}
			request := cliproxyexecutor.Request{
				Model: "test-model",
				Payload: []byte(`{"instructions":"base instructions","input":[
					{"role":"developer","content":"mode: plan"},
					{"role":"user","content":"mode: default is only user text"},
					{"role":"developer","content":[{"type":"input_text","text":"mode: default"}]},
					{"type":"function_call","call_id":"call_1","name":"inspect","arguments":"{}"},
					{"type":"function_call_output","call_id":"call_1","output":"read-only result"},
					{"role":"developer","content":"mode: plan"},
					{"role":"user","content":"continue"}
				],"tools":[{"type":"function","name":"inspect","parameters":{"type":"object","properties":{}}}]}`),
			}
			options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Stream: stream}
			if stream {
				result, errStream := executor.ExecuteStream(context.Background(), auth, request, options)
				if errStream != nil {
					t.Fatal(errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			} else if _, errExec := executor.Execute(context.Background(), auth, request, options); errExec != nil {
				t.Fatal(errExec)
			}
			body := <-bodies
			messages := gjson.GetBytes(body, "messages").Array()
			wantRoles := []string{"system", "developer", "user", "developer", "assistant", "tool", "developer", "user"}
			wantTexts := []string{"base instructions", "mode: plan", "mode: default is only user text", "mode: default", "", "read-only result", "mode: plan", "continue"}
			if len(messages) != len(wantRoles) {
				t.Fatalf("message count = %d, want %d; body=%s", len(messages), len(wantRoles), body)
			}
			for index, message := range messages {
				if role := message.Get("role").String(); role != wantRoles[index] {
					t.Errorf("message %d role = %q, want %q", index, role, wantRoles[index])
				}
				text := message.Get("content")
				if text.IsArray() {
					text = text.Get("0.text")
				}
				if text.String() != wantTexts[index] {
					t.Errorf("message %d text = %q, want %q", index, text.String(), wantTexts[index])
				}
			}
			if got := messages[4].Get("tool_calls.0.id").String(); got != "call_1" {
				t.Errorf("tool call id = %q, want call_1", got)
			}
			if got := messages[5].Get("tool_call_id").String(); got != "call_1" {
				t.Errorf("tool result id = %q, want call_1", got)
			}
		})
	}
}
