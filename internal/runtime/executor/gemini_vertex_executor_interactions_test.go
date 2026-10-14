package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestGeminiVertexInteractions_ServiceAccount_Execute(t *testing.T) {
	var mu sync.Mutex
	var gotPath string
	var gotMethod string
	var gotAuth string
	var gotRevision string
	var upstreamBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"mock-vertex-sa-token","token_type":"Bearer","expires_in":3600}`))
			return
		}
		mu.Lock()
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotRevision = r.Header.Get("Api-Revision")
		body, _ := io.ReadAll(r.Body)
		upstreamBody = body
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"interaction_vertex_1",
			"object":"interaction",
			"status":"completed",
			"steps":[{"type":"model_output","content":[{"text":"vertex interactions response"}]}],
			"usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}
		}`))
	}))
	defer server.Close()

	targetURL, errParse := url.Parse(server.URL)
	if errParse != nil {
		t.Fatal(errParse)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	t.Cleanup(transport.CloseIdleConnections)

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", issue6258RedirectTransport{
		target: targetURL,
		base:   transport,
	})

	var sa map[string]any
	if errUnmarshal := json.Unmarshal(testVertexServiceAccountJSON(t, server.URL+"/token"), &sa); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}

	auth := &cliproxyauth.Auth{
		ID:       "vertex-sa-interactions",
		Provider: "vertex",
		Metadata: map[string]any{
			"project_id":      "proxy-test",
			"location":        "global",
			"interactions":    true,
			"service_account": sa,
		},
	}

	exec := NewGeminiVertexExecutor(&config.Config{})
	req := cliproxyexecutor.Request{
		Model:   "gemini-3.8-flash",
		Payload: []byte(`{"model":"gemini-3.8-flash","input":"hi vertex interactions"}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatInteractions,
		ResponseFormat: sdktranslator.FormatInteractions,
	}

	resp, errExecute := exec.Execute(ctx, auth, req, opts)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	mu.Lock()
	defer mu.Unlock()

	expectedPath := "/v1beta1/projects/proxy-test/locations/global/interactions"
	if gotPath != expectedPath {
		t.Fatalf("got path = %q, want %q", gotPath, expectedPath)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("got method = %q, want %q", gotMethod, http.MethodPost)
	}
	if gotAuth != "Bearer mock-vertex-sa-token" {
		t.Fatalf("got auth = %q, want Bearer mock-vertex-sa-token", gotAuth)
	}
	if gotRevision != "2026-05-20" {
		t.Fatalf("got Api-Revision = %q, want 2026-05-20", gotRevision)
	}
	if !gjson.GetBytes(upstreamBody, "input").Exists() {
		t.Fatalf("expected input in upstream body, got: %s", string(upstreamBody))
	}
	if gotID := gjson.GetBytes(resp.Payload, "id").String(); gotID != "interaction_vertex_1" {
		t.Fatalf("got response id = %q, want interaction_vertex_1", gotID)
	}
}

func TestGeminiVertexInteractions_ServiceAccount_ExecuteStream(t *testing.T) {
	var mu sync.Mutex
	var gotPath string
	var gotQuery string
	var gotRevision string

	// Generate a 1MB payload to verify StreamScannerBuffer handles large frames without bufio.ErrTooLong
	largeOutput := strings.Repeat("A", 1024*1024)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"mock-vertex-sa-token","token_type":"Bearer","expires_in":3600}`))
			return
		}
		mu.Lock()
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotRevision = r.Header.Get("Api-Revision")
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: interaction.created\ndata: {\"id\":\"interaction_vertex_stream\",\"object\":\"interaction\",\"status\":\"in_progress\"}\n\n"))
		largeCompleted := fmt.Sprintf("event: interaction.completed\ndata: {\"id\":\"interaction_vertex_stream\",\"object\":\"interaction\",\"status\":\"completed\",\"steps\":[{\"type\":\"model_output\",\"content\":[{\"text\":%q}]}],\"usage\":{\"input_tokens\":5,\"output_tokens\":1000,\"total_tokens\":1005}}\n\n", largeOutput)
		_, _ = w.Write([]byte(largeCompleted))
	}))
	defer server.Close()

	targetURL, errParse := url.Parse(server.URL)
	if errParse != nil {
		t.Fatal(errParse)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	t.Cleanup(transport.CloseIdleConnections)

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", issue6258RedirectTransport{
		target: targetURL,
		base:   transport,
	})

	var sa map[string]any
	if errUnmarshal := json.Unmarshal(testVertexServiceAccountJSON(t, server.URL+"/token"), &sa); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}

	auth := &cliproxyauth.Auth{
		ID:       "vertex-sa-interactions-stream",
		Provider: "vertex",
		Metadata: map[string]any{
			"project_id":      "proxy-test",
			"location":        "global",
			"interactions":    true,
			"service_account": sa,
		},
	}

	exec := NewGeminiVertexExecutor(&config.Config{})
	req := cliproxyexecutor.Request{
		Model:   "gemini-3.8-flash",
		Payload: []byte(`{"model":"gemini-3.8-flash","input":"hi vertex interactions stream"}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatInteractions,
		ResponseFormat: sdktranslator.FormatInteractions,
	}

	stream, errStream := exec.ExecuteStream(ctx, auth, req, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}

	var chunkCount int
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error (scanner buffer overflow?): %v", chunk.Err)
		}
		chunkCount++
	}

	mu.Lock()
	defer mu.Unlock()

	expectedPath := "/v1beta1/projects/proxy-test/locations/global/interactions"
	if gotPath != expectedPath {
		t.Fatalf("got path = %q, want %q", gotPath, expectedPath)
	}
	if !strings.Contains(gotQuery, "alt=sse") {
		t.Fatalf("got query = %q, want query containing alt=sse", gotQuery)
	}
	if gotRevision != "2026-05-20" {
		t.Fatalf("got Api-Revision = %q, want 2026-05-20", gotRevision)
	}
	if chunkCount == 0 {
		t.Fatal("expected at least 1 stream chunk, got 0")
	}
}

func TestGeminiVertexInteractions_ServiceAccount_WithoutOptIn_UsesGenerateContent(t *testing.T) {
	var mu sync.Mutex
	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"mock-vertex-sa-token","token_type":"Bearer","expires_in":3600}`))
			return
		}
		mu.Lock()
		gotPath = r.URL.Path
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"responseId":"resp1","candidates":[{"content":{"parts":[{"text":"global generateContent ok"}]},"finishReason":"STOP"}]}`))
	}))
	defer server.Close()

	targetURL, errParse := url.Parse(server.URL)
	if errParse != nil {
		t.Fatal(errParse)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	t.Cleanup(transport.CloseIdleConnections)

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", issue6258RedirectTransport{
		target: targetURL,
		base:   transport,
	})

	var sa map[string]any
	if errUnmarshal := json.Unmarshal(testVertexServiceAccountJSON(t, server.URL+"/token"), &sa); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}

	// Service account has location: "global" but NO explicit interactions opt-in
	auth := &cliproxyauth.Auth{
		ID:       "vertex-sa-global-no-optin",
		Provider: "vertex",
		Metadata: map[string]any{
			"project_id":      "proxy-test",
			"location":        "global",
			"service_account": sa,
		},
	}

	exec := NewGeminiVertexExecutor(&config.Config{})
	req := cliproxyexecutor.Request{
		Model:   "gemini-3.8-flash",
		Payload: []byte(`{"model":"gemini-3.8-flash","input":"hi no optin"}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatInteractions,
		ResponseFormat: sdktranslator.FormatInteractions,
	}

	_, errExecute := exec.Execute(ctx, auth, req, opts)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	mu.Lock()
	defer mu.Unlock()

	expectedPath := "/v1/projects/proxy-test/locations/global/publishers/google/models/gemini-3.8-flash:generateContent"
	if gotPath != expectedPath {
		t.Fatalf("got path = %q, want global generateContent path %q", gotPath, expectedPath)
	}
}

