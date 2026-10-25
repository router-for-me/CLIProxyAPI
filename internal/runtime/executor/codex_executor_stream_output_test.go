package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorExecute_NonEmptyCompletionOutputHydratesMissingItemID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"id":"fc_123","type":"function_call","call_id":"call_123","name":"weather","arguments":"{}"},"output_index":0}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","item":{"id":"fc_done_existing","type":"function_call","call_id":"call_existing","name":"other","arguments":"{}"},"output_index":1}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","output":[{"id":null,"type":"function_call","call_id":"call_123","name":"weather-terminal","arguments":"{}"},{"id":"fc_existing","type":"function_call","call_id":"call_existing","name":"preserved","arguments":"{}"}]}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"What is the weather?"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gjson.GetBytes(resp.Payload, "output.0.id").String(); got != "fc_123" {
		t.Fatalf("output[0].id = %q, want %q; payload=%s", got, "fc_123", resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "output.0.name").String(); got != "weather-terminal" {
		t.Fatalf("output[0].name = %q, want terminal value; payload=%s", got, resp.Payload)
	}
	if got := gjson.GetBytes(resp.Payload, "output.1.id").String(); got != "fc_existing" {
		t.Fatalf("output[1].id = %q, want existing value; payload=%s", got, resp.Payload)
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

func TestCodexExecutorExecuteSurfacesTerminalStreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: error\n"))
		_, _ = w.Write([]byte(`data: {"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again.","param":"input"},"sequence_number":2}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.failed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again."}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err == nil {
		t.Fatal("expected terminal stream error, got nil")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusBadRequest, err)
	}
	assertCodexErrorCode(t, err.Error(), "invalid_request_error", "context_too_large")
	if !strings.Contains(err.Error(), "Your input exceeds the context window") {
		t.Fatalf("error message missing upstream context text: %v", err)
	}
}

func TestCodexExecutorExecuteIncompleteResponseIsSuccessful(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"response.incomplete","response":{"id":"resp_1","model":"gpt-5.5","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := gjson.GetBytes(resp.Payload, "stop_reason").String(); got != "max_tokens" {
		t.Fatalf("stop_reason = %q, want %q; payload=%s", got, "max_tokens", resp.Payload)
	}
}

func TestCodexExecutorExecuteExplicitTerminalFailureIsNotRequestScoped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"type":"error","error":{"type":"invalid_request_error","code":"invalid_value","message":"Invalid input."}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err == nil {
		t.Fatal("expected explicit terminal failure, got nil")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusBadRequest, err)
	}
	assertNotRequestScopedTestError(t, err)
}

func TestCodexExecutorExecuteMissingCompletionIsRequestScoped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.5\"}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err == nil {
		t.Fatal("expected missing-completion error, got nil")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusRequestTimeout {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusRequestTimeout, err)
	}
	assertRequestScopedTestError(t, err)
}

func TestCodexExecutorExecuteStreamMissingCompletionIsRequestScoped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.5\"}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if streamErr == nil {
		t.Fatal("expected missing-completion stream error, got nil")
	}
	if got := statusCodeFromTestError(t, streamErr); got != http.StatusRequestTimeout {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusRequestTimeout, streamErr)
	}
	assertRequestScopedTestError(t, streamErr)
}

func TestCodexExecutorExecuteStreamExplicitTerminalFailureIsNotSuccessful(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.5\"}}\n\n"))
		_, _ = w.Write([]byte(`data: {"type":"error","error":{"type":"invalid_request_error","code":"invalid_value","message":"Invalid input."}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if streamErr == nil {
		t.Fatal("expected explicit terminal stream error, got nil")
	}
	if got := statusCodeFromTestError(t, streamErr); got != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusBadRequest, streamErr)
	}
	assertNotRequestScopedTestError(t, streamErr)
}

func TestCodexAutoExecutorHTTPFallbackForwardsSequentialCutoffReasoningSummaryDelivery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request body: %v", errRead)
			return
		}
		if gjson.GetBytes(body, "stream_options.include_usage").Exists() {
			t.Errorf("unsupported stream option was forwarded: %s", body)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		if delivery := gjson.GetBytes(body, "stream_options.reasoning_summary_delivery").String(); delivery == "sequential_cutoff" {
			_, _ = w.Write([]byte(`data: {"type":"response.reasoning_summary_text.done","item_id":"rs_1","summary_index":0,"text":"Checking"}` + "\n\n"))
		} else {
			_, _ = w.Write([]byte(`data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","summary_index":0,"delta":"Checking"}` + "\n\n"))
		}
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexAutoExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}
	result, err := executor.ExecuteStream(cliproxyexecutor.WithDownstreamWebsocket(context.Background()), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"gpt-5.6-sol","input":"hello","reasoning":{"summary":"detailed"},"stream_options":{"reasoning_summary_delivery":"sequential_cutoff","include_usage":true}}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
		Stream:         true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var output bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		output.Write(chunk.Payload)
	}
	if !strings.Contains(output.String(), `"type":"response.reasoning_summary_text.done"`) {
		t.Fatalf("missing sequential-cutoff summary event; output=%s", output.String())
	}
}

