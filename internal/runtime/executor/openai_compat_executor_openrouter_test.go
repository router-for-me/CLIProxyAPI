package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const testOpenRouterBaseURL = "https://openrouter.ai/api/v1"

func TestOpenAICompatExecutorOpenRouterCacheAndSession(t *testing.T) {
	var gotBody []byte
	var gotSession string
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		gotBody, _ = io.ReadAll(r.Body)
		gotSession = r.Header.Get("x-session-id")
		body := `{"id":"c1","object":"chat.completion","model":"anthropic/claude-haiku-5.5","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	metadata := map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "conversation-1"}
	run := func(baseURL string) {
		gotBody, gotSession = nil, ""
		executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "router"}}})
		auth := &cliproxyauth.Auth{
			Provider:   "openai-compatibility",
			Attributes: map[string]string{"base_url": baseURL, "api_key": "test", "compat_name": "router", "provider_key": "router"},
		}
		payload := []byte(`{"model":"anthropic/claude-haiku-5.5","max_tokens":16,
			"system":[{"type":"text","text":"You are helpful.","cache_control":{"type":"ephemeral","ttl":"1h"}}],
			"messages":[{"role":"user","content":[{"type":"text","text":"Reply OK","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`)
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
		_, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{Model: "anthropic/claude-haiku-5.5", Payload: payload},
			cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude"), OriginalRequest: payload,
				Metadata: metadata})
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}
	}
	countOneHour := func() int {
		n := 0
		for _, msg := range ojson.GetBytes(gotBody, "messages").Array() {
			for _, part := range msg.Get("content").Array() {
				if part.Get("cache_control.ttl").String() == "1h" {
					n++
				}
			}
		}
		return n
	}

	run(testOpenRouterBaseURL)
	if got := countOneHour(); got != 2 {
		t.Fatalf("OpenRouter: want 2 one-hour breakpoints (system + user), got %d: %s", got, gotBody)
	}
	if gotSession == "" {
		t.Fatalf("OpenRouter: missing x-session-id header")
	}
	first := gotSession
	run(testOpenRouterBaseURL)
	if gotSession != first {
		t.Fatalf("x-session-id not stable for one conversation: %q vs %q", first, gotSession)
	}

	// A client that names its own session (e.g. pi) has only a canonical session.
	metadata = map[string]any{cliproxyexecutor.CanonicalSessionIDMetadataKey: "pi-session-42"}
	run(testOpenRouterBaseURL)
	if gotSession == "" || gotSession == first {
		t.Fatalf("canonical session: x-session-id = %q, want a stable id of its own", gotSession)
	}
	canonicalFirst := gotSession
	run(testOpenRouterBaseURL)
	if gotSession != canonicalFirst {
		t.Fatalf("canonical session: x-session-id not stable: %q vs %q", canonicalFirst, gotSession)
	}
	metadata = map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "conversation-1"}

	run("https://api.example.com/v1")
	if got := countOneHour(); got != 0 || strings.Contains(string(gotBody), "cache_control") {
		t.Fatalf("other providers must stay unchanged: %s", gotBody)
	}
	if gotSession != "" {
		t.Fatalf("other providers must not get x-session-id, got %q", gotSession)
	}
}
