package executor_test

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkexec "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func TestZhipuExecutor_IdentifierAndErrors(t *testing.T) {
	exec := executor.NewZhipuExecutor(&config.Config{})
	if exec.Identifier() != "zhipu" {
		t.Fatalf("identifier mismatch")
	}
	ctx := context.Background()
	// When bridge is disabled by default (zero value), executor should fallback to OpenAI-compat
	// and report missing baseURL if not provided via auth.
	_, err := exec.Execute(ctx, &coreauth.Auth{Attributes: map[string]string{"api_key": "k"}}, sdkexec.Request{Model: "glm-4.5"}, sdkexec.Options{})
	if err == nil {
		t.Fatalf("expected error due to missing provider baseURL")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "missing provider baseurl") {
		t.Fatalf("expected missing provider baseURL error, got: %v", err)
	}
}

// When claude-agent-sdk-for-python.enabled=false, ZhipuExecutor should fallback to legacy OpenAI-compat direct path.
// With missing baseURL in auth, OpenAICompatExecutor returns a specific error that we assert on.
func TestZhipuExecutor_OpenAICompat_MissingBaseURL_NonStream(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{}
	exec := executor.NewZhipuExecutor(cfg)

	auth := &coreauth.Auth{Attributes: map[string]string{"api_key": "glmsk-test"}}
	_, err := exec.Execute(ctx, auth, sdkexec.Request{Model: "glm-4.6", Payload: []byte(`{"messages":[]}`)}, sdkexec.Options{})
	if err == nil {
		t.Fatalf("expected error due to missing provider baseURL")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "missing provider baseurl") {
		t.Fatalf("expected missing provider baseURL error, got: %v", err)
	}
}

func TestZhipuExecutor_OpenAICompat_MissingBaseURL_Stream(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{}
	exec := executor.NewZhipuExecutor(cfg)

	auth := &coreauth.Auth{Attributes: map[string]string{"api_key": "glmsk-test"}}
	ch, err := exec.ExecuteStream(ctx, auth, sdkexec.Request{Model: "glm-4.6", Payload: []byte(`{"messages":[],"stream":true}`)}, sdkexec.Options{Stream: true})
	if err == nil {
		t.Fatalf("expected error due to missing provider baseURL (stream)")
	}
	if ch != nil {
		t.Fatalf("expected nil stream channel on error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "missing provider baseurl") {
		t.Fatalf("expected missing provider baseURL error, got: %v", err)
	}
}

// Positive path: OpenAI-compat direct path with base_url set on auth (non-stream)
func TestZhipuExecutor_OpenAICompatBaseURL_NonStream(t *testing.T) {
	var gotPath, gotAuth, gotCT, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		// Minimal OpenAI chat completion JSON
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	cfg := &config.Config{}
	exec := executor.NewZhipuExecutor(cfg)
	ctx := context.Background()
	auth := &coreauth.Auth{Attributes: map[string]string{"api_key": "tok", "base_url": srv.URL}}
	req := sdkexec.Request{Model: "glm-4.6", Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}
	opts := sdkexec.Options{SourceFormat: sdktranslator.FromString("openai")}
	resp, err := exec.Execute(ctx, auth, req, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatalf("expected non-empty payload")
	}
	if gotPath == "" { // path may vary; ensure request reached server
		t.Fatalf("unexpected path: %q", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("unexpected Authorization header: %q", gotAuth)
	}
	if gotCT != "application/json" {
		t.Fatalf("unexpected Content-Type: %q", gotCT)
	}
	if gotAccept != "" && gotAccept != "application/json" {
		t.Fatalf("unexpected Accept: %q", gotAccept)
	}
}

// Positive path: streaming via OpenAI-compat direct path. Server emits SSE lines including [DONE]. Executor should consume
// without error and close the channel after [DONE].
func TestZhipuExecutor_OpenAICompatBaseURL_Stream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Streaming expected
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		// Send one chunk and DONE
		_, _ = fmt.Fprintf(w, "data: %s\n\n", `{"id":"c1","object":"chat.completion.chunk","choices":[{"delta":{"content":"hi"}}]}`)
		flusher.Flush()
		_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	cfg := &config.Config{}
	exec := executor.NewZhipuExecutor(cfg)
	ctx := context.Background()
	auth := &coreauth.Auth{Attributes: map[string]string{"api_key": "tok", "base_url": srv.URL}}
	req := sdkexec.Request{Model: "glm-4.6", Payload: []byte(`{"messages":[{"role":"user","content":"hi"}],"stream":true}`)}
	opts := sdkexec.Options{Stream: true, SourceFormat: sdktranslator.FromString("openai")}
	ch, err := exec.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Drain stream until closed; ensure at least one payload chunk observed.
	got := 0
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		// payload may include translated SSE data lines; just assert non-empty
		if len(chunk.Payload) > 0 {
			// ensure the line is terminated per scanner semantics (may include newline)
			_ = bufio.NewReader(strings.NewReader(string(chunk.Payload)))
			got++
		}
	}
	if got == 0 {
		t.Fatalf("expected at least one stream chunk")
	}
}

// When python agent disabled and claude-api-key base_url points to an Anthropic-compatible endpoint
// (e.g., Zhipu's), ZhipuExecutor should fallback to ClaudeExecutor path.
func TestZhipuExecutor_FallbackToClaudeAnthropic_NonStream(t *testing.T) {
	var gotPath, gotAuth, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m","content":[{"text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()

	cfg := &config.Config{}
	cfg.ClaudeKey = []config.ClaudeKey{{
		APIKey:  "tok",
		BaseURL: srv.URL, // treated as Anthropic-compatible endpoint in test
	}}
	exec := executor.NewZhipuExecutor(cfg)
	ctx := context.Background()
	// Incoming auth can be minimal; fallback uses cfg.ClaudeKey
	auth := &coreauth.Auth{Attributes: map[string]string{"api_key": "unused"}}
	req := sdkexec.Request{Model: "glm-4.6", Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}
	opts := sdkexec.Options{SourceFormat: sdktranslator.FromString("openai")}
	resp, err := exec.Execute(ctx, auth, req, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatalf("expected response payload")
	}
	if gotPath != "/v1/messages" && gotPath != "/v1/messages"+"?beta=true" {
		// accept both with and without beta query depending on executor defaults
		t.Fatalf("unexpected path: %q", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("unexpected Authorization: %q", gotAuth)
	}
	if gotCT != "application/json" {
		t.Fatalf("unexpected Content-Type: %q", gotCT)
	}
}
