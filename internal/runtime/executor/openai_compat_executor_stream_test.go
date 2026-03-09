package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorExecuteStreamSetsIncludeUsage(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"hi"}}]}

`))
		_, _ = w.Write([]byte(`data: [DONE]

`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-4o-mini",
		Payload: []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
		}
	}
	if !gjson.GetBytes(gotBody, "stream_options.include_usage").Bool() {
		t.Fatalf("expected stream_options.include_usage=true, got body: %s", string(gotBody))
	}
}

func TestOpenAICompatExecutorExecuteStreamRetriesWithoutInjectedIncludeUsage(t *testing.T) {
	var gotBodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		gotBodies = append(gotBodies, body)
		if len(gotBodies) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"unknown field stream_options.include_usage"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"hi"}}]}

`))
		_, _ = w.Write([]byte(`data: [DONE]

`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-4o-mini",
		Payload: []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
		}
	}
	if len(gotBodies) != 2 {
		t.Fatalf("expected 2 upstream requests, got %d", len(gotBodies))
	}
	if !gjson.GetBytes(gotBodies[0], "stream_options.include_usage").Bool() {
		t.Fatalf("expected first request to include injected usage flag, got body: %s", string(gotBodies[0]))
	}
	if gjson.GetBytes(gotBodies[1], "stream_options.include_usage").Exists() {
		t.Fatalf("expected retry request to remove include_usage, got body: %s", string(gotBodies[1]))
	}
}

func TestOpenAICompatExecutorExecuteStreamDoesNotRetryUnrelatedValidationErrors(t *testing.T) {
	var gotBodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		gotBodies = append(gotBodies, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":{"message":"validation failed for messages[0].content; request trace mentions stream_options.include_usage but the actual issue is content shape"}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	_, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-4o-mini",
		Payload: []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err == nil {
		t.Fatal("expected ExecuteStream to return validation error")
	}
	if len(gotBodies) != 1 {
		t.Fatalf("expected 1 upstream request for unrelated validation error, got %d", len(gotBodies))
	}
	if !gjson.GetBytes(gotBodies[0], "stream_options.include_usage").Bool() {
		t.Fatalf("expected original request to include injected usage flag, got body: %s", string(gotBodies[0]))
	}
	if statusProvider, ok := err.(interface{ StatusCode() int }); !ok || statusProvider.StatusCode() != http.StatusUnprocessableEntity {
		t.Fatalf("expected status code 422, got: %v", err)
	}
}
