package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func writeClaudeSSEMockResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-3-5-sonnet\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
	_, _ = fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n")
	_, _ = fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
	_, _ = fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
}

func TestClaudeExecutor_PromptCacheOptionsMode_Explicit_SuppressesAutoCacheControl(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		writeClaudeSSEMockResponse(w)
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	originalReq := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"prompt_cache_options": {
			"mode": "explicit"
		},
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "Hello"}
		]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: originalReq,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: originalReq,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if count := countCacheControls(seenBody); count != 0 {
		t.Fatalf("expected 0 cache_control blocks with mode: explicit and no client breakpoints, got %d: %s", count, string(seenBody))
	}
	if gjson.GetBytes(seenBody, "prompt_cache_options").Exists() {
		t.Fatalf("prompt_cache_options should not be forwarded upstream: %s", string(seenBody))
	}
}

func TestClaudeExecutor_PromptCacheOptionsMode_Explicit_PreservesClientBreakpoints(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		writeClaudeSSEMockResponse(w)
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	originalReq := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"prompt_cache_options": {
			"mode": "explicit"
		},
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": [
				{"type": "text", "text": "Cached part", "cache_control": {"type": "ephemeral"}}
			]},
			{"role": "user", "content": "Uncached part"}
		]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: originalReq,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: originalReq,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if count := countCacheControls(seenBody); count != 1 {
		t.Fatalf("expected exactly 1 cache_control block (the client-specified one), got %d: %s", count, string(seenBody))
	}
	if !gjson.GetBytes(seenBody, "messages.0.content.0.cache_control").Exists() {
		t.Fatalf("client cache_control was not preserved: %s", string(seenBody))
	}
	if gjson.GetBytes(seenBody, "system.0.cache_control").Exists() {
		t.Fatalf("system prompt unexpectedly received automatic cache_control: %s", string(seenBody))
	}
	if gjson.GetBytes(seenBody, "messages.1.content.0.cache_control").Exists() {
		t.Fatalf("second message unexpectedly received automatic cache_control: %s", string(seenBody))
	}
}

