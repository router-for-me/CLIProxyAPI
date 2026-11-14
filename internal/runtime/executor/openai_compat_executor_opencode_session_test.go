package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestOpenAICompatExecutorExecuteOpenCodeSession(t *testing.T) {
	var gotHeaders []http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = append(gotHeaders, r.Header.Clone())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                  "compat",
			SendOpenCodeSession:   true,
			SupportPromptCacheKey: false,
		}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     server.URL + "/v1",
			"api_key":      "test",
			"compat_name":  "compat",
			"provider_key": "compat",
		},
	}

	// Flag on => header present and non-empty.
	if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4-flash",
		Payload: []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "session-1",
		},
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       false,
	}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if len(gotHeaders) != 1 {
		t.Fatalf("expected 1 request, got %d", len(gotHeaders))
	}
	if got := gotHeaders[0].Get("x-opencode-session"); got == "" {
		t.Fatalf("expected x-opencode-session header, got none")
	}

	// Same conversation => same id.
	if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4-flash",
		Payload: []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hello again"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "session-1",
		},
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       false,
	}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	first := gotHeaders[0].Get("x-opencode-session")
	second := gotHeaders[1].Get("x-opencode-session")
	if first != second {
		t.Fatalf("same session produced different ids: %q vs %q", first, second)
	}

	// Different conversation => different id.
	if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4-flash",
		Payload: []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"new topic"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "session-2",
		},
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       false,
	}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	third := gotHeaders[2].Get("x-opencode-session")
	if third == "" {
		t.Fatalf("expected x-opencode-session header for different session")
	}
	if third == first {
		t.Fatalf("different sessions produced same id: %q", first)
	}

	// Flag off => header absent.
	executorOff := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                  "compat",
			SendOpenCodeSession:   false,
			SupportPromptCacheKey: false,
		}},
	})
	if _, err := executorOff.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4-flash",
		Payload: []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "session-3",
		},
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       false,
	}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := gotHeaders[3].Get("x-opencode-session"); got != "" {
		t.Fatalf("expected no x-opencode-session header when flag off, got %q", got)
	}
}

func TestOpenAICompatExecutorExecuteImagesOpenCodeSession(t *testing.T) {
	var gotHeaders []http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = append(gotHeaders, r.Header.Clone())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1,"data":[{"url":"https://example.com/image.png"}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                "compat",
			SendOpenCodeSession: true,
		}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     server.URL + "/v1",
			"api_key":      "test",
			"compat_name":  "compat",
			"provider_key": "compat",
		},
	}

	if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "dall-e",
		Payload: []byte(`{"model":"dall-e","prompt":"a cat"}`),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "session-images",
		},
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Stream:       false,
	}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if len(gotHeaders) != 1 {
		t.Fatalf("expected 1 request, got %d", len(gotHeaders))
	}
	if got := gotHeaders[0].Get("x-opencode-session"); got == "" {
		t.Fatalf("expected x-opencode-session header for images, got none")
	}

	executorOff := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                "compat",
			SendOpenCodeSession: false,
		}},
	})
	if _, err := executorOff.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "dall-e",
		Payload: []byte(`{"model":"dall-e","prompt":"a dog"}`),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "session-images-off",
		},
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Stream:       false,
	}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := gotHeaders[1].Get("x-opencode-session"); got != "" {
		t.Fatalf("expected no x-opencode-session header for images when flag off, got %q", got)
	}
}

func TestOpenAICompatExecutorExecuteStreamOpenCodeSession(t *testing.T) {
	var gotHeaders []http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = append(gotHeaders, r.Header.Clone())
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}]}

data: [DONE]

`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                "compat",
			SendOpenCodeSession: true,
		}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     server.URL + "/v1",
			"api_key":      "test",
			"compat_name":  "compat",
			"provider_key": "compat",
		},
	}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4-flash",
		Payload: []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "session-stream",
		},
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	// Drain the stream so the handler goroutine completes.
	for range result.Chunks {
	}

	if len(gotHeaders) != 1 {
		t.Fatalf("expected 1 request, got %d", len(gotHeaders))
	}
	if got := gotHeaders[0].Get("x-opencode-session"); got == "" {
		t.Fatalf("expected x-opencode-session header for stream, got none")
	}

	executorOff := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                "compat",
			SendOpenCodeSession: false,
		}},
	})
	resultOff, err := executorOff.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4-flash",
		Payload: []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "session-stream-off",
		},
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for range resultOff.Chunks {
	}
	if got := gotHeaders[1].Get("x-opencode-session"); got != "" {
		t.Fatalf("expected no x-opencode-session header for stream when flag off, got %q", got)
	}
}

func TestOpenAICompatExecutorExecuteImagesStreamOpenCodeSession(t *testing.T) {
	var gotHeaders []http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = append(gotHeaders, r.Header.Clone())
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"created":1,"data":[{"url":"https://example.com/image.png"}]}

data: [DONE]

`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                "compat",
			SendOpenCodeSession: true,
		}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     server.URL + "/v1",
			"api_key":      "test",
			"compat_name":  "compat",
			"provider_key": "compat",
		},
	}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "dall-e",
		Payload: []byte(`{"model":"dall-e","prompt":"a cat"}`),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "session-images-stream",
		},
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for range result.Chunks {
	}

	if len(gotHeaders) != 1 {
		t.Fatalf("expected 1 request, got %d", len(gotHeaders))
	}
	if got := gotHeaders[0].Get("x-opencode-session"); got == "" {
		t.Fatalf("expected x-opencode-session header for image stream, got none")
	}

	executorOff := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                "compat",
			SendOpenCodeSession: false,
		}},
	})
	resultOff, err := executorOff.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "dall-e",
		Payload: []byte(`{"model":"dall-e","prompt":"a dog"}`),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "session-images-stream-off",
		},
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for range resultOff.Chunks {
	}
	if got := gotHeaders[1].Get("x-opencode-session"); got != "" {
		t.Fatalf("expected no x-opencode-session header for image stream when flag off, got %q", got)
	}
}

func TestOpenAICompatExecutorOpenCodeSessionStableWithEmptyMetadata(t *testing.T) {
	var gotHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("x-opencode-session")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:                "compat",
			SendOpenCodeSession: true,
		}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     server.URL + "/v1",
			"api_key":      "test",
			"compat_name":  "compat",
			"provider_key": "compat",
		},
	}

	// When metadata has no session identity, ProviderSessionUUID returns empty and the header must not be set.
	if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4-flash",
		Payload: []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       false,
	}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotHeader != "" {
		t.Fatalf("expected no x-opencode-session header without session metadata, got %q", gotHeader)
	}
}
