package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// flexRetryServer is a configurable httptest server for the flex-503 retry
// tests. The handler receives the raw upstream request body and returns the
// status code, content type, and response body the upstream should produce.
// Each request body is captured so the test can assert ordering and content.
type flexRetryServer struct {
	*httptest.Server
	mu      sync.Mutex
	bodies  [][]byte
	handler func(body []byte) (status int, contentType string, respBody []byte)
}

func newFlexRetryServer(t *testing.T, handler func(body []byte) (int, string, []byte)) *flexRetryServer {
	t.Helper()
	s := &flexRetryServer{handler: handler}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		s.mu.Lock()
		s.bodies = append(s.bodies, append([]byte(nil), body...))
		s.mu.Unlock()
		status, contentType, respBody := s.handler(body)
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write(respBody)
		if flusher, ok := w.(http.Flusher); ok && status >= 200 && status < 300 &&
			strings.HasPrefix(contentType, "text/event-stream") {
			flusher.Flush()
		}
	}))
	return s
}

func (s *flexRetryServer) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.bodies)
}

func (s *flexRetryServer) bodyAt(i int) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.bodies) {
		return nil
	}
	return append([]byte(nil), s.bodies[i]...)
}

func (s *flexRetryServer) close() {
	s.Server.Close()
}

func newFlexRetryAuth(serverURL, tier string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider: "neuralwatt",
		Attributes: map[string]string{
			"base_url":     serverURL + "/v1",
			"api_key":      "test",
			"compat_name":  "compat",
			"provider_key": "compat",
			"service_tier": tier,
		},
	}
}

func newFlexRetryExecutor() *NeuralwattExecutor {
	return NewNeuralwattExecutor(&config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat"}},
	})
}

