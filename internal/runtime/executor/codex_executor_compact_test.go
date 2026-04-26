package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorCompactAddsDefaultInstructions(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{
			name:    "missing instructions",
			payload: `{"model":"gpt-5.4","input":"hello"}`,
		},
		{
			name:    "null instructions",
			payload: `{"model":"gpt-5.4","instructions":null,"input":"hello"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			var gotBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				body, _ := io.ReadAll(r.Body)
				gotBody = body
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
			}))
			defer server.Close()

			executor := NewCodexExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"base_url": server.URL,
				"api_key":  "test",
			}}

			resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gpt-5.4",
				Payload: []byte(tc.payload),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai-response"),
				Alt:          "responses/compact",
				Stream:       false,
			})
			if err != nil {
				t.Fatalf("Execute error: %v", err)
			}
			if gotPath != "/responses/compact" {
				t.Fatalf("path = %q, want %q", gotPath, "/responses/compact")
			}
			if gjson.GetBytes(gotBody, "instructions").Exists() {
				t.Fatalf("instructions should be omitted when empty, got %s", string(gotBody))
			}
			if string(resp.Payload) != `{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}` {
				t.Fatalf("payload = %s", string(resp.Payload))
			}
		})
	}
}

func TestCodexExecutorCompactUsesCompactOnlyBodyFields(t *testing.T) {
	resetCodexWindowStateStore()
	var gotBody []byte
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"output":[]}`))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(`{
			"model":"gpt-5.4",
			"input":"hello",
			"store":true,
			"stream":true,
			"tool_choice":"required",
			"include":["reasoning.encrypted_content"],
			"prompt_cache_key":"pc-1",
			"client_metadata":{"x-codex-installation-id":"install-1"}
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	for _, field := range []string{"store", "stream", "tool_choice", "include", "prompt_cache_key", "client_metadata"} {
		if gjson.GetBytes(gotBody, field).Exists() {
			t.Fatalf("%s should not be sent to responses/compact: %s", field, gotBody)
		}
	}
	if got := gjson.GetBytes(gotBody, "tools").IsArray(); !got {
		t.Fatalf("tools should default to an empty array for compact: %s", gotBody)
	}
	if got := gjson.GetBytes(gotBody, "parallel_tool_calls").Bool(); !got {
		t.Fatalf("parallel_tool_calls = false, want true; body=%s", gotBody)
	}
	if got := gotHeaders.Get(codexHeaderTurnMetadata); got != "" {
		t.Fatalf("%s should not be sent by default to responses/compact: %q", codexHeaderTurnMetadata, got)
	}
	if got := gotHeaders.Get(codexHeaderTurnState); got != "" {
		t.Fatalf("%s should not be sent by default to responses/compact: %q", codexHeaderTurnState, got)
	}
	if got := gotHeaders.Get("X-Client-Request-Id"); got != "" {
		t.Fatalf("X-Client-Request-Id should not be sent by default to responses/compact: %q", got)
	}
	if got := gotHeaders.Get(codexHeaderSessionID); got == "" {
		t.Fatalf("%s should be present on responses/compact", codexHeaderSessionID)
	}
	if got := gotHeaders.Get(codexHeaderInstallationID); got == "" {
		t.Fatalf("%s should be present on responses/compact", codexHeaderInstallationID)
	}
}

func TestCodexExecutorCompactAdvancesWindowGenerationForSession(t *testing.T) {
	resetCodexWindowStateStore()

	firstReq, err := http.NewRequestWithContext(
		contextWithGinHeaders(map[string]string{codexHeaderSessionID: "conv-1"}),
		http.MethodPost,
		"https://example.com/responses",
		nil,
	)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error: %v", err)
	}
	applyCodexHeaders(firstReq, nil, "oauth-token", true, nil)
	if got := firstReq.Header.Get(codexHeaderWindowID); got != "conv-1:0" {
		t.Fatalf("initial %s = %q, want %q", codexHeaderWindowID, got, "conv-1:0")
	}

	var compactWindowID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		compactWindowID = r.Header.Get(codexHeaderWindowID)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}
	ctx := contextWithGinHeaders(map[string]string{
		codexHeaderSessionID: "conv-1",
	})

	request := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`),
	}

	if _, err := executor.Execute(ctx, auth, request, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
		Stream:       false,
	}); err != nil {
		t.Fatalf("compact Execute error: %v", err)
	}

	if compactWindowID != "conv-1:0" {
		t.Fatalf("compact %s = %q, want %q", codexHeaderWindowID, compactWindowID, "conv-1:0")
	}

	secondReq, err := http.NewRequestWithContext(
		contextWithGinHeaders(map[string]string{codexHeaderSessionID: "conv-1"}),
		http.MethodPost,
		"https://example.com/responses",
		nil,
	)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error: %v", err)
	}
	applyCodexHeaders(secondReq, nil, "oauth-token", true, nil)
	if got := secondReq.Header.Get(codexHeaderWindowID); got != "conv-1:1" {
		t.Fatalf("post-compact %s = %q, want %q", codexHeaderWindowID, got, "conv-1:1")
	}
}

func TestCodexExecutorCompactUsesTurnMetadataSessionIDWhenHeaderMissing(t *testing.T) {
	resetCodexWindowStateStore()

	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}
	ctx := contextWithGinHeaders(map[string]string{
		codexHeaderTurnMetadata: `{"session_id":"turn-session-1","turn_id":"turn-1","sandbox":"none"}`,
	})

	_, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("compact Execute error: %v", err)
	}

	if got := gotHeaders.Get(codexHeaderSessionID); got != "turn-session-1" {
		t.Fatalf("%s = %q, want %q", codexHeaderSessionID, got, "turn-session-1")
	}
	if got := gotHeaders.Get(codexHeaderWindowID); got != "turn-session-1:0" {
		t.Fatalf("%s = %q, want %q", codexHeaderWindowID, got, "turn-session-1:0")
	}

	nextReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"https://example.com/responses",
		nil,
	)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error: %v", err)
	}
	applyCodexHeaders(nextReq, nil, "oauth-token", true, nil)
	if got := nextReq.Header.Get(codexHeaderSessionID); got != "turn-session-1" {
		t.Fatalf("next %s = %q, want %q", codexHeaderSessionID, got, "turn-session-1")
	}
	if got := nextReq.Header.Get(codexHeaderWindowID); got != "turn-session-1:1" {
		t.Fatalf("next %s = %q, want %q", codexHeaderWindowID, got, "turn-session-1:1")
	}
}
