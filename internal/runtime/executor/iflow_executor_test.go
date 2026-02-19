package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestIFlowExecutorParseSuffix(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		wantBase  string
		wantLevel string
	}{
		{"no suffix", "glm-4", "glm-4", ""},
		{"glm with suffix", "glm-4.1-flash(high)", "glm-4.1-flash", "high"},
		{"minimax no suffix", "minimax-m2", "minimax-m2", ""},
		{"minimax with suffix", "minimax-m2.1(medium)", "minimax-m2.1", "medium"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := thinking.ParseSuffix(tt.model)
			if result.ModelName != tt.wantBase {
				t.Errorf("ParseSuffix(%q).ModelName = %q, want %q", tt.model, result.ModelName, tt.wantBase)
			}
		})
	}
}

func TestPreserveReasoningContentInMessages(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  []byte // nil means output should equal input
	}{
		{
			"non-glm model passthrough",
			[]byte(`{"model":"gpt-4","messages":[]}`),
			nil,
		},
		{
			"glm model with empty messages",
			[]byte(`{"model":"glm-4","messages":[]}`),
			nil,
		},
		{
			"glm model preserves existing reasoning_content",
			[]byte(`{"model":"glm-4","messages":[{"role":"assistant","content":"hi","reasoning_content":"thinking..."}]}`),
			nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := preserveReasoningContentInMessages(tt.input)
			want := tt.want
			if want == nil {
				want = tt.input
			}
			if string(got) != string(want) {
				t.Errorf("preserveReasoningContentInMessages() = %s, want %s", got, want)
			}
		})
	}
}

