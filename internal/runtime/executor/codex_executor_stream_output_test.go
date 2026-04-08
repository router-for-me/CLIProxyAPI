package executor

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexResponseOutputCollectorPatchCompletedOutput_SortsByOutputIndex(t *testing.T) {
	collector := newCodexResponseOutputCollector()
	collector.AddOutputItemDone([]byte(`{"type":"response.output_item.done","item":{"type":"message","id":"msg_2"},"output_index":2}`))
	collector.AddOutputItemDone([]byte(`{"type":"response.output_item.done","item":{"type":"message","id":"msg_0"},"output_index":0}`))
	collector.AddOutputItemDone([]byte(`{"type":"response.output_item.done","item":{"type":"message","id":"msg_fallback"}}`))

	patched := collector.PatchCompletedOutput([]byte(`{"type":"response.completed","response":{"id":"resp_1","output":[]}}`))
	output := gjson.GetBytes(patched, "response.output")
	if !output.IsArray() {
		t.Fatalf("response.output should be an array: %s", patched)
	}
	if len(output.Array()) != 3 {
		t.Fatalf("response.output length = %d, want 3; payload=%s", len(output.Array()), patched)
	}
	if output.Array()[0].Get("id").String() != "msg_0" {
		t.Fatalf("response.output[0].id = %q, want %q; payload=%s", output.Array()[0].Get("id").String(), "msg_0", patched)
	}
	if output.Array()[1].Get("id").String() != "msg_2" {
		t.Fatalf("response.output[1].id = %q, want %q; payload=%s", output.Array()[1].Get("id").String(), "msg_2", patched)
	}
	if output.Array()[2].Get("id").String() != "msg_fallback" {
		t.Fatalf("response.output[2].id = %q, want %q; payload=%s", output.Array()[2].Get("id").String(), "msg_fallback", patched)
	}
}

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

func TestCodexExecutorExecuteStream_EmptyStreamCompletionOutputUsesOutputItemDone(t *testing.T) {
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
		Model:   "gpt-5.4-mini",
		Payload: []byte(`{"model":"gpt-5.4-mini","input":"Say ok","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("codex"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var stream bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		stream.Write(chunk.Payload)
		stream.WriteByte('\n')
	}

	var completedData []byte
	for _, line := range bytes.Split(stream.Bytes(), []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if gjson.GetBytes(data, "type").String() != "response.completed" {
			continue
		}
		completedData = append([]byte(nil), data...)
	}
	if len(completedData) == 0 {
		t.Fatalf("missing response.completed event in stream: %s", stream.Bytes())
	}

	output := gjson.GetBytes(completedData, "response.output")
	if !output.IsArray() || len(output.Array()) == 0 {
		t.Fatalf("response.output should be populated: %s", completedData)
	}
	gotContent := output.Array()[0].Get("content.0.text").String()
	if gotContent != "ok" {
		t.Fatalf("response.output[0].content[0].text = %q, want %q; payload=%s", gotContent, "ok", completedData)
	}
}