func TestClaudeExecutor_PromptCacheOptionsMode_Stream_Explicit_SuppressesAutoCacheControl(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		writeClaudeSSEMockResponse(w)
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	originalReq := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"stream": true,
		"prompt_cache_options": {
			"mode": "explicit"
		},
		"messages": [
			{"role": "user", "content": "Stream query"}
		]
	}`)

	streamRes, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: originalReq,
	}, cliproxyexecutor.Options{
		Stream:          true,
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: originalReq,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for range streamRes.Chunks {
	}

	if count := countCacheControls(seenBody); count != 0 {
		t.Fatalf("expected 0 cache_control blocks in stream with mode: explicit, got %d: %s", count, string(seenBody))
	}
	if gjson.GetBytes(seenBody, "prompt_cache_options").Exists() {
		t.Fatalf("prompt_cache_options should not be forwarded upstream: %s", string(seenBody))
	}
}

func TestClaudeExecutor_PromptCacheOptionsMode_ImplicitOrMissing_PreservesAutoCacheControl(t *testing.T) {
	for _, tc := range []struct {
		name        string
		originalReq []byte
	}{
		{
			name: "missing prompt_cache_options",
			originalReq: []byte(`{
				"model": "claude-3-5-sonnet-20241022",
				"messages": [
					{"role": "system", "content": "You are helpful."},
					{"role": "user", "content": "Hello"}
				]
			}`),
		},
		{
			name: "mode implicit",
			originalReq: []byte(`{
				"model": "claude-3-5-sonnet-20241022",
				"prompt_cache_options": {
					"mode": "implicit"
				},
				"messages": [
					{"role": "system", "content": "You are helpful."},
					{"role": "user", "content": "Hello"}
				]
			}`),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seenBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				seenBody = bytes.Clone(body)
				writeClaudeSSEMockResponse(w)
			}))
			defer server.Close()

			executor := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"api_key":  "key-123",
				"base_url": server.URL,
			}}

			_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "claude-3-5-sonnet-20241022",
				Payload: tc.originalReq,
			}, cliproxyexecutor.Options{
				SourceFormat:    sdktranslator.FromString("openai"),
				OriginalRequest: tc.originalReq,
			})
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}

			// In normal mode without client markers, system and message should receive auto-injected cache_control.
			if !gjson.GetBytes(seenBody, "system.0.cache_control").Exists() {
				t.Fatalf("expected system cache_control in implicit mode: %s", string(seenBody))
			}
			if !gjson.GetBytes(seenBody, "messages.0.content.0.cache_control").Exists() {
				t.Fatalf("expected message cache_control in implicit mode: %s", string(seenBody))
			}
		})
	}
}

func TestClaudeExecutor_PromptCacheOptionsMode_Explicit_ToolsFallbackSuppressed(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		writeClaudeSSEMockResponse(w)
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	originalReq := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"prompt_cache_options": {
			"mode": "explicit"
		},
		"tools": [
			{"type": "function", "function": {"name": "tool1", "description": "t1", "parameters": {"type": "object"}}},
			{"type": "function", "function": {"name": "tool2", "description": "t2", "parameters": {"type": "object"}}}
		],
		"messages": [
			{"role": "user", "content": "Hello without system"}
		]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: originalReq,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: originalReq,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if count := countCacheControls(seenBody); count != 0 {
		t.Fatalf("expected 0 cache_control blocks with mode: explicit when only tools are present, got %d: %s", count, string(seenBody))
	}
}

func TestClaudeExecutor_PromptCacheOptionsMode_Explicit_ClientToolBreakpointPreserved(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		writeClaudeSSEMockResponse(w)
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	originalReq := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"prompt_cache_options": {
			"mode": "explicit"
		},
		"tools": [
			{"type": "function", "cache_control": {"type": "ephemeral"}, "function": {"name": "tool1", "description": "t1", "parameters": {"type": "object"}}},
			{"type": "function", "function": {"name": "tool2", "description": "t2", "parameters": {"type": "object"}}}
		],
		"messages": [
			{"role": "user", "content": "Hello"}
		]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: originalReq,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: originalReq,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if count := countCacheControls(seenBody); count != 1 {
		t.Fatalf("expected 1 cache_control block on client-specified tool, got %d: %s", count, string(seenBody))
	}
	if !gjson.GetBytes(seenBody, "tools.0.cache_control").Exists() {
		t.Fatalf("tools.0 should have client-specified cache_control: %s", string(seenBody))
	}
	if gjson.GetBytes(seenBody, "tools.1.cache_control").Exists() {
		t.Fatalf("tools.1 should NOT have cache_control: %s", string(seenBody))
	}
	if gjson.GetBytes(seenBody, "messages.0.content.0.cache_control").Exists() {
		t.Fatalf("message should NOT receive automatic cache_control: %s", string(seenBody))
	}
}

func TestClaudeExecutor_PromptCacheOptionsMode_Explicit_Cloaked_SuppressesAllAutoCacheControl(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		writeClaudeSSEMockResponse(w)
	}))
	defer server.Close()

	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey: "key-123",
			Cloak:  &config.CloakConfig{Mode: "always"},
		}},
	}
	executor := NewClaudeExecutor(cfg)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	originalReq := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"prompt_cache_options": {
			"mode": "explicit"
		},
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "Hello in cloaked mode"}
		]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: originalReq,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: originalReq,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if count := countCacheControls(seenBody); count != 0 {
		t.Fatalf("expected 0 cache_control blocks in cloaked mode with explicit mode and no client breakpoints, got %d: %s", count, string(seenBody))
	}
	if gjson.GetBytes(seenBody, "prompt_cache_options").Exists() {
		t.Fatalf("prompt_cache_options should not be forwarded upstream: %s", string(seenBody))
	}
}

func TestClaudeExecutor_PromptCacheOptionsMode_Explicit_Cloaked_PreservesClientBreakpoints(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		writeClaudeSSEMockResponse(w)
	}))
	defer server.Close()

	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey: "key-123",
			Cloak:  &config.CloakConfig{Mode: "always"},
		}},
	}
	executor := NewClaudeExecutor(cfg)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	originalReq := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"prompt_cache_options": {
			"mode": "explicit"
		},
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "First turn"},
			{"role": "assistant", "content": "Understood."},
			{"role": "user", "content": [
				{"type": "text", "text": "Specific cached user query", "cache_control": {"type": "ephemeral"}}
			]}
		]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: originalReq,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: originalReq,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if count := countCacheControls(seenBody); count != 1 {
		t.Fatalf("expected exactly 1 cache_control block (the client-specified one), got %d: %s", count, string(seenBody))
	}
	if gjson.GetBytes(seenBody, "system.0.cache_control").Exists() || gjson.GetBytes(seenBody, "system.1.cache_control").Exists() {
		t.Fatalf("system blocks should not have automatic cache_control: %s", string(seenBody))
	}
	if !gjson.GetBytes(seenBody, "messages.3.content.0.cache_control").Exists() {
		t.Fatalf("client specified cache_control on user turn should be preserved: %s", string(seenBody))
	}
}

func TestClaudeExecutor_PromptCacheOptionsMode_Explicit_Cloaked_PreservesClientSystemBreakpoint(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		writeClaudeSSEMockResponse(w)
	}))
	defer server.Close()

	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey: "key-123",
			Cloak:  &config.CloakConfig{Mode: "always"},
		}},
	}
	executor := NewClaudeExecutor(cfg)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	originalReq := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"prompt_cache_options": {
			"mode": "explicit"
		},
		"messages": [
			{
				"role": "system",
				"content": "Special client system prompt with breakpoint",
				"cache_control": {"type": "ephemeral", "scope": "global"}
			},
			{
				"role": "user",
				"content": "Regular user query"
			}
		]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: originalReq,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: originalReq,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if count := countCacheControls(seenBody); count != 1 {
		t.Fatalf("expected exactly 1 cache_control block (the client system one), got %d: %s", count, string(seenBody))
	}
	// Verify CPA synthetic agent block does NOT have cache_control
	if gjson.GetBytes(seenBody, "system.1.cache_control").Exists() {
		t.Fatalf("CPA synthetic agent block must not have cache_control: %s", string(seenBody))
	}
	// Verify user message does NOT have cache_control
	if gjson.GetBytes(seenBody, "messages.0.content.0.cache_control").Exists() {
		t.Fatalf("first user message must not receive auto cache_control: %s", string(seenBody))
	}
	// Verify client system prompt has cache_control preserved
	foundClientSystemCC := false
	messages := gjson.GetBytes(seenBody, "messages")
	if messages.IsArray() {
		messages.ForEach(func(_, msg gjson.Result) bool {
			if msg.Get("role").String() == "system" {
				content := msg.Get("content")
				if content.IsArray() {
					content.ForEach(func(_, part gjson.Result) bool {
						if part.Get("text").String() == "Special client system prompt with breakpoint" && part.Get("cache_control.type").String() == "ephemeral" && part.Get("cache_control.scope").String() == "global" {
							foundClientSystemCC = true
						}
						return true
					})
				}
			}
			return true
		})
	}
	system := gjson.GetBytes(seenBody, "system")
	if system.IsArray() {
		system.ForEach(func(_, part gjson.Result) bool {
			if part.Get("text").String() == "Special client system prompt with breakpoint" && part.Get("cache_control.type").String() == "ephemeral" && part.Get("cache_control.scope").String() == "global" {
				foundClientSystemCC = true
			}
			return true
		})
	}
	if !foundClientSystemCC {
		t.Fatalf("client specified cache_control on system prompt must be preserved: %s", string(seenBody))
	}
}

func TestClaudeExecutor_CountTokens_StripsPromptCacheOptions(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens": 15}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	originalReq := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"prompt_cache_options": {
			"mode": "explicit"
		},
		"messages": [
			{"role": "user", "content": "How many tokens?"}
		]
	}`)

	resp, err := executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: originalReq,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: originalReq,
	})
	if err != nil {
		t.Fatalf("CountTokens() error = %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("expected non-empty CountTokens payload")
	}

	// For custom base_url, CountTokens uses the local estimator after stripping fields.
	// Now test countTokensUpstream specifically to verify upstream payload body:
	upstreamResp, errUpstream := executor.countTokensUpstream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: originalReq,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: originalReq,
	})
	if errUpstream != nil {
		t.Fatalf("countTokensUpstream() error = %v", errUpstream)
	}
	if len(upstreamResp.Payload) == 0 {
		t.Fatal("expected non-empty countTokensUpstream payload")
	}
	if gjson.GetBytes(seenBody, "prompt_cache_options").Exists() {
		t.Fatalf("prompt_cache_options should not be forwarded in count_tokens upstream: %s", string(seenBody))
	}
}

