package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func TestParseCodexRetryAfter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	t.Run("resets_in_seconds", func(t *testing.T) {
		body := []byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":123}}`)
		retryAfter := parseCodexRetryAfter(http.StatusTooManyRequests, body, now)
		if retryAfter == nil {
			t.Fatalf("expected retryAfter, got nil")
		}
		if *retryAfter != 123*time.Second {
			t.Fatalf("retryAfter = %v, want %v", *retryAfter, 123*time.Second)
		}
	})

	t.Run("prefers resets_at", func(t *testing.T) {
		resetAt := now.Add(5 * time.Minute).Unix()
		body := []byte(`{"error":{"type":"usage_limit_reached","resets_at":` + itoa(resetAt) + `,"resets_in_seconds":1}}`)
		retryAfter := parseCodexRetryAfter(http.StatusTooManyRequests, body, now)
		if retryAfter == nil {
			t.Fatalf("expected retryAfter, got nil")
		}
		if *retryAfter != 5*time.Minute {
			t.Fatalf("retryAfter = %v, want %v", *retryAfter, 5*time.Minute)
		}
	})

	t.Run("fallback when resets_at is past", func(t *testing.T) {
		resetAt := now.Add(-1 * time.Minute).Unix()
		body := []byte(`{"error":{"type":"usage_limit_reached","resets_at":` + itoa(resetAt) + `,"resets_in_seconds":77}}`)
		retryAfter := parseCodexRetryAfter(http.StatusTooManyRequests, body, now)
		if retryAfter == nil {
			t.Fatalf("expected retryAfter, got nil")
		}
		if *retryAfter != 77*time.Second {
			t.Fatalf("retryAfter = %v, want %v", *retryAfter, 77*time.Second)
		}
	})

	t.Run("non-429 status code", func(t *testing.T) {
		body := []byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":30}}`)
		if got := parseCodexRetryAfter(http.StatusBadRequest, body, now); got != nil {
			t.Fatalf("expected nil for non-429, got %v", *got)
		}
	})

	t.Run("non usage_limit_reached error type", func(t *testing.T) {
		body := []byte(`{"error":{"type":"server_error","resets_in_seconds":30}}`)
		if got := parseCodexRetryAfter(http.StatusTooManyRequests, body, now); got != nil {
			t.Fatalf("expected nil for non-usage_limit_reached, got %v", *got)
		}
	})
}

func TestNewCodexStatusErrTreatsCapacityAsRetryableRateLimit(t *testing.T) {
	body := []byte(`{"error":{"message":"Selected model is at capacity. Please try a different model."}}`)

	err := newCodexStatusErr(http.StatusBadRequest, body)

	if got := err.StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", got, http.StatusTooManyRequests)
	}
	if err.RetryAfter() == nil {
		t.Fatalf("expected retryAfter for capacity fallback")
	}
	if got := *err.RetryAfter(); got != codexModelCapacityRetryAfter {
		t.Fatalf("retryAfter = %v, want %v", got, codexModelCapacityRetryAfter)
	}
}

func TestCodexStreamStatusErrTreatsCapacityAsRetryableRateLimit(t *testing.T) {
	event := []byte(`{"type":"response.failed","response":{"error":{"message":"Selected model is at capacity. Please try a different model."}}}`)

	err, ok := codexStreamStatusErr(event)

	if !ok {
		t.Fatalf("expected stream capacity error")
	}
	if got := err.StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", got, http.StatusTooManyRequests)
	}
	if err.RetryAfter() == nil {
		t.Fatalf("expected retryAfter for capacity fallback")
	}
	if got := *err.RetryAfter(); got != codexModelCapacityRetryAfter {
		t.Fatalf("retryAfter = %v, want %v", got, codexModelCapacityRetryAfter)
	}
}

func TestCodexStreamStatusErrIgnoresCapacityTextInOutput(t *testing.T) {
	event := []byte(`{"type":"response.output_text.delta","delta":"Selected model is at capacity. Please try a different model."}`)

	if err, ok := codexStreamStatusErr(event); ok {
		t.Fatalf("expected normal output text to pass through, got %v", err)
	}
}

func TestCodexExecutorExecuteStream_ReturnsErrorForStreamCapacityEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"Selected model is at capacity. Please try a different model.\"}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"Say ok"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	chunk, ok := <-result.Chunks
	if !ok {
		t.Fatalf("expected error chunk")
	}
	if chunk.Err == nil {
		t.Fatalf("expected error chunk, got payload %q", string(chunk.Payload))
	}
	statusErr, ok := chunk.Err.(interface {
		StatusCode() int
		RetryAfter() *time.Duration
	})
	if !ok {
		t.Fatalf("stream error does not expose status/retryAfter: %T", chunk.Err)
	}
	if got := statusErr.StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", got, http.StatusTooManyRequests)
	}
	if statusErr.RetryAfter() == nil {
		t.Fatalf("expected retryAfter")
	}
}