func TestCodexExecutorTransportFailureBeforeTerminalIsRequestScoped(t *testing.T) {
	tests := []struct {
		name   string
		stream bool
	}{
		{name: "non-streaming"},
		{name: "streaming", stream: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			created := []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.5\"}}\n\n")
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"text/event-stream"}},
					Body:       io.NopCloser(io.MultiReader(bytes.NewReader(created), unexpectedEOFReader{})),
					Request:    req,
				}, nil
			}))

			executor := NewCodexExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"base_url": "http://codex.test",
				"api_key":  "test",
			}}
			req := cliproxyexecutor.Request{Model: "gpt-5.5", Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: tc.stream}

			var terminalErr error
			if tc.stream {
				result, errStream := executor.ExecuteStream(ctx, auth, req, opts)
				if errStream != nil {
					t.Fatalf("ExecuteStream error: %v", errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						terminalErr = chunk.Err
					}
				}
			} else {
				_, terminalErr = executor.Execute(ctx, auth, req, opts)
			}
			if terminalErr == nil {
				t.Fatal("expected transport failure before terminal event")
			}
			if got := statusCodeFromTestError(t, terminalErr); got != http.StatusRequestTimeout {
				t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusRequestTimeout, terminalErr)
			}
			assertRequestScopedTestError(t, terminalErr)
		})
	}
}

func TestCodexExecutorExecuteIgnoresTransportErrorAfterCompletion(t *testing.T) {
	completed := []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.5\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       io.NopCloser(io.MultiReader(bytes.NewReader(completed), unexpectedEOFReader{})),
			Request:    req,
		}, nil
	}))

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": "http://codex.test",
		"api_key":  "test",
	}}

	resp, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("unexpected error after response.completed: %v", err)
	}
	if got := gjson.GetBytes(resp.Payload, "id").String(); got != "resp_1" {
		t.Fatalf("response id = %q, want resp_1; payload=%s", got, resp.Payload)
	}
}

func TestCodexExecutorExecuteStreamIgnoresTransportErrorAfterCompletion(t *testing.T) {
	completed := []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.5\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       io.NopCloser(io.MultiReader(bytes.NewReader(completed), unexpectedEOFReader{})),
			Request:    req,
		}, nil
	}))

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": "http://codex.test",
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if streamErr != nil {
		t.Fatalf("unexpected error after response.completed: %v", streamErr)
	}
}

func TestCodexExecutorExecuteStreamSurfacesTerminalStreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: error\n"))
		_, _ = w.Write([]byte(`data: {"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again.","param":"input"},"sequence_number":2}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			break
		}
	}
	if streamErr == nil {
		t.Fatal("missing stream terminal error")
	}
	if got := statusCodeFromTestError(t, streamErr); got != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusBadRequest, streamErr)
	}
	assertCodexErrorCode(t, streamErr.Error(), "invalid_request_error", "context_too_large")
}

func TestCodexTerminalStreamContextLengthErrFromResponseFailed(t *testing.T) {
	err, ok := codexTerminalStreamContextLengthErr([]byte(`{"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again."}}}`))
	if !ok {
		t.Fatal("expected context length terminal error")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusBadRequest, err)
	}
	assertCodexErrorCode(t, err.Error(), "invalid_request_error", "context_too_large")
}

func TestCodexTerminalStreamContextLengthErrFromTopLevelError(t *testing.T) {
	err, ok := codexTerminalStreamContextLengthErr([]byte(`{"type":"error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model. Please adjust your input and try again.","sequence_number":2}`))
	if !ok {
		t.Fatal("expected top-level context length terminal error")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusBadRequest, err)
	}
	assertCodexErrorCode(t, err.Error(), "invalid_request_error", "context_too_large")
	if !strings.Contains(err.Error(), "Your input exceeds the context window") {
		t.Fatalf("error message missing upstream context text: %v", err)
	}
}

func TestCodexTerminalStreamContextLengthErrIgnoresOtherTerminalErrors(t *testing.T) {
	_, ok := codexTerminalStreamContextLengthErr([]byte(`{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Rate limit reached."}}`))
	if ok {
		t.Fatal("rate limit terminal error should not be handled by context length fix")
	}
}

