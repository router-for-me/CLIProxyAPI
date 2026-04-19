package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorExecute_EmptyStreamCompletionOutputUsesOutputItemDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]},\"output_index\":0}\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":1775555723,\"status\":\"completed\",\"model\":\"gpt-5.4-mini-2026-03-17\",\"output\":[],\"usage\":{\"input_tokens\":8,\"output_tokens\":28,\"total_tokens\":36}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4-mini",
		Payload: []byte(`{"model":"gpt-5.4-mini","messages":[{"role":"user","content":"Say ok"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	gotContent := gjson.GetBytes(resp.Payload, "choices.0.message.content").String()
	if gotContent != "ok" {
		t.Fatalf("choices.0.message.content = %q, want %q; payload=%s", gotContent, "ok", string(resp.Payload))
	}
}

func TestCodexExecutorExecuteStream_EmptyCompletionOutputUsesOutputItemDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]},\"output_index\":0}\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":1775555723,\"status\":\"completed\",\"model\":\"gpt-5.4-mini-2026-03-17\",\"output\":[],\"usage\":{\"input_tokens\":8,\"output_tokens\":28,\"total_tokens\":36}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"Say ok","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var chunks [][]byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		chunks = append(chunks, chunk.Payload)
	}

	var completedPayload []byte
	for _, chunk := range chunks {
		text := strings.TrimSpace(string(chunk))
		if !strings.HasPrefix(text, "data: ") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(text, "data: "))
		if gjson.Get(payload, "type").String() != "response.completed" {
			continue
		}
		completedPayload = []byte(payload)
		break
	}
	if len(completedPayload) == 0 {
		t.Fatalf("missing response.completed chunk in stream: %q", chunks)
	}

	output := gjson.GetBytes(completedPayload, "response.output")
	if !output.Exists() || len(output.Array()) != 1 {
		t.Fatalf("response.output = %s, want 1 recovered item; completed=%s", output.Raw, string(completedPayload))
	}
	if got := output.Array()[0].Get("content.0.text").String(); got != "ok" {
		t.Fatalf("response.output[0].content[0].text = %q, want %q; completed=%s", got, "ok", string(completedPayload))
	}
}

func TestCodexExecutorExecuteStream_EmptyCompletionOutputUsesFunctionCallState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"id\":\"fc_call_1\",\"type\":\"function_call\",\"status\":\"in_progress\",\"arguments\":\"\",\"call_id\":\"call_1\",\"name\":\"skill\"}}\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_call_1\",\"output_index\":1,\"delta\":\"{\\\"path\\\":\\\"README.md\\\"}\"}\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.function_call_arguments.done\",\"item_id\":\"fc_call_1\",\"output_index\":1,\"arguments\":\"{\\\"path\\\":\\\"README.md\\\"}\"}\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_2\",\"object\":\"response\",\"created_at\":1775555723,\"status\":\"completed\",\"model\":\"gpt-5.4-2026-03-05\",\"output\":[],\"usage\":{\"input_tokens\":8,\"output_tokens\":28,\"total_tokens\":36}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"Use a tool","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var completedPayload []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		text := strings.TrimSpace(string(chunk.Payload))
		if !strings.HasPrefix(text, "data: ") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(text, "data: "))
		if gjson.Get(payload, "type").String() != "response.completed" {
			continue
		}
		completedPayload = []byte(payload)
	}
	if len(completedPayload) == 0 {
		t.Fatal("missing response.completed chunk")
	}

	output := gjson.GetBytes(completedPayload, "response.output")
	if !output.Exists() || len(output.Array()) != 1 {
		t.Fatalf("response.output = %s, want 1 recovered function_call; completed=%s", output.Raw, string(completedPayload))
	}
	item := output.Array()[0]
	if got := item.Get("type").String(); got != "function_call" {
		t.Fatalf("response.output[0].type = %q, want %q; completed=%s", got, "function_call", string(completedPayload))
	}
	if got := item.Get("call_id").String(); got != "call_1" {
		t.Fatalf("response.output[0].call_id = %q, want %q; completed=%s", got, "call_1", string(completedPayload))
	}
	if got := item.Get("name").String(); got != "skill" {
		t.Fatalf("response.output[0].name = %q, want %q; completed=%s", got, "skill", string(completedPayload))
	}
	if got := item.Get("arguments").String(); got != "{\"path\":\"README.md\"}" {
		t.Fatalf("response.output[0].arguments = %q, want %q; completed=%s", got, "{\"path\":\"README.md\"}", string(completedPayload))
	}
}