func TestIFlowExecutorExecute_RetryWithoutSignatureOn406(t *testing.T) {
	var (
		mu                         sync.Mutex
		attempts                   int
		signatures                 []string
		sessions                   []string
		conversations              []string
		conversationHeaderPresence []bool
		accepts                    []string
		timestamps                 []string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		signatures = append(signatures, r.Header.Get("x-iflow-signature"))
		sessions = append(sessions, r.Header.Get("session-id"))
		conversations = append(conversations, r.Header.Get("conversation-id"))
		_, hasConversation := r.Header["Conversation-Id"]
		conversationHeaderPresence = append(conversationHeaderPresence, hasConversation)
		accepts = append(accepts, r.Header.Get("Accept"))
		timestamps = append(timestamps, r.Header.Get("x-iflow-timestamp"))
		currentAttempt := attempts
		mu.Unlock()

		if r.URL.Path != "/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"unexpected path"}}`)
			return
		}

		if currentAttempt == 1 {
			w.WriteHeader(http.StatusNotAcceptable)
			_, _ = io.WriteString(w, `{"error":{"message":"status 406","type":"invalid_request_error"}}`)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"glm-5","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()

	executor := NewIFlowExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "iflow-test-auth",
		Provider: "iflow",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "glm-5",
		Payload: []byte(`{"model":"glm-5","messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Metadata: map[string]any{
			"idempotency_key": "sess-test-123",
		},
	}

	resp, err := executor.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() unexpected error: %v", err)
	}
	if !strings.Contains(string(resp.Payload), `"content":"ok"`) {
		t.Fatalf("Execute() response missing expected content: %s", string(resp.Payload))
	}

	mu.Lock()
	defer mu.Unlock()
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if signatures[0] == "" {
		t.Fatalf("first attempt should include x-iflow-signature")
	}
	if sessions[0] == "" {
		t.Fatalf("first attempt should include session-id")
	}
	if sessions[0] != "sess-test-123" {
		t.Fatalf("first attempt session-id = %q, want %q", sessions[0], "sess-test-123")
	}
	if timestamps[0] == "" {
		t.Fatalf("first attempt should include x-iflow-timestamp")
	}
	if signatures[1] != "" {
		t.Fatalf("second attempt should not include x-iflow-signature, got %q", signatures[1])
	}
	if sessions[1] != sessions[0] {
		t.Fatalf("second attempt should reuse session-id, got %q vs %q", sessions[1], sessions[0])
	}
	if timestamps[1] != "" {
		t.Fatalf("second attempt should not include x-iflow-timestamp, got %q", timestamps[1])
	}
	if conversations[0] != "" || conversations[1] != "" {
		t.Fatalf("conversation-id should be empty in this test, got first=%q second=%q", conversations[0], conversations[1])
	}
	if !conversationHeaderPresence[0] || !conversationHeaderPresence[1] {
		t.Fatalf("conversation-id header should be present on both attempts, got first=%v second=%v", conversationHeaderPresence[0], conversationHeaderPresence[1])
	}
	if accepts[0] != "" || accepts[1] != "" {
		t.Fatalf("accept header should be omitted on both attempts, got first=%q second=%q", accepts[0], accepts[1])
	}
}

func TestIFlowExecutorExecute_LogsDiagnosticOn403(t *testing.T) {
	hook := logtest.NewLocal(log.StandardLogger())
	defer hook.Reset()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"unexpected path"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "iflow-req-403")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"message":"status 403","type":"invalid_request_error"}}`)
	}))
	defer server.Close()

	executor := NewIFlowExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "iflow",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "glm-5",
		Payload: []byte(`{"model":"glm-5","messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Metadata: map[string]any{
			"idempotency_key": "sess-test-403",
		},
	}

	_, err := executor.Execute(context.Background(), auth, req, opts)
	if err == nil {
		t.Fatal("Execute() expected error, got nil")
	}
	var sErr statusErr
	if !errors.As(err, &sErr) || sErr.code != http.StatusForbidden {
		t.Fatalf("Execute() error = %v, want statusErr code=%d", err, http.StatusForbidden)
	}

	var diagnostic *log.Entry
	for _, entry := range hook.AllEntries() {
		if entry.Message == "iflow executor: upstream rejected request" {
			diagnostic = entry
			break
		}
	}
	if diagnostic == nil {
		t.Fatal("expected diagnostic warning log for 403, got none")
	}

	if got, ok := diagnostic.Data["status"].(int); !ok || got != http.StatusForbidden {
		t.Fatalf("diagnostic status = %#v, want %d", diagnostic.Data["status"], http.StatusForbidden)
	}
	if got, ok := diagnostic.Data["with_signature"].(bool); !ok || !got {
		t.Fatalf("diagnostic with_signature = %#v, want true", diagnostic.Data["with_signature"])
	}
	if got, ok := diagnostic.Data["request_has_signature"].(bool); !ok || !got {
		t.Fatalf("diagnostic request_has_signature = %#v, want true", diagnostic.Data["request_has_signature"])
	}
	if got, ok := diagnostic.Data["request_has_timestamp"].(bool); !ok || !got {
		t.Fatalf("diagnostic request_has_timestamp = %#v, want true", diagnostic.Data["request_has_timestamp"])
	}
	if got, ok := diagnostic.Data["retrying_without_signature"].(bool); !ok || got {
		t.Fatalf("diagnostic retrying_without_signature = %#v, want false", diagnostic.Data["retrying_without_signature"])
	}

	maskedSession, ok := diagnostic.Data["request_session_id"].(string)
	if !ok || maskedSession == "" {
		t.Fatalf("diagnostic request_session_id = %#v, want masked non-empty string", diagnostic.Data["request_session_id"])
	}
	if maskedSession == "sess-test-403" {
		t.Fatalf("diagnostic request_session_id should be masked, got %q", maskedSession)
	}
	if got := diagnostic.Data["response_x_request_id"]; got != "iflow-req-403" {
		t.Fatalf("diagnostic response_x_request_id = %#v, want %q", got, "iflow-req-403")
	}
	if _, exists := diagnostic.Data["request_signature"]; exists {
		t.Fatalf("diagnostic should not include raw request_signature field")
	}
}

func TestIFlowExecutorExecute_NoDiagnosticOn500(t *testing.T) {
	hook := logtest.NewLocal(log.StandardLogger())
	defer hook.Reset()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"unexpected path"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"status 500","type":"server_error"}}`)
	}))
	defer server.Close()

	executor := NewIFlowExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "iflow",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "glm-5",
		Payload: []byte(`{"model":"glm-5","messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	}

	_, err := executor.Execute(context.Background(), auth, req, opts)
	if err == nil {
		t.Fatal("Execute() expected error, got nil")
	}

	for _, entry := range hook.AllEntries() {
		if entry.Message == "iflow executor: upstream rejected request" {
			t.Fatalf("unexpected diagnostic warning log on 500: %#v", entry.Data)
		}
	}
}

func TestIFlowExecutorExecuteStream_EmitsMessageStopWhenUpstreamOmitsDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"unexpected path"}}`)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"glm-5","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	executor := NewIFlowExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "iflow",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model: "glm-5",
		Payload: []byte(`{
			"model":"glm-5",
			"stream":true,
			"max_tokens":64,
			"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
		}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		OriginalRequest: req.Payload,
	}

	stream, err := executor.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() unexpected error: %v", err)
	}

	output := collectIFlowStreamPayload(t, stream)
	if !strings.Contains(output, "event: message_stop") {
		t.Fatalf("expected message_stop in stream output, got: %s", output)
	}
}

func TestIFlowExecutorExecuteStream_FallbacksWhenUpstreamReturnsJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"unexpected path"}}`)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-2","object":"chat.completion","created":2,"model":"glm-5","choices":[{"index":0,"message":{"role":"assistant","content":"fallback-ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)
	}))
	defer server.Close()

	executor := NewIFlowExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "iflow",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model: "glm-5",
		Payload: []byte(`{
			"model":"glm-5",
			"stream":true,
			"max_tokens":64,
			"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
		}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		OriginalRequest: req.Payload,
	}

	stream, err := executor.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() unexpected error: %v", err)
	}

	output := collectIFlowStreamPayload(t, stream)
	if !strings.Contains(output, "fallback-ok") {
		t.Fatalf("expected fallback content in stream output, got: %s", output)
	}
	if !strings.Contains(output, "event: message_stop") {
		t.Fatalf("expected message_stop in fallback stream output, got: %s", output)
	}
}

func TestIFlowExecutorExecuteStream_ReturnsRateLimitErrorWhenUpstreamReturnsBusinessJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"unexpected path"}}`)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"449","msg":"You exceeded your current rate limit","body":null}`)
	}))
	defer server.Close()

	executor := NewIFlowExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "iflow",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model: "glm-5",
		Payload: []byte(`{
			"model":"glm-5",
			"stream":true,
			"max_tokens":64,
			"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
		}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		OriginalRequest: req.Payload,
	}

	stream, err := executor.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() unexpected error: %v", err)
	}

	var gotErr error
	for chunk := range stream {
		if len(chunk.Payload) > 0 {
			t.Fatalf("expected no payload when business error occurs, got: %s", string(chunk.Payload))
		}
		if chunk.Err != nil {
			gotErr = chunk.Err
		}
	}
	if gotErr == nil {
		t.Fatal("expected stream error, got nil")
	}

	var sErr statusErr
	if !errors.As(gotErr, &sErr) {
		t.Fatalf("expected statusErr, got %T: %v", gotErr, gotErr)
	}
	if sErr.code != http.StatusTooManyRequests {
		t.Fatalf("statusErr.code = %d, want %d", sErr.code, http.StatusTooManyRequests)
	}
}

