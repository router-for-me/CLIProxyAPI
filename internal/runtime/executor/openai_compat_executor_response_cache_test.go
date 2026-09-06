package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func newResponseCacheTestExecutor(t *testing.T, serverURL string, cacheCfg config.ResponseCacheConfig) (*OpenAICompatExecutor, *cliproxyauth.Auth) {
	t.Helper()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:          "compat",
			ResponseCache: cacheCfg,
		}},
	})
	auth := &cliproxyauth.Auth{
		ID:       "auth-1",
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     serverURL + "/v1",
			"api_key":      "test",
			"compat_name":  "compat",
			"provider_key": "compat",
		},
	}
	return executor, auth
}

func TestOpenAICompatExecutorResponseCacheServesIdenticalRequest(t *testing.T) {
	var upstreamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom-Passthrough", "header-val")
		_, _ = fmt.Fprintf(w, `{"id":"chatcmpl-%d","object":"chat.completion","model":"claude-opus-5","choices":[{"index":0,"message":{"role":"assistant","content":"answer-%d"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`, call, call)
	}))
	defer server.Close()

	executor, auth := newResponseCacheTestExecutor(t, server.URL, config.ResponseCacheConfig{Enabled: true, TTL: "1m"})
	payload := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: false}

	first, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}, opts)
	if err != nil {
		t.Fatalf("first Execute error: %v", err)
	}
	second, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}, opts)
	if err != nil {
		t.Fatalf("second Execute error: %v", err)
	}

	if first.Headers.Get("X-Custom-Passthrough") != "header-val" || second.Headers.Get("X-Custom-Passthrough") != "header-val" {
		t.Fatalf("expected preserved response headers on cache hit: first=%q, second=%q", first.Headers.Get("X-Custom-Passthrough"), second.Headers.Get("X-Custom-Passthrough"))
	}
	if got := upstreamCalls.Load(); got != 1 {
		t.Fatalf("expected 1 upstream call, got %d", got)
	}
	firstContent := gjson.GetBytes(first.Payload, "choices.0.message.content").String()
	secondContent := gjson.GetBytes(second.Payload, "choices.0.message.content").String()
	if firstContent != "answer-1" || secondContent != "answer-1" {
		t.Fatalf("unexpected contents: %q then %q", firstContent, secondContent)
	}
}

func TestOpenAICompatExecutorResponseCacheDisabledByDefault(t *testing.T) {
	var upstreamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"claude-opus-5","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor, auth := newResponseCacheTestExecutor(t, server.URL, config.ResponseCacheConfig{})
	payload := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: false}

	for i := 0; i < 2; i++ {
		if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}, opts); err != nil {
			t.Fatalf("Execute error: %v", err)
		}
	}
	if got := upstreamCalls.Load(); got != 2 {
		t.Fatalf("expected caching to stay off, upstream calls = %d", got)
	}
}

func TestOpenAICompatExecutorResponseCacheDistinguishesPayloads(t *testing.T) {
	var upstreamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"claude-opus-5","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor, auth := newResponseCacheTestExecutor(t, server.URL, config.ResponseCacheConfig{Enabled: true, TTL: "1m"})
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: false}

	for _, prompt := range []string{"hi", "hello"} {
		payload := []byte(fmt.Sprintf(`{"model":"claude-opus-5","messages":[{"role":"user","content":%q}]}`, prompt))
		if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}, opts); err != nil {
			t.Fatalf("Execute error: %v", err)
		}
	}
	if got := upstreamCalls.Load(); got != 2 {
		t.Fatalf("expected distinct prompts to reach upstream twice, got %d", got)
	}
}

func TestOpenAICompatExecutorResponseCacheReplaysStream(t *testing.T) {
	var upstreamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Custom-Stream-Header", "stream-val")
		flusher, _ := w.(http.Flusher)
		frames := []string{
			`{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"claude-opus-5","choices":[{"index":0,"delta":{"role":"assistant","content":"he"}}]}`,
			`{"id":"chatcmpl-1","object":"chat.completion.chunk","model":"claude-opus-5","choices":[{"index":0,"delta":{"content":"llo"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`,
			"[DONE]",
		}
		for _, frame := range frames {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", frame)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	executor, auth := newResponseCacheTestExecutor(t, server.URL, config.ResponseCacheConfig{Enabled: true, TTL: "1m"})
	payload := []byte(`{"model":"claude-opus-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true}

	collect := func() (string, string) {
		result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}, opts)
		if err != nil {
			t.Fatalf("ExecuteStream error: %v", err)
		}
		headerVal := result.Headers.Get("X-Custom-Stream-Header")
		var builder strings.Builder
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				t.Fatalf("stream chunk error: %v", chunk.Err)
			}
			builder.Write(chunk.Payload)
		}
		return builder.String(), headerVal
	}

	live, liveH := collect()
	replayed, replayedH := collect()

	if liveH != "stream-val" || replayedH != "stream-val" {
		t.Fatalf("expected stream response headers preserved on hit: live=%q, replay=%q", liveH, replayedH)
	}

	if got := upstreamCalls.Load(); got != 1 {
		t.Fatalf("expected 1 upstream stream call, got %d", got)
	}
	if live != replayed {
		t.Fatalf("replayed stream differs from live stream:\nlive=%q\nreplay=%q", live, replayed)
	}
	if !strings.Contains(live, "llo") {
		t.Fatalf("expected streamed content in output: %q", live)
	}
}

func TestOpenAICompatExecutorResponseCacheSkipsIncompleteStream(t *testing.T) {
	var upstreamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		// No [DONE] sentinel: the stream ends early and must not be cached.
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"model\":\"claude-opus-5\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	executor, auth := newResponseCacheTestExecutor(t, server.URL, config.ResponseCacheConfig{Enabled: true, TTL: "1m"})
	payload := []byte(`{"model":"claude-opus-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true}

	for i := 0; i < 2; i++ {
		result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}, opts)
		if err != nil {
			t.Fatalf("ExecuteStream error: %v", err)
		}
		for chunk := range result.Chunks {
			_ = chunk
		}
	}
	if got := upstreamCalls.Load(); got != 2 {
		t.Fatalf("expected incomplete stream to bypass the cache, upstream calls = %d", got)
	}
}

func TestOpenAICompatExecutorResponseCacheIgnoresErrorResponses(t *testing.T) {
	var upstreamCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer server.Close()

	executor, auth := newResponseCacheTestExecutor(t, server.URL, config.ResponseCacheConfig{Enabled: true, TTL: "1m"})
	payload := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: false}

	for i := 0; i < 2; i++ {
		if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}, opts); err == nil {
			t.Fatal("expected upstream error to propagate")
		}
	}
	if got := upstreamCalls.Load(); got != 2 {
		t.Fatalf("expected errors to never be cached, upstream calls = %d", got)
	}
}