func TestCodexTerminalStreamErrIgnoresRateLimitTerminalErrors(t *testing.T) {
	_, _, ok := codexTerminalStreamErr([]byte(`{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Rate limit reached."}}`))
	if ok {
		t.Fatal("rate limit terminal error should not be handled by replay terminal error path")
	}
}

func TestCodexTerminalFailureErrClassifiesStatus(t *testing.T) {
	tests := []struct {
		name       string
		event      string
		wantStatus int
	}{
		{
			name:       "invalid request",
			event:      `{"type":"error","error":{"type":"invalid_request_error","code":"invalid_value","message":"Invalid input."}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "cyber policy",
			event:      `{"type":"error","error":{"type":"invalid_request","code":"cyber_policy","message":"This content was flagged for possible cybersecurity risk."}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "authentication",
			event:      `{"type":"response.failed","response":{"error":{"type":"authentication_error","code":"invalid_api_key","message":"Invalid token."}}}`,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "rate limit",
			event:      `{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Rate limit reached."}}`,
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name:       "unknown upstream failure",
			event:      `{"type":"response.failed","response":{"error":{"type":"upstream_error","code":"unknown","message":"Upstream failed."}}}`,
			wantStatus: http.StatusBadGateway,
		},
		// Overload rejections keep falling through to 502 here. The 503 restoration is scoped to
		// the opt-in bootstrap buffering path so this shared mapping stays unchanged.
		{
			name:       "overload stays a bad gateway without buffering",
			event:      `{"type":"error","error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}`,
			wantStatus: http.StatusBadGateway,
		},
		{
			name:       "model not found with invalid_request_error type maps to 404",
			event:      `{"type":"error","error":{"type":"invalid_request_error","code":"model_not_found","message":"The model gpt-5.5 does not exist or you do not have access to it."}}`,
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			streamErr, _, ok := codexTerminalFailureErr([]byte(tc.event))
			if !ok {
				t.Fatal("expected terminal failure to be handled")
			}
			if got := streamErr.StatusCode(); got != tc.wantStatus {
				t.Fatalf("status code = %d, want %d; err=%v", got, tc.wantStatus, streamErr)
			}
		})
	}
}

func TestCodexTerminalStreamErrHandlesUsageLimitErrorEvent(t *testing.T) {
	streamErr, _, ok := codexTerminalStreamErr([]byte(`{"type":"error","error":{"type":"usage_limit_reached","message":"You've hit your usage limit.","resets_in_seconds":300}}`))
	if !ok {
		t.Fatal("expected usage_limit_reached terminal error to be handled")
	}
	if got := statusCodeFromTestError(t, streamErr); got != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", got, http.StatusTooManyRequests)
	}
	retryAfter := streamErr.RetryAfter()
	if retryAfter == nil {
		t.Fatal("expected retryAfter from usage_limit_reached terminal error")
	}
	if *retryAfter != 300*time.Second {
		t.Fatalf("retryAfter = %v, want %v", *retryAfter, 300*time.Second)
	}
}

func TestCodexTerminalStreamErrHandlesUsageLimitResponseFailed(t *testing.T) {
	streamErr, _, ok := codexTerminalStreamErr([]byte(`{"type":"response.failed","response":{"error":{"type":"usage_limit_reached","message":"usage limit reached","resets_in_seconds":60}}}`))
	if !ok {
		t.Fatal("expected usage_limit_reached response.failed terminal error to be handled")
	}
	if got := statusCodeFromTestError(t, streamErr); got != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", got, http.StatusTooManyRequests)
	}
	if streamErr.RetryAfter() == nil {
		t.Fatal("expected retryAfter from usage_limit_reached response.failed terminal error")
	}
}

func statusCodeFromTestError(t *testing.T, err error) int {
	t.Helper()

	statusErr, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error %T does not expose StatusCode(): %v", err, err)
	}
	return statusErr.StatusCode()
}

