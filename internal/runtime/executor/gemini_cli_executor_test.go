package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

type recordedHTTPRequest struct {
	Method string
	URL    string
	Header http.Header
	Body   []byte
}

type scriptedRoundTripper struct {
	mu        sync.Mutex
	requests  []recordedHTTPRequest
	responses []scriptedHTTPResponse
}

type scriptedHTTPResponse struct {
	statusCode int
	body       string
	header     http.Header
	err        error
}

func (s *scriptedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	body, errRead := io.ReadAll(req.Body)
	if errRead != nil {
		return nil, errRead
	}
	_ = req.Body.Close()

	s.mu.Lock()
	index := len(s.requests)
	s.requests = append(s.requests, recordedHTTPRequest{
		Method: req.Method,
		URL:    req.URL.String(),
		Header: req.Header.Clone(),
		Body:   append([]byte(nil), body...),
	})
	respSpec := scriptedHTTPResponse{
		statusCode: http.StatusOK,
		body:       `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`,
		header:     http.Header{"Content-Type": []string{"application/json"}},
	}
	if index < len(s.responses) {
		respSpec = s.responses[index]
	}
	s.mu.Unlock()

	if respSpec.err != nil {
		return nil, respSpec.err
	}
	header := respSpec.header.Clone()
	if header == nil {
		header = http.Header{"Content-Type": []string{"application/json"}}
	}
	return &http.Response{
		StatusCode: respSpec.statusCode,
		Status:     fmt.Sprintf("%d", respSpec.statusCode),
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(respSpec.body)),
		Request:    req,
	}, nil
}

func (s *scriptedRoundTripper) Requests() []recordedHTTPRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]recordedHTTPRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

func newGeminiCLIAuth() *cliproxyauth.Auth {
	expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	return &cliproxyauth.Auth{
		ID:       "auth-1",
		Provider: "gemini-cli",
		Metadata: map[string]any{
			"project_id":    "mimetic-mantra-3pbjf",
			"access_token":  "access-token",
			"refresh_token": "refresh-token",
			"token_type":    "Bearer",
			"expiry":        expiry,
			"token": map[string]any{
				"access_token":  "access-token",
				"refresh_token": "refresh-token",
				"token_type":    "Bearer",
				"expiry":        expiry,
				"client_id":     "client-id",
				"client_secret": "client-secret",
			},
		},
	}
}

func newOpenAIRequestPayload(model string) []byte {
	return []byte(fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"Reply with exactly: ok"}]}`, model))
}

func TestGeminiCLIExecute_SendsOfficialHeadersAndBody(t *testing.T) {
	rt := &scriptedRoundTripper{}
	executor := NewGeminiCLIExecutor(&config.Config{})
	auth := newGeminiCLIAuth()
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(rt))

	_, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "gemini-3-flash-preview",
		Payload: newOpenAIRequestPayload("gemini-3-flash-preview"),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	requests := rt.Requests()
	if len(requests) != 1 {
		t.Fatalf("request count = %d, want 1", len(requests))
	}
	req := requests[0]
	if req.URL != "https://cloudcode-pa.googleapis.com/v1internal:generateContent" {
		t.Fatalf("request URL = %s", req.URL)
	}
	if got := req.Header.Get("User-Agent"); got != misc.GeminiCLIUserAgent("gemini-3-flash-preview") {
		t.Fatalf("unexpected User-Agent %q", got)
	}
	if got := req.Header.Get("X-Goog-Api-Client"); got != "" {
		t.Fatalf("unexpected X-Goog-Api-Client header %q", got)
	}

	var payload map[string]any
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got, _ := payload["project"].(string); got != "mimetic-mantra-3pbjf" {
		t.Fatalf("project = %q, want mimetic-mantra-3pbjf", got)
	}
	if got, _ := payload["model"].(string); got != "gemini-3-flash-preview" {
		t.Fatalf("model = %q, want gemini-3-flash-preview", got)
	}
	if got, _ := payload["user_prompt_id"].(string); strings.TrimSpace(got) == "" {
		t.Fatal("user_prompt_id is empty")
	}
	requestNode, _ := payload["request"].(map[string]any)
	if got, _ := requestNode["session_id"].(string); strings.TrimSpace(got) == "" {
		t.Fatal("request.session_id is empty")
	}
}

func TestGeminiCLIExecute_RetriesUsingConfiguredRequestRetry(t *testing.T) {
	originalDelay := geminiCLIDefaultRetryDelay
	geminiCLIDefaultRetryDelay = 0
	t.Cleanup(func() { geminiCLIDefaultRetryDelay = originalDelay })

	rt := &scriptedRoundTripper{
		responses: []scriptedHTTPResponse{
			{statusCode: http.StatusTooManyRequests, body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED"}}`, header: http.Header{"Content-Type": []string{"application/json"}}},
			{statusCode: http.StatusTooManyRequests, body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED"}}`, header: http.Header{"Content-Type": []string{"application/json"}}},
			{statusCode: http.StatusOK, body: `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`, header: http.Header{"Content-Type": []string{"application/json"}}},
		},
	}
	executor := NewGeminiCLIExecutor(&config.Config{RequestRetry: 2})
	auth := newGeminiCLIAuth()
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(rt))

	if _, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "gemini-3-flash-preview",
		Payload: newOpenAIRequestPayload("gemini-3-flash-preview"),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")}); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if got := len(rt.Requests()); got != 3 {
		t.Fatalf("request count = %d, want 3", got)
	}
}

