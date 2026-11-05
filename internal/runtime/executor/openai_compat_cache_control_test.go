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

func cacheControlTestDoc(t *testing.T, payload string, baseURL string, from string, original string) []byte {
	t.Helper()
	doc := ojson.NewDoc([]byte(payload))
	applyOpenRouterCacheControlDoc(baseURL, sdktranslator.FromString(from), cliproxyexecutor.Request{Payload: []byte(original)}, cliproxyexecutor.Options{}, doc)
	return doc.Bytes()
}

func TestOpenAICompatCacheControlMarksSystemAndLatestTurn(t *testing.T) {
	out := cacheControlTestDoc(t, `{"messages":[
		{"role":"system","content":"You are helpful."},
		{"role":"user","content":"first"},
		{"role":"assistant","content":"ok","tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"file contents"}]}`, testOpenRouterBaseURL, "openai", "")

	if got := ojson.GetBytes(out, "messages.0.content.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("system not marked: %s", out)
	}
	if got := ojson.GetBytes(out, "messages.0.content.0.text").String(); got != "You are helpful." {
		t.Fatalf("system text lost: %s", out)
	}
	if ojson.GetBytes(out, "messages.1.content.0.cache_control").Exists() || ojson.GetBytes(out, "messages.1.content").Type != ojson.String {
		t.Fatalf("earlier user message changed: %s", out)
	}
	if got := ojson.GetBytes(out, "messages.3.content.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("latest tool message not marked: %s", out)
	}
	if ojson.GetBytes(out, "messages.3.content.0.cache_control.ttl").Exists() {
		t.Fatalf("unexpected ttl without a client request: %s", out)
	}
}

func TestOpenAICompatCacheControlMarksLastTextPart(t *testing.T) {
	out := cacheControlTestDoc(t, `{"messages":[{"role":"user","content":[
		{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:x"}},{"type":"text","text":"  "}]}]}`, testOpenRouterBaseURL, "openai", "")
	if !ojson.GetBytes(out, "messages.0.content.1.cache_control").Exists() {
		t.Fatalf("expected the last non-empty part (image) to be marked: %s", out)
	}
	if ojson.GetBytes(out, "messages.0.content.2.cache_control").Exists() || ojson.GetBytes(out, "messages.0.content.0.cache_control").Exists() {
		t.Fatalf("only one part should be marked: %s", out)
	}
}

func TestOpenAICompatCacheControlLeavesRequestsAlone(t *testing.T) {
	plain := `{"messages":[{"role":"user","content":"hi"}]}`
	if out := cacheControlTestDoc(t, plain, "https://api.example.com/v1", "openai", ""); string(out) != plain {
		t.Fatalf("non-OpenRouter provider changed: %s", out)
	}
	if out := cacheControlTestDoc(t, plain, "", "openai", ""); string(out) != plain {
		t.Fatalf("empty base URL changed: %s", out)
	}
	marked := `{"messages":[{"role":"system","content":"s"},{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`
	if out := cacheControlTestDoc(t, marked, testOpenRouterBaseURL, "openai", ""); string(out) != marked {
		t.Fatalf("request with its own markers changed: %s", out)
	}
	responses := `{"input":[{"role":"user","content":"hi"}]}`
	if out := cacheControlTestDoc(t, responses, testOpenRouterBaseURL, "openai-response", ""); string(out) != responses {
		t.Fatalf("non chat-completions body changed: %s", out)
	}
}

func TestOpenAICompatCacheControlKeepsClaudeOneHourRequest(t *testing.T) {
	claude := `{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":"hi"}]}`
	out := cacheControlTestDoc(t, `{"messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`, testOpenRouterBaseURL, "claude", claude)
	for _, i := range []string{"0", "1"} {
		if got := ojson.GetBytes(out, "messages."+i+".content.0.cache_control.ttl").String(); got != "1h" {
			t.Fatalf("message %s: ttl = %q, want 1h: %s", i, got, out)
		}
	}
	// a 1h request in another source format is not inferred
	out = cacheControlTestDoc(t, `{"messages":[{"role":"user","content":"hi"}]}`, testOpenRouterBaseURL, "openai", claude)
	if ojson.GetBytes(out, "messages.0.content.0.cache_control.ttl").Exists() {
		t.Fatalf("ttl applied for a non-Claude source: %s", out)
	}
}

func TestOpenAICompatExecutorOpenRouterCacheAndSession(t *testing.T) {
	var gotBody []byte
	var gotSession string
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		gotBody, _ = io.ReadAll(r.Body)
		gotSession = r.Header.Get("x-session-id")
		body := `{"id":"c1","object":"chat.completion","model":"anthropic/claude-haiku-5.5","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
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
				Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "conversation-1"}})
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

	run("https://api.example.com/v1")
	if got := countOneHour(); got != 0 || strings.Contains(string(gotBody), "cache_control") {
		t.Fatalf("other providers must stay unchanged: %s", gotBody)
	}
	if gotSession != "" {
		t.Fatalf("other providers must not get x-session-id, got %q", gotSession)
	}
}