func TestCodexExecutorExecuteStream_BuffersTerminalEventBeforeCapacityError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.failed\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"Selected model is at capacity. Please try a different model.\"}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"Say ok"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	chunk, ok := <-result.Chunks
	if !ok {
		t.Fatalf("expected capacity error chunk")
	}
	if len(chunk.Payload) > 0 {
		t.Fatalf("expected no payload before capacity error, got %q", string(chunk.Payload))
	}
	if chunk.Err == nil {
		t.Fatalf("expected capacity error chunk")
	}
	statusErr, ok := chunk.Err.(interface {
		StatusCode() int
		RetryAfter() *time.Duration
	})
	if !ok {
		t.Fatalf("stream error does not expose status/retryAfter: %T", chunk.Err)
	}
	if got := statusErr.StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", got, http.StatusTooManyRequests)
	}
	if statusErr.RetryAfter() == nil {
		t.Fatalf("expected retryAfter")
	}
}

func TestCodexExecutorExecuteStream_BuffersControlLinesBeforeCapacityError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(": keep-alive\n"))
		_, _ = w.Write([]byte("id: resp_123\n"))
		_, _ = w.Write([]byte("retry: 1000\n\n"))
		_, _ = w.Write([]byte("event: response.failed\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"Selected model is at capacity. Please try a different model.\"}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"Say ok"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	chunk, ok := <-result.Chunks
	if !ok {
		t.Fatalf("expected capacity error chunk")
	}
	if len(chunk.Payload) > 0 {
		t.Fatalf("expected no control-line payload before capacity error, got %q", string(chunk.Payload))
	}
	if chunk.Err == nil {
		t.Fatalf("expected capacity error chunk")
	}
	if got := statusCodeFromCodexTestError(chunk.Err); got != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", got, http.StatusTooManyRequests)
	}
}

func TestCodexExecutorExecuteStream_FlushesLongBootstrapBufferBeforeContent(t *testing.T) {
	bootstrapFlushed := make(chan struct{})
	allowContent := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 4; i++ {
			_, _ = w.Write([]byte("event: response.in_progress\n"))
			_, _ = w.Write([]byte("data: {\"type\":\"response.in_progress\"}\n\n"))
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(bootstrapFlushed)
		<-allowContent
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_123\",\"output\":[]}}\n\n"))
	}))
	defer server.Close()
	defer close(allowContent)

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"Say ok"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	<-bootstrapFlushed
	select {
	case chunk := <-result.Chunks:
		if chunk.Err != nil {
			t.Fatalf("expected buffered bootstrap payload, got error %v", chunk.Err)
		}
		if !bytes.Contains(chunk.Payload, []byte("response.in_progress")) {
			t.Fatalf("expected bootstrap payload before content, got %q", string(chunk.Payload))
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for bounded bootstrap buffer to flush")
	}
}

func TestCodexExecutorExecuteStream_DoesNotBufferContentEventLine(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"Say ok"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	chunk, ok := <-result.Chunks
	if !ok {
		t.Fatalf("expected content event chunk")
	}
	if chunk.Err != nil {
		t.Fatalf("expected payload chunk, got error %v", chunk.Err)
	}
	if len(chunk.Payload) == 0 {
		t.Fatalf("expected non-empty payload")
	}
}

func TestCodexExecutorExecuteStream_PreservesBufferedBootstrapDelimiter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_123\",\"created_at\":1700000000,\"model\":\"gpt-5.5\"}}\n\n"))
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_123\",\"output\":[]}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"Say ok"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var payloads [][]byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("expected payload chunk, got error %v", chunk.Err)
		}
		payloads = append(payloads, chunk.Payload)
	}
	joined := string(bytes.Join(payloads, nil))
	if !strings.Contains(joined, "gpt-5.5\"}}\n\nevent: response.output_text.delta") {
		t.Fatalf("buffered bootstrap delimiter missing from payloads: %q", joined)
	}
}

func TestCodexExecutorExecuteStream_ReturnsErrorForNonCapacityFailureEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.failed\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"upstream failed\"}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"Say ok"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	chunk, ok := <-result.Chunks
	if !ok {
		t.Fatalf("expected failure error chunk")
	}
	if len(chunk.Payload) > 0 {
		t.Fatalf("expected no payload before failure error, got %q", string(chunk.Payload))
	}
	if chunk.Err == nil {
		t.Fatalf("expected failure error chunk")
	}
	if got := statusCodeFromCodexTestError(chunk.Err); got != http.StatusRequestTimeout {
		t.Fatalf("status code = %d, want %d", got, http.StatusRequestTimeout)
	}
}

