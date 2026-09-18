package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/zcode"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// stubZCodeRouter is a spy router that never touches the network: Refresh is a
// no-op and BaseURL returns a fixed pinned base (used to prove the request was
// routed to the ultra gateway / test server).
type stubZCodeRouter struct {
	base string
}

func (s *stubZCodeRouter) Refresh(context.Context) error { return nil }
func (s *stubZCodeRouter) BaseURL(_ *cliproxyauth.Auth, _ string) string {
	return s.base
}

func TestZCodeExecutor_RequestToFormat(t *testing.T) {
	e := NewZCodeExecutor(nil)
	if f := e.RequestToFormat(cliproxyexecutor.Request{}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}); f != sdktranslator.FormatClaude {
		t.Fatalf("expected claude format, got %v", f)
	}
}

func TestZCodeExecutor_SendsIdentityHeaders(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"glm-5.3","content":[{"type":"text","text":"hi"}]}`))
	}))
	defer srv.Close()

	auth := &cliproxyauth.Auth{
		ID: "zcode.json", Provider: "zcode",
		Attributes: map[string]string{
			"api_key":                  "k.s",
			"base_url":                 srv.URL,
			"header:User-Agent":        "ZCode/3.12.0",
			"header:X-ZCode-Agent":     "glm",
			"header:X-Title":           "ZCode",
			"header:X-Client-Lang":     "zh-CN",
			"header:X-Client-Timezone": "Asia/Shanghai",
		},
	}
	reqBody, _ := json.Marshal(map[string]any{
		"model":      "glm-5.3",
		"max_tokens": 1024,
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
	})
	req := cliproxyexecutor.Request{Model: "glm-5.3", Payload: reqBody, Format: sdktranslator.FormatClaude}

	e := NewZCodeExecutor(nil)
	// Hermetic: a stub router whose Refresh never touches the network (the auth
	// pins the test server base, so BaseURL is simply never used).
	e.routes = &stubZCodeRouter{base: srv.URL}
	resp, err := e.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("expected non-empty response payload")
	}
	if gotHeaders.Get("User-Agent") != "ZCode/3.12.0" {
		t.Fatalf("User-Agent = %q", gotHeaders.Get("User-Agent"))
	}
	if gotHeaders.Get("X-ZCode-Agent") != "glm" {
		t.Fatalf("X-ZCode-Agent = %q", gotHeaders.Get("X-ZCode-Agent"))
	}
	if gotHeaders.Get("X-Client-Lang") != "zh-CN" {
		t.Fatalf("X-Client-Lang = %q", gotHeaders.Get("X-Client-Lang"))
	}
	if gotHeaders.Get("X-Client-Timezone") != "Asia/Shanghai" {
		t.Fatalf("X-Client-Timezone = %q", gotHeaders.Get("X-Client-Timezone"))
	}
}

func TestZCodeExecutor_ExecuteStream_RoutesToUltraAndSendsIdentityHeaders(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"glm-5.3\"}}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	// Pin the canonical default base so the executor's routing logic runs, and
	// inject a stub router that maps it to the test server (proving the routed
	// ultra-gateway base is applied on the streaming path).
	auth := &cliproxyauth.Auth{
		ID: "zcode.json", Provider: "zcode",
		Attributes: map[string]string{
			"api_key":                  "k.s",
			"base_url":                 "https://api.z.ai/api/anthropic",
			"header:User-Agent":        "ZCode/3.12.0",
			"header:X-ZCode-Agent":     "glm",
			"header:X-Client-Lang":     "zh-CN",
			"header:X-Client-Timezone": "Asia/Shanghai",
		},
	}
	reqBody, _ := json.Marshal(map[string]any{
		"model":      "glm-5.3",
		"max_tokens": 1024,
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
	})
	req := cliproxyexecutor.Request{Model: "glm-5.3", Payload: reqBody, Format: sdktranslator.FormatClaude}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}

	e := NewZCodeExecutor(nil)
	e.routes = &stubZCodeRouter{base: srv.URL}

	res, err := e.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("execute stream: %v", err)
	}
	var chunks []string
	for c := range res.Chunks {
		if c.Err != nil {
			t.Fatalf("stream chunk error: %v", c.Err)
		}
		if len(c.Payload) > 0 {
			chunks = append(chunks, string(c.Payload))
		}
	}
	joined := strings.Join(chunks, "")
	if !strings.Contains(joined, "message_start") {
		t.Fatalf("expected streamed message_start, got: %q", joined)
	}
	if gotHeaders.Get("User-Agent") != "ZCode/3.12.0" {
		t.Fatalf("User-Agent = %q", gotHeaders.Get("User-Agent"))
	}
	if gotHeaders.Get("X-ZCode-Agent") != "glm" {
		t.Fatalf("X-ZCode-Agent = %q", gotHeaders.Get("X-ZCode-Agent"))
	}
	if gotHeaders.Get("X-Client-Lang") != "zh-CN" {
		t.Fatalf("X-Client-Lang = %q", gotHeaders.Get("X-Client-Lang"))
	}
	if gotHeaders.Get("X-Client-Timezone") != "Asia/Shanghai" {
		t.Fatalf("X-Client-Timezone = %q", gotHeaders.Get("X-Client-Timezone"))
	}
}

// TestZCodeExecutor_ReloadedCredentialSendsFullKey covers the production restart
// path end to end: a credential is persisted to an auth file, reloaded by the
// file store (which does NOT persist Attributes), and executed. The api_key must
// be reconstructed as api_key.secret from metadata and the identity headers must
// be rebuilt from the persisted "headers" map.
func TestZCodeExecutor_ReloadedCredentialSendsFullKey(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"glm-5.3","content":[{"type":"text","text":"hi"}]}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(dir)

	original := sdkAuth.BuildZCodeAuth(
		&zcode.Credential{APIKey: "k-1", Secret: "s-1"}, "jwt", "u-1", "bigmodel")
	if _, err := store.Save(context.Background(), original); err != nil {
		t.Fatalf("save: %v", err)
	}

	reloadedAuths, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(reloadedAuths) != 1 {
		t.Fatalf("expected 1 reloaded auth, got %d", len(reloadedAuths))
	}
	reloaded := reloadedAuths[0]

	// Guard: prove we are exercising the metadata reconstruction path, not the
	// in-memory attributes (which are not persisted to the auth file).
	if reloaded.Attributes["api_key"] != "" || reloaded.Attributes["base_url"] != "" {
		t.Fatalf("reloaded auth unexpectedly carries api_key/base_url attributes: %+v", reloaded.Attributes)
	}
	if got, _ := reloaded.Metadata["base_url"].(string); got != "https://open.bigmodel.cn/api/anthropic" {
		t.Fatalf("reloaded base_url = %q", got)
	}
	if reloaded.Provider != "zcode" {
		t.Fatalf("reloaded provider = %q", reloaded.Provider)
	}

	// Keep the test hermetic: point the reloaded credential at the test server.
	// The api_key/secret and headers under test are untouched.
	reloaded.Metadata["base_url"] = srv.URL

	reqBody, _ := json.Marshal(map[string]any{
		"model":      "glm-5.3",
		"max_tokens": 128,
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
	})
	req := cliproxyexecutor.Request{Model: "glm-5.3", Payload: reqBody, Format: sdktranslator.FormatClaude}

	e := NewZCodeExecutor(nil)
	e.routes = &stubZCodeRouter{base: srv.URL}
	if _, err := e.Execute(context.Background(), reloaded, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}); err != nil {
		t.Fatalf("execute: %v", err)
	}

	if got := gotHeaders.Get("Authorization"); got != "Bearer k-1.s-1" {
		t.Fatalf("Authorization = %q, want Bearer k-1.s-1", got)
	}
	if got := gotHeaders.Get("User-Agent"); got != "ZCode/3.12.0" {
		t.Fatalf("User-Agent = %q (identity headers not reconstructed)", got)
	}
	if got := gotHeaders.Get("X-ZCode-Agent"); got != "glm" {
		t.Fatalf("X-ZCode-Agent = %q (identity headers not reconstructed)", got)
	}
}