func assertRequestScopedTestError(t *testing.T, err error) {
	t.Helper()

	requestErr, ok := err.(interface{ IsRequestScoped() bool })
	if !ok {
		t.Fatalf("error %T does not expose IsRequestScoped(): %v", err, err)
	}
	if !requestErr.IsRequestScoped() {
		t.Fatalf("error %T is not request-scoped: %v", err, err)
	}
}

func assertNotRequestScopedTestError(t *testing.T, err error) {
	t.Helper()

	requestErr, ok := err.(interface{ IsRequestScoped() bool })
	if ok && requestErr.IsRequestScoped() {
		t.Fatalf("error %T is unexpectedly request-scoped: %v", err, err)
	}
}

type unexpectedEOFReader struct{}

func (unexpectedEOFReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
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
		Payload: []byte(`{"model":"gpt-5.4-mini","input":"Say ok"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var completed []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		payload := bytes.TrimSpace(chunk.Payload)
		if !bytes.HasPrefix(payload, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(payload[5:])
		if gjson.GetBytes(data, "type").String() == "response.completed" {
			completed = append([]byte(nil), data...)
		}
	}

	if len(completed) == 0 {
		t.Fatal("missing response.completed chunk")
	}

	gotContent := gjson.GetBytes(completed, "response.output.0.content.0.text").String()
	if gotContent != "ok" {
		t.Fatalf("response.output[0].content[0].text = %q, want %q; completed=%s", gotContent, "ok", string(completed))
	}
}

func TestCodexPropagatedRequestTimeoutKeepsCredentialEligible(t *testing.T) {
	const (
		timeoutFailedEvent = `{"type":"response.failed","sequence_number":2,"response":{"status":"failed","error":{"code":"request_timeout","message":"stream error: stream disconnected before completion: stream closed before response.completed","param":null,"type":"server_error"}}}`
		timeoutErrorBody   = `{"error":{"code":"request_timeout","message":"stream error: stream disconnected before completion: stream closed before response.completed","type":"server_error"}}`
		websocketError408  = `{"type":"error","status":408,"error":{"code":"request_timeout","message":"stream error: stream disconnected before completion: stream closed before response.completed","type":"server_error"}}`
		createdEvent       = `{"type":"response.created","response":{"id":"resp_1","model":"gpt-5.4"}}`
		doneItemEvent      = `{"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]},"output_index":0}`
		completedEvent     = `{"type":"response.completed","response":{"id":"resp_2","object":"response","status":"completed","model":"gpt-5.4","output":[],"usage":{"input_tokens":8,"output_tokens":2,"total_tokens":10}}}`
	)

	tests := []struct {
		name          string
		streamTurn1   bool
		useWebsockets bool
		http408Turn1  bool
		wsFrameTurn1  string
	}{
		{
			name:        "sse_stream_response_failed",
			streamTurn1: true,
		},
		{
			name: "nonstream_response_failed",
		},
		{
			name:         "http_408_request_timeout",
			http408Turn1: true,
		},
		{
			name:          "websocket_stream_response_failed",
			streamTurn1:   true,
			useWebsockets: true,
			wsFrameTurn1:  timeoutFailedEvent,
		},
		{
			name:          "websocket_status_408_error",
			useWebsockets: true,
			wsFrameTurn1:  websocketError408,
		},
	}

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			stopWS := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				turn := attempts.Add(1)
				if tc.useWebsockets {
					conn, errUpgrade := upgrader.Upgrade(w, r, nil)
					if errUpgrade != nil {
						t.Errorf("upgrade websocket: %v", errUpgrade)
						return
					}
					defer func() { _ = conn.Close() }()
					go func() {
						<-stopWS
						_ = conn.Close()
					}()
					_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
					if _, _, errRead := conn.ReadMessage(); errRead != nil {
						t.Errorf("read websocket request: %v", errRead)
						return
					}
					if turn == 1 {
						if tc.streamTurn1 {
							_ = conn.WriteMessage(websocket.TextMessage, []byte(createdEvent))
						}
						_ = conn.WriteMessage(websocket.TextMessage, []byte(tc.wsFrameTurn1))
					} else {
						_ = conn.WriteMessage(websocket.TextMessage, []byte(createdEvent))
						_ = conn.WriteMessage(websocket.TextMessage, []byte(doneItemEvent))
						_ = conn.WriteMessage(websocket.TextMessage, []byte(completedEvent))
					}
					for {
						if _, _, errRead := conn.ReadMessage(); errRead != nil {
							return
						}
					}
				}

				if turn == 1 && tc.http408Turn1 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusRequestTimeout)
					_, _ = w.Write([]byte(timeoutErrorBody))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if turn == 1 {
					_, _ = w.Write([]byte("data: " + createdEvent + "\n\n"))
					_, _ = w.Write([]byte("data: " + timeoutFailedEvent + "\n\n"))
					return
				}
				_, _ = w.Write([]byte("data: " + createdEvent + "\n\n"))
				_, _ = w.Write([]byte("data: " + doneItemEvent + "\n\n"))
				_, _ = w.Write([]byte("data: " + completedEvent + "\n\n"))
			}))
			defer server.Close()
			defer close(stopWS)

			manager, authID, model := newCodexTimeoutScopeTestManager(t, server.URL, tc.useWebsockets, 1)
			req := cliproxyexecutor.Request{
				Model:   model,
				Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FromString("openai-response"),
				ResponseFormat: sdktranslator.FromString("openai-response"),
				Stream:         tc.streamTurn1,
			}

			var errTurn1 error
			if tc.streamTurn1 {
				result, errStream := manager.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
				errTurn1 = errStream
				if result != nil {
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							errTurn1 = chunk.Err
						}
					}
				}
			} else {
				_, errTurn1 = manager.Execute(context.Background(), []string{"codex"}, req, opts)
			}
			if errTurn1 == nil {
				t.Fatal("expected propagated request_timeout failure on first turn, got nil")
			}
			if got := statusCodeFromManagerTestError(t, errTurn1); got != http.StatusRequestTimeout {
				t.Fatalf("first turn status = %d, want %d; err=%v", got, http.StatusRequestTimeout, errTurn1)
			}
			if !isRequestScopedManagerTestError(errTurn1) {
				t.Fatalf("first turn error %T is not request-scoped: %v", errTurn1, errTurn1)
			}
			if got := gjson.Get(errTurn1.Error(), "error.code").String(); got != "request_timeout" {
				t.Fatalf("error.code = %q, want request_timeout; err=%v", got, errTurn1)
			}
			if got := gjson.Get(errTurn1.Error(), "error.type").String(); got != "server_error" {
				t.Fatalf("error.type = %q, want server_error; err=%v", got, errTurn1)
			}
			if got := attempts.Load(); got != 1 {
				t.Fatalf("first turn upstream attempts = %d, want 1", got)
			}

			updated, ok := manager.GetByID(authID)
			if !ok || updated == nil {
				t.Fatalf("auth %s not found", authID)
			}
			if updated.Unavailable || !updated.NextRetryAfter.IsZero() {
				t.Fatalf("auth cooled after request_timeout: unavailable=%v next_retry_after=%v", updated.Unavailable, updated.NextRetryAfter)
			}
			if state := updated.ModelStates[model]; state != nil && (state.Unavailable || !state.NextRetryAfter.IsZero()) {
				t.Fatalf("model state cooled after request_timeout: %+v", state)
			}
			if registry.GetGlobalRegistry().IsModelSuspendedForClient(authID, model) {
				t.Fatalf("model %s suspended in registry after request_timeout", model)
			}

			respTurn2, errTurn2 := manager.Execute(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FromString("openai-response"),
				ResponseFormat: sdktranslator.FromString("openai-response"),
			})
			if errTurn2 != nil {
				t.Fatalf("subsequent request failed after propagated request_timeout: %v", errTurn2)
			}
			if got := attempts.Load(); got != 2 {
				t.Fatalf("total upstream attempts = %d, want 2", got)
			}
			if got := gjson.GetBytes(respTurn2.Payload, "output.0.content.0.text").String(); got != "ok" {
				t.Fatalf("subsequent response text = %q, want %q; payload=%s", got, "ok", respTurn2.Payload)
			}
		})
	}
}