func TestCodexExecutorExecuteStream_PreservesInvalidRequestFailureStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.failed\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"context length exceeded\",\"type\":\"invalid_request_error\",\"code\":\"context_length_exceeded\"}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"Say ok"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	chunk, ok := <-result.Chunks
	if !ok {
		t.Fatalf("expected failure error chunk")
	}
	if len(chunk.Payload) > 0 {
		t.Fatalf("expected no payload before failure error, got %q", string(chunk.Payload))
	}
	if chunk.Err == nil {
		t.Fatalf("expected failure error chunk")
	}
	if got := statusCodeFromCodexTestError(chunk.Err); got != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", got, http.StatusBadRequest)
	}
	if !strings.Contains(chunk.Err.Error(), "context_too_large") {
		t.Fatalf("expected classified context error, got %v", chunk.Err)
	}
}

func TestCodexExecutorExecuteStream_PreservesAuthenticationFailureStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.failed\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"invalid or expired token\",\"type\":\"authentication_error\",\"code\":\"invalid_api_key\"}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"Say ok"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	chunk, ok := <-result.Chunks
	if !ok {
		t.Fatalf("expected auth error chunk")
	}
	if len(chunk.Payload) > 0 {
		t.Fatalf("expected no payload before auth error, got %q", string(chunk.Payload))
	}
	if chunk.Err == nil {
		t.Fatalf("expected auth error chunk")
	}
	if got := statusCodeFromCodexTestError(chunk.Err); got != http.StatusUnauthorized {
		t.Fatalf("status code = %d, want %d", got, http.StatusUnauthorized)
	}
	if !strings.Contains(chunk.Err.Error(), "auth_unavailable") {
		t.Fatalf("expected classified auth error, got %v", chunk.Err)
	}
}

func TestCodexExecutorExecuteStream_ReturnsMissingCompletedOnBootstrapOnlyEOF(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_123\",\"created_at\":1700000000,\"model\":\"gpt-5.5\"}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"Say ok"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var gotPayload bool
	var gotErr error
	for chunk := range result.Chunks {
		if len(chunk.Payload) > 0 {
			gotPayload = true
		}
		if chunk.Err != nil {
			gotErr = chunk.Err
		}
	}
	if gotPayload {
		t.Fatalf("expected bootstrap payload to be buffered and dropped before missing-completed error")
	}
	if gotErr == nil {
		t.Fatalf("expected missing completed error")
	}
	if got := statusCodeFromCodexTestError(gotErr); got != http.StatusRequestTimeout {
		t.Fatalf("status code = %d, want %d", got, http.StatusRequestTimeout)
	}
}

func TestCodexExecutorExecuteStream_DropsBootstrapEventLineOnEOF(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"Say ok"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var gotPayload []byte
	var gotErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			gotErr = chunk.Err
			continue
		}
		gotPayload = append(gotPayload, chunk.Payload...)
	}
	if len(gotPayload) > 0 {
		t.Fatalf("expected no payload before missing-completed error, got %q", string(gotPayload))
	}
	if gotErr == nil {
		t.Fatalf("expected missing completed error")
	}
	if got := statusCodeFromCodexTestError(gotErr); got != http.StatusRequestTimeout {
		t.Fatalf("status code = %d, want %d", got, http.StatusRequestTimeout)
	}
}

func TestCodexExecutorExecuteStream_DoesNotAppendMissingCompletedAfterIncomplete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.incomplete\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_123\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"Say ok"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var gotPayload bool
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		if len(chunk.Payload) > 0 {
			gotPayload = true
		}
	}
	if !gotPayload {
		t.Fatalf("expected incomplete payload to be forwarded")
	}
}

func TestCodexExecutorExecute_TreatsResponseFailedCapacityAsRetryableRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_123\",\"created_at\":1700000000,\"model\":\"gpt-5.5\"}}\n\n"))
		_, _ = w.Write([]byte("event: response.failed\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"Selected model is at capacity. Please try a different model.\"}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"Say ok"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
	})
	if err == nil {
		t.Fatal("expected execute error")
	}

	statusErr, ok := err.(interface {
		StatusCode() int
		RetryAfter() *time.Duration
	})
	if !ok {
		t.Fatalf("execute error does not expose status/retryAfter: %T", err)
	}
	if got := statusErr.StatusCode(); got != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", got, http.StatusTooManyRequests)
	}
	if statusErr.RetryAfter() == nil {
		t.Fatalf("expected retryAfter")
	}
	if got := *statusErr.RetryAfter(); got != codexModelCapacityRetryAfter {
		t.Fatalf("retryAfter = %v, want %v", got, codexModelCapacityRetryAfter)
	}
}

