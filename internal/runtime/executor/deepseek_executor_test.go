package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func TestDeepSeekExecutorAppliesV4Thinking(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %s, want /chat/completions", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL, "api_key": "ds-test-key"}}
	exec := NewDeepSeekExecutor(&config.Config{})
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4-pro(max)",
		Payload: []byte(`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if gotAuth != "Bearer ds-test-key" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	thinkingObj, ok := gotBody["thinking"].(map[string]any)
	if !ok || thinkingObj["type"] != "enabled" {
		t.Fatalf("thinking object = %#v, want enabled", gotBody["thinking"])
	}
	if got := gotBody["reasoning_effort"]; got != "max" {
		t.Fatalf("reasoning_effort = %#v, want max; body=%#v", got, gotBody)
	}
}

func TestDeepSeekExecutorDisablesThinking(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL, "api_key": "ds-test-key"}}
	exec := NewDeepSeekExecutor(&config.Config{})
	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek-v4-flash(none)",
		Payload: []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	thinkingObj, ok := gotBody["thinking"].(map[string]any)
	if !ok || thinkingObj["type"] != "disabled" {
		t.Fatalf("thinking object = %#v, want disabled", gotBody["thinking"])
	}
	if _, exists := gotBody["reasoning_effort"]; exists {
		t.Fatalf("reasoning_effort should be removed when disabled; body=%#v", gotBody)
	}
}

func TestDeepSeekPrepareRequestUsesDefaultBaseURLCredential(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://example.test/models", nil)
	exec := NewDeepSeekExecutor(&config.Config{})
	err := exec.PrepareRequest(req, &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "ds-key"}})
	if err != nil {
		t.Fatalf("PrepareRequest returned error: %v", err)
	}
	if got := req.Header.Get("Authorization"); !strings.EqualFold(got, "Bearer ds-key") {
		t.Fatalf("Authorization = %q", got)
	}
}
