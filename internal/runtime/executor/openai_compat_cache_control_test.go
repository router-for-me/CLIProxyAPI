package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func cacheControlTestDoc(t *testing.T, payload string, compat *config.OpenAICompatibility, from string, original string) []byte {
	t.Helper()
	doc := ojson.NewDoc([]byte(payload))
	applyOpenAICompatCacheControlDoc(compat, sdktranslator.FromString(from), cliproxyexecutor.Request{Payload: []byte(original)}, cliproxyexecutor.Options{}, doc)
	return doc.Bytes()
}

func TestOpenAICompatCacheControlMarksSystemAndLatestTurn(t *testing.T) {
	compat := &config.OpenAICompatibility{SupportCacheControl: true}
	out := cacheControlTestDoc(t, `{"messages":[
		{"role":"system","content":"You are helpful."},
		{"role":"user","content":"first"},
		{"role":"assistant","content":"ok","tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"file contents"}]}`, compat, "openai", "")

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
	compat := &config.OpenAICompatibility{SupportCacheControl: true}
	out := cacheControlTestDoc(t, `{"messages":[{"role":"user","content":[
		{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:x"}},{"type":"text","text":"  "}]}]}`, compat, "openai", "")
	if !ojson.GetBytes(out, "messages.0.content.1.cache_control").Exists() {
		t.Fatalf("expected the last non-empty part (image) to be marked: %s", out)
	}
	if ojson.GetBytes(out, "messages.0.content.2.cache_control").Exists() || ojson.GetBytes(out, "messages.0.content.0.cache_control").Exists() {
		t.Fatalf("only one part should be marked: %s", out)
	}
}

func TestOpenAICompatCacheControlLeavesRequestsAlone(t *testing.T) {
	plain := `{"messages":[{"role":"user","content":"hi"}]}`
	if out := cacheControlTestDoc(t, plain, &config.OpenAICompatibility{}, "openai", ""); string(out) != plain {
		t.Fatalf("provider without opt-in changed: %s", out)
	}
	if out := cacheControlTestDoc(t, plain, nil, "openai", ""); string(out) != plain {
		t.Fatalf("nil compat changed: %s", out)
	}
	marked := `{"messages":[{"role":"system","content":"s"},{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`
	if out := cacheControlTestDoc(t, marked, &config.OpenAICompatibility{SupportCacheControl: true}, "openai", ""); string(out) != marked {
		t.Fatalf("request with its own markers changed: %s", out)
	}
	responses := `{"input":[{"role":"user","content":"hi"}]}`
	if out := cacheControlTestDoc(t, responses, &config.OpenAICompatibility{SupportCacheControl: true}, "openai-response", ""); string(out) != responses {
		t.Fatalf("non chat-completions body changed: %s", out)
	}
}

func TestOpenAICompatCacheControlKeepsClaudeOneHourRequest(t *testing.T) {
	compat := &config.OpenAICompatibility{SupportCacheControl: true}
	claude := `{"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":"hi"}]}`
	out := cacheControlTestDoc(t, `{"messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`, compat, "claude", claude)
	for _, i := range []string{"0", "1"} {
		if got := ojson.GetBytes(out, "messages."+i+".content.0.cache_control.ttl").String(); got != "1h" {
			t.Fatalf("message %s: ttl = %q, want 1h: %s", i, got, out)
		}
	}
	// a 1h request in another source format is not inferred
	out = cacheControlTestDoc(t, `{"messages":[{"role":"user","content":"hi"}]}`, compat, "openai", claude)
	if ojson.GetBytes(out, "messages.0.content.0.cache_control.ttl").Exists() {
		t.Fatalf("ttl applied for a non-Claude source: %s", out)
	}
}

func TestOpenAICompatExecutorAddsCacheControlForClaudeRequests(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"anthropic/claude-haiku-5.5","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{Name: "router", SupportCacheControl: true}},
	})
	auth := &cliproxyauth.Auth{
		Provider:   "openai-compatibility",
		Attributes: map[string]string{"base_url": server.URL + "/v1", "api_key": "test", "compat_name": "router", "provider_key": "router"},
	}
	payload := []byte(`{"model":"anthropic/claude-haiku-5.5","max_tokens":16,
		"system":[{"type":"text","text":"You are helpful.","cache_control":{"type":"ephemeral","ttl":"1h"}}],
		"messages":[{"role":"user","content":[{"type":"text","text":"Reply OK","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "anthropic/claude-haiku-5.5", Payload: payload},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude"), OriginalRequest: payload})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	marked := 0
	for _, msg := range ojson.GetBytes(gotBody, "messages").Array() {
		for _, part := range msg.Get("content").Array() {
			if part.Get("cache_control.ttl").String() == "1h" {
				marked++
			}
		}
	}
	if marked != 2 {
		t.Fatalf("want 2 one-hour breakpoints (system + user), got %d: %s", marked, gotBody)
	}
}