func TestIFlowExecutorExecute_ReturnsRateLimitErrorWhenUpstreamReturnsBusinessJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"unexpected path"}}`)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"449","msg":"You exceeded your current rate limit","body":null}`)
	}))
	defer server.Close()

	executor := NewIFlowExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "iflow",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "glm-5",
		Payload: []byte(`{"model":"glm-5","messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	}

	_, err := executor.Execute(context.Background(), auth, req, opts)
	if err == nil {
		t.Fatal("Execute() expected error, got nil")
	}

	var sErr statusErr
	if !errors.As(err, &sErr) {
		t.Fatalf("expected statusErr, got %T: %v", err, err)
	}
	if sErr.code != http.StatusTooManyRequests {
		t.Fatalf("statusErr.code = %d, want %d", sErr.code, http.StatusTooManyRequests)
	}
}

func TestIFlowExecutorExecuteStream_ReturnsErrorWhenUpstreamSSENetworkErrorWithoutContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"unexpected path"}}`)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream;charset=UTF-8")
		_, _ = io.WriteString(w, "data:"+`{"id":"chatcmpl-neterr","created":1,"model":"glm-5","choices":[{"index":0,"finish_reason":"network_error","delta":{"role":"assistant","content":""}}]}`+"\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	executor := NewIFlowExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "iflow",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model: "glm-5",
		Payload: []byte(`{
			"model":"glm-5",
			"stream":true,
			"max_tokens":64,
			"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]
		}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("claude"),
		OriginalRequest: req.Payload,
	}

	stream, err := executor.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() unexpected error: %v", err)
	}

	var gotErr error
	for chunk := range stream {
		if len(chunk.Payload) > 0 {
			t.Fatalf("expected no payload when network_error without content occurs, got: %s", string(chunk.Payload))
		}
		if chunk.Err != nil {
			gotErr = chunk.Err
		}
	}
	if gotErr == nil {
		t.Fatal("expected stream error, got nil")
	}

	var sErr statusErr
	if !errors.As(gotErr, &sErr) {
		t.Fatalf("expected statusErr, got %T: %v", gotErr, gotErr)
	}
	if sErr.code != http.StatusBadGateway {
		t.Fatalf("statusErr.code = %d, want %d", sErr.code, http.StatusBadGateway)
	}
}

func collectIFlowStreamPayload(t *testing.T, stream <-chan cliproxyexecutor.StreamChunk) string {
	t.Helper()

	var builder strings.Builder
	for chunk := range stream {
		if chunk.Err != nil {
			t.Fatalf("stream chunk returned error: %v", chunk.Err)
		}
		builder.Write(chunk.Payload)
	}
	return builder.String()
}
