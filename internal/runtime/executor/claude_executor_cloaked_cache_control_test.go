package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestClaudeExecutorExecute_RewritesCloakedCacheControl(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":    "key-123",
		"base_url":   server.URL,
		"cloak_mode": "always",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: cloakedCacheControlRewritePayload(),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	assertCloakedCacheControlRewrite(t, seenBody)
}

func TestClaudeExecutorExecuteStream_RewritesCloakedCacheControl(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":    "key-123",
		"base_url":   server.URL,
		"cloak_mode": "always",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: cloakedCacheControlRewritePayload(),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
		}
	}

	assertCloakedCacheControlRewrite(t, seenBody)
}

func cloakedCacheControlRewritePayload() []byte {
	return []byte(`{
		"system": [
			{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.63.abcde; cc_entrypoint=cli; cch=00000;","cache_control":{"type":"ephemeral"}},
			{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude.","cache_control":{"type":"ephemeral","ttl":"1h"}},
			{"type":"text","text":"Follow the user's instructions closely."}
		],
		"tools": [
			{"name":"t1","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}},
			{"name":"t2","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral","ttl":"1h"}}
		],
		"messages": [
			{"role":"user","content":[{"type":"text","text":"first","cache_control":{"type":"ephemeral"}}]},
			{"role":"user","content":[{"type":"text","text":"second","cache_control":{"type":"ephemeral"}},{"type":"text","text":"third"}]}
		]
	}`)
}

func assertCloakedCacheControlRewrite(t *testing.T, body []byte) {
	t.Helper()

	if len(body) == 0 {
		t.Fatal("expected upstream request body to be captured")
	}
	if !strings.HasPrefix(gjson.GetBytes(body, "system.0.text").String(), "x-anthropic-billing-header:") {
		t.Fatalf("system.0.text = %q, want cloaked billing header", gjson.GetBytes(body, "system.0.text").String())
	}

	system := gjson.GetBytes(body, "system").Array()
	if len(system) < 3 {
		t.Fatalf("expected cloaked system blocks, got %d", len(system))
	}
	if system[0].Get("cache_control").Exists() {
		t.Fatalf("system.0.cache_control should be stripped for cloaked payloads")
	}
	for i := 1; i < len(system); i++ {
		if system[i].Get("cache_control.type").String() != "ephemeral" {
			t.Fatalf("system.%d.cache_control.type = %q, want %q", i, system[i].Get("cache_control.type").String(), "ephemeral")
		}
	}

	if gjson.GetBytes(body, "tools.0.cache_control").Exists() {
		t.Fatalf("tools.0.cache_control should be stripped during cloaked rewrite")
	}
	if gjson.GetBytes(body, "tools.1.cache_control.type").String() != "ephemeral" {
		t.Fatalf("tools.1.cache_control.type = %q, want %q", gjson.GetBytes(body, "tools.1.cache_control.type").String(), "ephemeral")
	}

	if gjson.GetBytes(body, "messages.0.content.0.cache_control").Exists() {
		t.Fatalf("messages.0.content.0.cache_control should be stripped during cloaked rewrite")
	}
	if gjson.GetBytes(body, "messages.1.content.0.cache_control").Exists() {
		t.Fatalf("messages.1.content.0.cache_control should be stripped during cloaked rewrite")
	}
	if gjson.GetBytes(body, "messages.1.content.1.cache_control.type").String() != "ephemeral" {
		t.Fatalf("messages.1.content.1.cache_control.type = %q, want %q", gjson.GetBytes(body, "messages.1.content.1.cache_control.type").String(), "ephemeral")
	}
	if got := countMessageCacheControls(body); got != 1 {
		t.Fatalf("message cache_control count = %d, want 1", got)
	}
	if got := countCacheControls(body); got != 4 {
		t.Fatalf("cache_control count = %d, want 4", got)
	}
}

func countMessageCacheControls(payload []byte) int {
	count := 0
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return count
	}

	messages.ForEach(func(_, msg gjson.Result) bool {
		content := msg.Get("content")
		if !content.IsArray() {
			return true
		}
		content.ForEach(func(_, item gjson.Result) bool {
			if item.Get("cache_control").Exists() {
				count++
			}
			return true
		})
		return true
	})

	return count
}