func TestCodexExecutorExecute_DoesNotClassifyGenericResponseFailedAsBadRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.failed\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"upstream failed\"}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.5",
		Payload: []byte(`{"model":"gpt-5.5","input":"Say ok"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
	})
	if err == nil {
		t.Fatal("expected execute error")
	}
	if got := statusCodeFromCodexTestError(err); got == http.StatusBadRequest {
		t.Fatalf("generic streamed failure status = %d, must not be forced to bad request; err=%v", got, err)
	}
	if got := statusCodeFromCodexTestError(err); got != http.StatusRequestTimeout {
		t.Fatalf("generic streamed failure status = %d, want %d", got, http.StatusRequestTimeout)
	}
}

func TestNewCodexStatusErrClassifiesKnownCodexFailures(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       []byte
		wantStatus int
		wantType   string
		wantCode   string
	}{
		{
			name:       "context length status",
			statusCode: http.StatusRequestEntityTooLarge,
			body:       []byte(`{"error":{"message":"context length exceeded","type":"invalid_request_error","code":"context_length_exceeded"}}`),
			wantStatus: http.StatusRequestEntityTooLarge,
			wantType:   "invalid_request_error",
			wantCode:   "context_too_large",
		},
		{
			name:       "thinking signature",
			statusCode: http.StatusBadRequest,
			body:       []byte(`{"error":{"message":"Invalid signature in thinking block","type":"invalid_request_error","code":"invalid_request_error"}}`),
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_request_error",
			wantCode:   "thinking_signature_invalid",
		},
		{
			name:       "previous response missing",
			statusCode: http.StatusBadRequest,
			body:       []byte(`{"error":{"message":"No response found for previous_response_id resp_123","type":"invalid_request_error","code":"previous_response_not_found"}}`),
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_request_error",
			wantCode:   "previous_response_not_found",
		},
		{
			name:       "auth unavailable",
			statusCode: http.StatusUnauthorized,
			body:       []byte(`{"error":{"message":"invalid or expired token","type":"authentication_error","code":"invalid_api_key"}}`),
			wantStatus: http.StatusUnauthorized,
			wantType:   "authentication_error",
			wantCode:   "auth_unavailable",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := newCodexStatusErr(tc.statusCode, tc.body)

			if got := err.StatusCode(); got != tc.wantStatus {
				t.Fatalf("status code = %d, want %d", got, tc.wantStatus)
			}
			assertCodexErrorCode(t, err.Error(), tc.wantType, tc.wantCode)
		})
	}
}

func TestNewCodexStatusErrPreservesUnclassifiedErrors(t *testing.T) {
	body := []byte(`{"error":{"message":"documentation mentions too many tokens, but this is a billing configuration failure","type":"server_error","code":"billing_config_error"}}`)

	err := newCodexStatusErr(http.StatusBadGateway, body)

	if got := err.StatusCode(); got != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d", got, http.StatusBadGateway)
	}
	if got := err.Error(); got != string(body) {
		t.Fatalf("error body = %s, want original %s", got, string(body))
	}
}

func TestNewCodexMissingCompletedErr(t *testing.T) {
	err := newCodexMissingCompletedErr()
	if got := err.StatusCode(); got != http.StatusRequestTimeout {
		t.Fatalf("status code = %d, want %d", got, http.StatusRequestTimeout)
	}
	if got := err.Error(); got != codexMissingCompletedMessage {
		t.Fatalf("error = %s, want %s", got, codexMissingCompletedMessage)
	}
}

func assertCodexErrorCode(t *testing.T, raw string, wantType string, wantCode string) {
	t.Helper()

	var payload struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("error body is not valid JSON: %v; body=%s", err, raw)
	}
	if payload.Error.Type != wantType {
		t.Fatalf("error.type = %q, want %q; body=%s", payload.Error.Type, wantType, raw)
	}
	if payload.Error.Code != wantCode {
		t.Fatalf("error.code = %q, want %q; body=%s", payload.Error.Code, wantCode, raw)
	}
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}

func statusCodeFromCodexTestError(err error) int {
	type statusCoder interface {
		StatusCode() int
	}
	if se, ok := err.(statusCoder); ok && se != nil {
		return se.StatusCode()
	}
	return 0
}