func TestGeminiVertexInteractions_RegionalServiceAccount_UsesStandardBridge(t *testing.T) {
	var mu sync.Mutex
	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"mock-vertex-sa-token","token_type":"Bearer","expires_in":3600}`))
			return
		}
		mu.Lock()
		gotPath = r.URL.Path
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"responseId":"resp1","candidates":[{"content":{"parts":[{"text":"regional ok"}]},"finishReason":"STOP"}]}`))
	}))
	defer server.Close()

	targetURL, errParse := url.Parse(server.URL)
	if errParse != nil {
		t.Fatal(errParse)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	t.Cleanup(transport.CloseIdleConnections)

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", issue6258RedirectTransport{
		target: targetURL,
		base:   transport,
	})

	var sa map[string]any
	if errUnmarshal := json.Unmarshal(testVertexServiceAccountJSON(t, server.URL+"/token"), &sa); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}

	// Regional service account without explicit interactions opt-in
	auth := &cliproxyauth.Auth{
		ID:       "vertex-sa-regional",
		Provider: "vertex",
		Metadata: map[string]any{
			"project_id":      "proxy-test",
			"location":        "europe-west1",
			"service_account": sa,
		},
	}

	exec := NewGeminiVertexExecutor(&config.Config{})
	req := cliproxyexecutor.Request{
		Model:   "gemini-3.8-flash",
		Payload: []byte(`{"model":"gemini-3.8-flash","input":"hi regional"}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatInteractions,
		ResponseFormat: sdktranslator.FormatInteractions,
	}

	_, errExecute := exec.Execute(ctx, auth, req, opts)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	mu.Lock()
	defer mu.Unlock()

	expectedRegionalPath := "/v1/projects/proxy-test/locations/europe-west1/publishers/google/models/gemini-3.8-flash:generateContent"
	if gotPath != expectedRegionalPath {
		t.Fatalf("got path = %q, want regional generateContent path %q", gotPath, expectedRegionalPath)
	}
}

func TestGeminiVertexInteractions_APIKey_Execute(t *testing.T) {
	var mu sync.Mutex
	var gotPath string
	var gotKey string
	var gotRevision string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-goog-api-key")
		gotRevision = r.Header.Get("Api-Revision")
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"interaction_vertex_key",
			"object":"interaction",
			"status":"completed",
			"steps":[{"type":"model_output","content":[{"text":"key ok"}]}],
			"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}
		}`))
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		ID:       "vertex-apikey-interactions",
		Provider: "vertex",
		Attributes: map[string]string{
			"api_key":      "test-vertex-key",
			"base_url":     server.URL,
			"interactions": "true",
		},
	}

	exec := NewGeminiVertexExecutor(&config.Config{})
	req := cliproxyexecutor.Request{
		Model:   "gemini-3.8-flash",
		Payload: []byte(`{"model":"gemini-3.8-flash","input":"hi vertex key"}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatInteractions,
		ResponseFormat: sdktranslator.FormatInteractions,
	}

	resp, errExecute := exec.Execute(context.Background(), auth, req, opts)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	mu.Lock()
	defer mu.Unlock()

	if !strings.HasSuffix(gotPath, "interactions") {
		t.Fatalf("got path = %q, want path ending with interactions", gotPath)
	}
	if gotKey != "test-vertex-key" {
		t.Fatalf("got key = %q, want test-vertex-key", gotKey)
	}
	if gotRevision != "2026-05-20" {
		t.Fatalf("got Api-Revision = %q, want 2026-05-20", gotRevision)
	}
	if gotID := gjson.GetBytes(resp.Payload, "id").String(); gotID != "interaction_vertex_key" {
		t.Fatalf("got response id = %q, want interaction_vertex_key", gotID)
	}
}

func TestGeminiVertexInteractions_APIKey_WithoutOptIn_UsesGenerateContent(t *testing.T) {
	var mu sync.Mutex
	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotPath = r.URL.Path
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"responseId":"resp1","candidates":[{"content":{"parts":[{"text":"relay ok"}]},"finishReason":"STOP"}]}`))
	}))
	defer server.Close()

	// Third-party relay API key without interactions: true
	auth := &cliproxyauth.Auth{
		ID:       "vertex-relay-key",
		Provider: "vertex",
		Attributes: map[string]string{
			"api_key":  "test-relay-key",
			"base_url": server.URL,
		},
	}

	exec := NewGeminiVertexExecutor(&config.Config{})
	req := cliproxyexecutor.Request{
		Model:   "gemini-3.8-flash",
		Payload: []byte(`{"model":"gemini-3.8-flash","input":"hi relay"}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatInteractions,
		ResponseFormat: sdktranslator.FormatInteractions,
	}

	_, errExecute := exec.Execute(context.Background(), auth, req, opts)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	mu.Lock()
	defer mu.Unlock()

	expectedRelayPath := "/v1/publishers/google/models/gemini-3.8-flash:generateContent"
	if gotPath != expectedRelayPath {
		t.Fatalf("got path = %q, want relay generateContent path %q", gotPath, expectedRelayPath)
	}
}

func TestGeminiVertexInteractions_PayloadRulesOrdering(t *testing.T) {
	var mu sync.Mutex
	var upstreamBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = body
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"interaction_1","object":"interaction","status":"completed","steps":[{"type":"model_output","content":[{"text":"ok"}]}]}`))
	}))
	defer server.Close()

	exec := NewGeminiVertexExecutor(&config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "gemini-3.8-flash", Protocol: "interactions", FromProtocol: "interactions"},
					},
					Params: map[string]any{
						"custom_rule_param": "enforced_after_all_transforms",
					},
				},
			},
		},
	})
	auth := &cliproxyauth.Auth{
		ID:       "vertex-apikey-interactions-rules",
		Provider: "vertex",
		Attributes: map[string]string{
			"api_key":      "test-key",
			"base_url":     server.URL,
			"interactions": "true",
		},
	}
	req := cliproxyexecutor.Request{
		Model: "gemini-3.8-flash",
		Payload: []byte(`{
			"model":"gemini-3.8-flash",
			"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]
		}`),
	}

	_, errExecute := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatInteractions,
		ResponseFormat: sdktranslator.FormatInteractions,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	mu.Lock()
	defer mu.Unlock()

	if got := gjson.GetBytes(upstreamBody, "custom_rule_param").String(); got != "enforced_after_all_transforms" {
		t.Fatalf("custom_rule_param = %q, want enforced_after_all_transforms. Body: %s", got, string(upstreamBody))
	}
}