func TestGeminiCLIExecuteStream_UsesSSEPathWithoutLocalRetry(t *testing.T) {
	rt := &scriptedRoundTripper{
		responses: []scriptedHTTPResponse{
			{statusCode: http.StatusTooManyRequests, body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED"}}`, header: http.Header{"Content-Type": []string{"application/json"}}},
		},
	}
	executor := NewGeminiCLIExecutor(&config.Config{RequestRetry: 3})
	auth := newGeminiCLIAuth()
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(rt))

	_, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gemini-3-flash-preview",
		Payload: newOpenAIRequestPayload("gemini-3-flash-preview"),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err == nil {
		t.Fatal("expected ExecuteStream error, got nil")
	}

	requests := rt.Requests()
	if len(requests) != 1 {
		t.Fatalf("request count = %d, want 1", len(requests))
	}
	if requests[0].URL != "https://cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse" {
		t.Fatalf("request URL = %s", requests[0].URL)
	}
}

func TestGeminiCLICountTokens_UsesOfficialBodyAndRetryConfig(t *testing.T) {
	originalDelay := geminiCLIDefaultRetryDelay
	geminiCLIDefaultRetryDelay = 0
	t.Cleanup(func() { geminiCLIDefaultRetryDelay = originalDelay })

	rt := &scriptedRoundTripper{
		responses: []scriptedHTTPResponse{
			{statusCode: http.StatusInternalServerError, body: `{"error":{"code":500,"status":"INTERNAL"}}`, header: http.Header{"Content-Type": []string{"application/json"}}},
			{statusCode: http.StatusOK, body: `{"totalTokens":123}`, header: http.Header{"Content-Type": []string{"application/json"}}},
		},
	}
	executor := NewGeminiCLIExecutor(&config.Config{RequestRetry: 1})
	auth := newGeminiCLIAuth()
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(rt))

	if _, err := executor.CountTokens(ctx, auth, cliproxyexecutor.Request{
		Model:   "gemini-3-flash-preview",
		Payload: newOpenAIRequestPayload("gemini-3-flash-preview"),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")}); err != nil {
		t.Fatalf("CountTokens returned error: %v", err)
	}

	requests := rt.Requests()
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	var payload map[string]any
	if err := json.Unmarshal(requests[0].Body, &payload); err != nil {
		t.Fatalf("unmarshal countTokens payload: %v", err)
	}
	requestNode, _ := payload["request"].(map[string]any)
	if got, _ := requestNode["model"].(string); got != "models/gemini-3-flash-preview" {
		t.Fatalf("request.model = %q, want models/gemini-3-flash-preview", got)
	}
	if _, exists := payload["project"]; exists {
		t.Fatal("countTokens payload should not include top-level project")
	}
	if got := requests[0].Header.Get("X-Goog-Api-Client"); got != "" {
		t.Fatalf("unexpected X-Goog-Api-Client header %q", got)
	}
}