func TestClaudeExecutor_CountTokensUpstream_StripsPromptCacheOptions_EvenWithPayloadRule(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens": 15}`))
	}))
	defer server.Close()

	cfg := &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{{Name: "claude-3-5-sonnet-20241022"}},
					Params: map[string]any{
						"prompt_cache_options.mode": "explicit",
					},
				},
			},
		},
	}
	executor := NewClaudeExecutor(cfg)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	originalReq := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"messages": [
			{"role": "user", "content": "How many tokens?"}
		]
	}`)

	upstreamResp, errUpstream := executor.countTokensUpstream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: originalReq,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: originalReq,
	})
	if errUpstream != nil {
		t.Fatalf("countTokensUpstream() error = %v", errUpstream)
	}
	if len(upstreamResp.Payload) == 0 {
		t.Fatal("expected non-empty countTokensUpstream payload")
	}
	if gjson.GetBytes(seenBody, "prompt_cache_options").Exists() {
		t.Fatalf("prompt_cache_options should not be forwarded in count_tokens upstream even if injected by payload rule: %s", string(seenBody))
	}
}

func TestClaudeExecutor_PromptCacheOptionsMode_Explicit_ProbePreservesClient1hTTL(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		writeClaudeSSEMockResponse(w)
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	// "Hi" is a classic probe text that would trigger isProbeOrHelper in native cloaking.
	originalReq := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"prompt_cache_options": {
			"mode": "explicit"
		},
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "Hi", "cache_control": {"type": "ephemeral", "ttl": "1h"}}
				]
			}
		]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: originalReq,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: originalReq,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if gotTTL := gjson.GetBytes(seenBody, "messages.0.content.0.cache_control.ttl").String(); gotTTL != "1h" {
		t.Fatalf("client specified 1h TTL must be preserved in explicit mode even on probe request, got %q: %s", gotTTL, string(seenBody))
	}
}

func TestClaudeExecutor_CountTokensUpstream_Cloaked_ExplicitPreservesClientSystemBreakpoint(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens": 20}`))
	}))
	defer server.Close()

	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey: "key-123",
			Cloak:  &config.CloakConfig{Mode: "always"},
		}},
	}
	executor := NewClaudeExecutor(cfg)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	originalReq := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"prompt_cache_options": {
			"mode": "explicit"
		},
		"messages": [
			{
				"role": "system",
				"content": "Count tokens system instructions",
				"cache_control": {"type": "ephemeral", "scope": "global"}
			},
			{
				"role": "user",
				"content": "Count tokens user message"
			}
		]
	}`)

	upstreamResp, errUpstream := executor.countTokensUpstream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: originalReq,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: originalReq,
	})
	if errUpstream != nil {
		t.Fatalf("countTokensUpstream() error = %v", errUpstream)
	}
	if len(upstreamResp.Payload) == 0 {
		t.Fatal("expected non-empty countTokensUpstream payload")
	}

	if count := countCacheControls(seenBody); count != 1 {
		t.Fatalf("expected exactly 1 cache_control in count_tokens explicit mode, got %d: %s", count, string(seenBody))
	}
	// Verify user message did not get auto-cached
	if gjson.GetBytes(seenBody, "messages.0.content.0.cache_control").Exists() {
		t.Fatalf("user message must not receive auto cache_control: %s", string(seenBody))
	}
	// Verify client system breakpoint was preserved with scope: global.
	foundClientSystemCC := false
	system := gjson.GetBytes(seenBody, "system")
	if system.IsArray() {
		system.ForEach(func(_, part gjson.Result) bool {
			if part.Get("text").String() == "Count tokens system instructions" &&
				part.Get("cache_control.type").String() == "ephemeral" &&
				part.Get("cache_control.scope").String() == "global" {
				foundClientSystemCC = true
			}
			return true
		})
	}
	if !foundClientSystemCC {
		t.Fatalf("client specified cache_control with scope: global must be preserved in count_tokens: %s", string(seenBody))
	}
}

func TestClaudeExecutor_PromptCacheOptionsMode_Explicit_Cloaked_LegacyModelPreservesClientSystemBreakpoint(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		writeClaudeSSEMockResponse(w)
	}))
	defer server.Close()

	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey: "key-123",
			Cloak:  &config.CloakConfig{Mode: "always"},
		}},
	}
	executor := NewClaudeExecutor(cfg)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	// claude-3-5-haiku-20241022 is in claudeLegacySystemReminderModels, so it uses legacy system reminders
	originalReq := []byte(`{
		"model": "claude-3-5-haiku-20241022",
		"prompt_cache_options": {
			"mode": "explicit"
		},
		"messages": [
			{
				"role": "system",
				"content": "Legacy system instructions with scope",
				"cache_control": {"type": "ephemeral", "scope": "global"}
			},
			{
				"role": "user",
				"content": "User prompt"
			}
		]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-haiku-20241022",
		Payload: originalReq,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: originalReq,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if count := countCacheControls(seenBody); count != 1 {
		t.Fatalf("expected exactly 1 cache_control block in legacy model explicit mode, got %d: %s", count, string(seenBody))
	}
	// Verify system.1 agent block does not have cache_control
	if gjson.GetBytes(seenBody, "system.1.cache_control").Exists() {
		t.Fatalf("agent block must not have cache_control: %s", string(seenBody))
	}
	// Verify the prepended reminder on user message preserves scope: global
	foundReminderCC := false
	messages := gjson.GetBytes(seenBody, "messages")
	if messages.IsArray() {
		messages.ForEach(func(_, msg gjson.Result) bool {
			content := msg.Get("content")
			if content.IsArray() {
				content.ForEach(func(_, part gjson.Result) bool {
					if strings.Contains(part.Get("text").String(), "Legacy system instructions with scope") &&
						part.Get("cache_control.type").String() == "ephemeral" &&
						part.Get("cache_control.scope").String() == "global" {
						foundReminderCC = true
					}
					return true
				})
			}
			return true
		})
	}
	if !foundReminderCC {
		t.Fatalf("legacy system reminder must preserve client cache_control with scope: global: %s", string(seenBody))
	}
}