func TestCodexPropagatedRequestTimeoutPreservesCredentialFailures(t *testing.T) {
	tests := []struct {
		name          string
		useWebsockets bool
		event         string
		wantStatus    int
	}{
		{
			name:       "server_error",
			event:      `{"type":"response.failed","response":{"status":"failed","error":{"code":"internal_server_error","type":"server_error","message":"Internal error."}}}`,
			wantStatus: http.StatusBadGateway,
		},
		{
			name:       "authentication_error_with_request_timeout_code",
			event:      `{"type":"error","error":{"code":"request_timeout","type":"authentication_error","message":"Invalid token."}}`,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "usage_limit_reached",
			event:      `{"type":"response.failed","response":{"status":"failed","error":{"type":"usage_limit_reached","message":"You've hit your usage limit.","resets_in_seconds":300}}}`,
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name:          "websocket_server_error",
			useWebsockets: true,
			event:         `{"type":"error","status":502,"error":{"code":"internal_server_error","type":"server_error","message":"Internal error."}}`,
			wantStatus:    http.StatusBadGateway,
		},
	}

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			stopWS := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				if tc.useWebsockets {
					conn, errUpgrade := upgrader.Upgrade(w, r, nil)
					if errUpgrade != nil {
						t.Errorf("upgrade websocket: %v", errUpgrade)
						return
					}
					defer func() { _ = conn.Close() }()
					go func() {
						<-stopWS
						_ = conn.Close()
					}()
					_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
					if _, _, errRead := conn.ReadMessage(); errRead != nil {
						return
					}
					_ = conn.WriteMessage(websocket.TextMessage, []byte(tc.event))
					for {
						if _, _, errRead := conn.ReadMessage(); errRead != nil {
							return
						}
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.4"}}` + "\n\n"))
				_, _ = w.Write([]byte("data: " + tc.event + "\n\n"))
			}))
			defer server.Close()
			defer close(stopWS)

			manager, authID, model := newCodexTimeoutScopeTestManager(t, server.URL, tc.useWebsockets, 0)
			req := cliproxyexecutor.Request{
				Model:   model,
				Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FromString("openai-response"),
				ResponseFormat: sdktranslator.FromString("openai-response"),
			}

			_, errTurn1 := manager.Execute(context.Background(), []string{"codex"}, req, opts)
			if errTurn1 == nil {
				t.Fatal("expected failure on first turn, got nil")
			}
			if got := statusCodeFromManagerTestError(t, errTurn1); got != tc.wantStatus {
				t.Fatalf("first turn status = %d, want %d; err=%v", got, tc.wantStatus, errTurn1)
			}
			if isRequestScopedManagerTestError(errTurn1) {
				t.Fatalf("credential/server failure unexpectedly became request-scoped: %v", errTurn1)
			}

			updated, ok := manager.GetByID(authID)
			if !ok || updated == nil {
				t.Fatalf("auth %s not found", authID)
			}
			state := updated.ModelStates[model]
			if !updated.Unavailable && (state == nil || !state.Unavailable) {
				t.Fatalf("expected credential or model cooldown after %s: auth=%+v modelState=%+v", tc.name, updated, state)
			}

			_, errTurn2 := manager.Execute(context.Background(), []string{"codex"}, req, opts)
			if errTurn2 == nil {
				t.Fatal("expected second turn to fail while sole credential is cooling")
			}
			if got := attempts.Load(); got != 1 {
				t.Fatalf("upstream attempts = %d, want 1 while credential is cooled", got)
			}
		})
	}
}