// flexRetrySuccessBody is a minimal valid OpenAI chat-completion JSON the
// upstream translator can decode into a non-stream response.
const flexRetrySuccessBody = `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

// flexRetryStreamBody is a minimal valid SSE chat-completion stream with a
// content delta and the OpenAI [DONE] terminator.
const flexRetryStreamBody = "data: {\"id\":\"chatcmpl_1\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"

// neuralwattRequestPayload is the base chat-completion body the tests stamp
// service_tier onto via applyNeuralwattServiceTier.
const neuralwattRequestPayload = `{"model":"neuralwatt-model","messages":[{"role":"user","content":"hi"}]}`

// TestNeuralwattFlexRetryExecute pins the contract: a flex credential whose
// first attempt returns 503 must be retried exactly once on the default tier.
// The test exercises the real compat executor (statusErr is unexported, so a
// fake cannot observe the concrete error type) and the real Neuralwatt
// executor wrapper, asserting:
//   - Execute returns nil error after a flex-503 → default-200 sequence
//   - the upstream saw exactly two requests
//   - the first body carried the flex stamp and the second did not
//   - the ctx-scoped provider metadata carries flex_downgraded=true so the
//     dashboard / audit log can show the shed happened
func TestNeuralwattFlexRetryExecute(t *testing.T) {
	server := newFlexRetryServer(t, func(body []byte) (int, string, []byte) {
		if bytes.Contains(body, []byte(`"service_tier":"flex"`)) {
			return http.StatusServiceUnavailable, "application/json", []byte(`{"error":{"message":"flex shed"}}`)
		}
		return http.StatusOK, "application/json", []byte(flexRetrySuccessBody)
	})
	defer server.close()

	executor := newFlexRetryExecutor()
	auth := newFlexRetryAuth(server.URL, "flex")

	ctx := helps.EnsureProviderUsageMetadata(context.Background())
	_, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "neuralwatt-model",
		Payload: []byte(neuralwattRequestPayload),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := server.requestCount(); got != 2 {
		t.Fatalf("server observed %d requests, want 2", got)
	}
	if !bytes.Contains(server.bodyAt(0), []byte(`"service_tier":"flex"`)) {
		t.Fatalf("first body missing flex stamp: %s", server.bodyAt(0))
	}
	if bytes.Contains(server.bodyAt(1), []byte(`"service_tier":"flex"`)) {
		t.Fatalf("retry body still contains flex stamp: %s", server.bodyAt(1))
	}
	md := helps.ProviderUsageMetadataFromContext(ctx)
	inner, ok := md.Metadata["neuralwatt"].(map[string]any)
	if !ok {
		t.Fatalf("neuralwatt metadata missing: %+v", md.Metadata)
	}
	if got, _ := inner["flex_downgraded"].(bool); !got {
		t.Fatalf("flex_downgraded = %v, want true; metadata = %+v", inner["flex_downgraded"], inner)
	}
}

// TestNeuralwattFlexRetryExecuteStream mirrors the Execute retry for the
// streaming path. The upstream's 503 surfaces BEFORE any SSE chunk is
// delivered (the compat executor returns the statusErr from the initial
// connection, before it opens the chunk channel), so the retry is
// well-defined: there are no partial-stream bytes to drop on the floor.
func TestNeuralwattFlexRetryExecuteStream(t *testing.T) {
	server := newFlexRetryServer(t, func(body []byte) (int, string, []byte) {
		if bytes.Contains(body, []byte(`"service_tier":"flex"`)) {
			return http.StatusServiceUnavailable, "application/json", []byte(`{"error":{"message":"flex shed"}}`)
		}
		return http.StatusOK, "text/event-stream", []byte(flexRetryStreamBody)
	})
	defer server.close()

	executor := newFlexRetryExecutor()
	auth := newFlexRetryAuth(server.URL, "flex")

	ctx := helps.EnsureProviderUsageMetadata(context.Background())
	result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "neuralwatt-model",
		Payload: []byte(neuralwattRequestPayload),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	// Drain the channel so the streaming goroutine finishes and any sink
	// hooks (which capture the response) have landed before we inspect the
	// metadata holder.
	for range result.Chunks {
	}
	if got := server.requestCount(); got != 2 {
		t.Fatalf("server observed %d requests, want 2", got)
	}
	if !bytes.Contains(server.bodyAt(0), []byte(`"service_tier":"flex"`)) {
		t.Fatalf("first body missing flex stamp: %s", server.bodyAt(0))
	}
	if bytes.Contains(server.bodyAt(1), []byte(`"service_tier":"flex"`)) {
		t.Fatalf("retry body still contains flex stamp: %s", server.bodyAt(1))
	}
	md := helps.ProviderUsageMetadataFromContext(ctx)
	inner, ok := md.Metadata["neuralwatt"].(map[string]any)
	if !ok {
		t.Fatalf("neuralwatt metadata missing: %+v", md.Metadata)
	}
	if got, _ := inner["flex_downgraded"].(bool); !got {
		t.Fatalf("flex_downgraded = %v, want true; metadata = %+v", inner["flex_downgraded"], inner)
	}
}

// TestNeuralwattFlexNoRetryOnNon503 pins the status-code guard: a flex
// credential whose first attempt fails with a non-503 status must NOT be
// retried (no retry on transient server bugs, validation failures, etc).
// The downgrade flag must NOT be set either because no retry happened.
func TestNeuralwattFlexNoRetryOnNon503(t *testing.T) {
	server := newFlexRetryServer(t, func(body []byte) (int, string, []byte) {
		return http.StatusInternalServerError, "application/json", []byte(`{"error":{"message":"upstream bug"}}`)
	})
	defer server.close()

	executor := newFlexRetryExecutor()
	auth := newFlexRetryAuth(server.URL, "flex")

	ctx := helps.EnsureProviderUsageMetadata(context.Background())
	_, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "neuralwatt-model",
		Payload: []byte(neuralwattRequestPayload),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err == nil {
		t.Fatal("expected error for non-2xx response, got nil")
	}
	if got := server.requestCount(); got != 1 {
		t.Fatalf("server observed %d requests, want 1 (no retry on non-503)", got)
	}
	md := helps.ProviderUsageMetadataFromContext(ctx)
	if inner, ok := md.Metadata["neuralwatt"].(map[string]any); ok {
		if got, _ := inner["flex_downgraded"].(bool); got {
			t.Fatalf("flex_downgraded set on non-503 path; metadata = %+v", inner)
		}
	}
}

// TestNeuralwattFlexNoRetryOnNonFlexTier pins the tier guard: a credential
// whose service_tier is NOT "flex" (here "default") must NOT be retried even
// on a 503. The retry only sheds flex→default; if the caller already asked
// for default (or has no tier at all) there is nothing to fall back to.
func TestNeuralwattFlexNoRetryOnNonFlexTier(t *testing.T) {
	server := newFlexRetryServer(t, func(body []byte) (int, string, []byte) {
		return http.StatusServiceUnavailable, "application/json", []byte(`{"error":{"message":"down"}}`)
	})
	defer server.close()

	executor := newFlexRetryExecutor()
	auth := newFlexRetryAuth(server.URL, "default")

	ctx := helps.EnsureProviderUsageMetadata(context.Background())
	_, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "neuralwatt-model",
		Payload: []byte(neuralwattRequestPayload),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err == nil {
		t.Fatal("expected error for 503 response, got nil")
	}
	if got := server.requestCount(); got != 1 {
		t.Fatalf("server observed %d requests, want 1 (default tier is not retried)", got)
	}
}

// TestNeuralwattFlexRetryExactlyOnce pins the off-by-one guard: even when the
// retry also returns 503, the executor must NOT loop. Exactly two requests,
// period, with the retry's error propagated to the caller.
func TestNeuralwattFlexRetryExactlyOnce(t *testing.T) {
	server := newFlexRetryServer(t, func(body []byte) (int, string, []byte) {
		return http.StatusServiceUnavailable, "application/json", []byte(`{"error":{"message":"still down"}}`)
	})
	defer server.close()

	executor := newFlexRetryExecutor()
	auth := newFlexRetryAuth(server.URL, "flex")

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "neuralwatt-model",
		Payload: []byte(neuralwattRequestPayload),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err == nil {
		t.Fatal("expected error from double-503, got nil")
	}
	if got := server.requestCount(); got != 2 {
		t.Fatalf("server observed %d requests, want 2 (exactly one retry)", got)
	}
}

// TestNeuralwattFlexNoRetryOnFirstTrySuccess pins the "successful first try
// avoids the downgrade flag" contract: when the flex attempt succeeds on
// the first try, the executor must NOT stamp flex_downgraded (no retry
// happened) and the upstream must see exactly one request.
func TestNeuralwattFlexNoRetryOnFirstTrySuccess(t *testing.T) {
	server := newFlexRetryServer(t, func(body []byte) (int, string, []byte) {
		return http.StatusOK, "application/json", []byte(flexRetrySuccessBody)
	})
	defer server.close()

	executor := newFlexRetryExecutor()
	auth := newFlexRetryAuth(server.URL, "flex")

	ctx := helps.EnsureProviderUsageMetadata(context.Background())
	_, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "neuralwatt-model",
		Payload: []byte(neuralwattRequestPayload),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := server.requestCount(); got != 1 {
		t.Fatalf("server observed %d requests, want 1 (success = no retry)", got)
	}
	md := helps.ProviderUsageMetadataFromContext(ctx)
	if inner, ok := md.Metadata["neuralwatt"].(map[string]any); ok {
		if got, _ := inner["flex_downgraded"].(bool); got {
			t.Fatalf("flex_downgraded set on success path; metadata = %+v", inner)
		}
	}
}