func TestClaudeExecutor_CountTokensUpstream_Cloaked_LegacyModel_ExplicitPreservesClientSystemBreakpoint(t *testing.T) {
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = bytes.Clone(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens": 20}`))
	}))
	defer server.Close()

	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey: "key-123",
			Cloak:  &config.CloakConfig{Mode: "always"},
		}},
	}
	executor := NewClaudeExecutor(cfg)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	originalReq := []byte(`{
		"model": "claude-3-5-haiku-20241022",
		"prompt_cache_options": {
			"mode": "explicit"
		},
		"messages": [
			{
				"role": "system",
				"content": "Count tokens legacy system instructions",
				"cache_control": {"type": "ephemeral", "scope": "global"}
			},
			{
				"role": "user",
				"content": "User prompt"
			}
		]
	}`)

	upstreamResp, errUpstream := executor.countTokensUpstream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-haiku-20241022",
		Payload: originalReq,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FromString("openai"),
		OriginalRequest: originalReq,
	})
	if errUpstream != nil {
		t.Fatalf("countTokensUpstream() error = %v", errUpstream)
	}
	if len(upstreamResp.Payload) == 0 {
		t.Fatal("expected non-empty countTokensUpstream payload")
	}

	if count := countCacheControls(seenBody); count != 1 {
		t.Fatalf("expected exactly 1 cache_control in legacy count_tokens explicit mode, got %d: %s", count, string(seenBody))
	}
	foundReminderCC := false
	messages := gjson.GetBytes(seenBody, "messages")
	if messages.IsArray() {
		messages.ForEach(func(_, msg gjson.Result) bool {
			content := msg.Get("content")
			if content.IsArray() {
				content.ForEach(func(_, part gjson.Result) bool {
					if strings.Contains(part.Get("text").String(), "Count tokens legacy system instructions") &&
						part.Get("cache_control.type").String() == "ephemeral" &&
						part.Get("cache_control.scope").String() == "global" {
						foundReminderCC = true
					}
					return true
				})
			}
			return true
		})
	}
	if !foundReminderCC {
		t.Fatalf("legacy count_tokens system reminder must preserve client cache_control with scope: global: %s", string(seenBody))
	}
}