func newCodexTimeoutScopeTestManager(t *testing.T, baseURL string, useWebsockets bool, retry int) (*cliproxyauth.Manager, string, string) {
	t.Helper()
	const model = "gpt-5.4"
	cfg := &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}}
	manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
	manager.SetRetryConfig(retry, 0, 0)
	if useWebsockets {
		manager.RegisterExecutor(NewCodexWebsocketsExecutor(cfg))
	} else {
		manager.RegisterExecutor(NewCodexExecutor(cfg))
	}

	authID := "codex-timeout-scope-" + uuid.NewString()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })
	if _, err := manager.Register(context.Background(), &cliproxyauth.Auth{
		ID:       authID,
		Provider: "codex",
		Status:   cliproxyauth.StatusActive,
		Attributes: map[string]string{
			"base_url": baseURL,
			"api_key":  "sk-test",
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	return manager, authID, model
}

func statusCodeFromManagerTestError(t *testing.T, err error) int {
	t.Helper()
	var statusCoder interface{ StatusCode() int }
	if !errors.As(err, &statusCoder) || statusCoder == nil {
		t.Fatalf("error %T does not expose StatusCode(): %v", err, err)
	}
	return statusCoder.StatusCode()
}

func isRequestScopedManagerTestError(err error) bool {
	var requestErr cliproxyexecutor.RequestScopedError
	return errors.As(err, &requestErr) && requestErr != nil && requestErr.IsRequestScoped()
}