func TestClaudeExecutor_CountTokensUpstream_OAuthRelocatesAffectedSystemPrompts(t *testing.T) {
	tests := []struct {
		name          string
		messages      string
		wantMessages  int64
		wantSystemIdx int
	}{
		{
			name:          "single user",
			messages:      `[{"role":"user","content":"request"}]`,
			wantMessages:  2,
			wantSystemIdx: 1,
		},
		{
			name:          "leading user run",
			messages:      `[{"role":"user","content":"prompt"},{"role":"user","content":"context"},{"role":"assistant","content":"answer"},{"role":"user","content":"follow-up"}]`,
			wantMessages:  5,
			wantSystemIdx: 2,
		},
		{
			name:          "trailing user run",
			messages:      `[{"role":"user","content":"first"},{"role":"user","content":"second"}]`,
			wantMessages:  3,
			wantSystemIdx: 2,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var seenBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				seenBody = bytes.Clone(body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"input_tokens":20}`))
			}))
			defer server.Close()

			auth := &cliproxyauth.Auth{
				Attributes: map[string]string{
					"api_key":  "sk-ant-oat-count-tokens",
					"base_url": server.URL,
				},
				Metadata: claudeOAuthTestMetadata(),
			}
			payload := []byte(`{"model":"claude-opus-5","system":"caller guidance","messages":` + test.messages + `}`)
			executor := NewClaudeExecutor(&config.Config{})
			resp, errCount := executor.countTokensUpstream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "claude-opus-5",
				Payload: payload,
			}, cliproxyexecutor.Options{
				SourceFormat:    sdktranslator.FormatClaude,
				OriginalRequest: payload,
			})
			if errCount != nil {
				t.Fatalf("countTokensUpstream() error = %v", errCount)
			}
			if len(resp.Payload) == 0 {
				t.Fatal("expected non-empty countTokensUpstream payload")
			}
			if gjson.GetBytes(seenBody, "system").Exists() {
				t.Fatalf("OAuth caller system prompt must not remain top-level: %s", seenBody)
			}
			if got := gjson.GetBytes(seenBody, "messages.#").Int(); got != test.wantMessages {
				t.Fatalf("message count = %d, want %d: %s", got, test.wantMessages, seenBody)
			}
			messagePath := fmt.Sprintf("messages.%d", test.wantSystemIdx)
			if got := gjson.GetBytes(seenBody, messagePath+".role").String(); got != "system" {
				t.Fatalf("%s.role = %q, want system: %s", messagePath, got, seenBody)
			}
			if got := gjson.GetBytes(seenBody, messagePath+".content.0.text").String(); got != "caller guidance" {
				t.Fatalf("%s.content.0.text = %q, want caller guidance: %s", messagePath, got, seenBody)
			}
		})
	}
}
