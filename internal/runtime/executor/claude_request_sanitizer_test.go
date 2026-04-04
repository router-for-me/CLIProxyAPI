package executor

import (
	"bytes"
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

const (
	validClaudeSignature     = "valid_signature_1234567890123456789012345678901234567890"
	syntheticGPTSignature    = "gpt#valid_signature_1234567890123456789012345678901234567890"
	syntheticClaudeSignature = "claude#valid_signature_1234567890123456789012345678901234567890"
)

func TestSanitizeClaudeRequestBody_RemovesInvalidThinkingBlocks(t *testing.T) {
	input := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "missing signature"},
					{"type": "text", "text": "keep text"},
					{"type": "thinking", "thinking": "blank signature", "signature": "   "},
					{"type": "tool_use", "id": "tool_1", "name": "search", "input": {"q": "tea"}},
					{"type": "thinking", "thinking": "synthetic", "signature": "` + syntheticGPTSignature + `"},
					{"type": "thinking", "thinking": "signed", "signature": "` + validClaudeSignature + `"},
					{"type": "tool_result", "tool_use_id": "tool_1", "content": "ok"}
				]
			}
		]
	}`)

	out := sanitizeClaudeRequestBody(input)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 4 {
		t.Fatalf("content block count = %d, want 4", len(content))
	}
	if got := content[0].Get("type").String(); got != "text" {
		t.Fatalf("content[0].type = %q, want %q", got, "text")
	}
	if got := content[1].Get("type").String(); got != "tool_use" {
		t.Fatalf("content[1].type = %q, want %q", got, "tool_use")
	}
	if got := content[2].Get("signature").String(); got != validClaudeSignature {
		t.Fatalf("content[2].signature = %q, want %q", got, validClaudeSignature)
	}
	if got := content[3].Get("type").String(); got != "tool_result" {
		t.Fatalf("content[3].type = %q, want %q", got, "tool_result")
	}
}

func TestSanitizeClaudeRequestBody_RemovesNonStringSignature(t *testing.T) {
	input := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "bad signature type", "signature": 123},
					{"type": "text", "text": "keep"}
				]
			}
		]
	}`)

	out := sanitizeClaudeRequestBody(input)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 1 {
		t.Fatalf("content block count = %d, want 1", len(content))
	}
	if got := content[0].Get("type").String(); got != "text" {
		t.Fatalf("content[0].type = %q, want %q", got, "text")
	}
}

func TestSanitizeClaudeRequestBody_PreservesOtherRoles(t *testing.T) {
	input := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "thinking", "thinking": "user thinking without signature"},
					{"type": "text", "text": "hi"}
				]
			}
		]
	}`)

	out := sanitizeClaudeRequestBody(input)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("user content block count = %d, want 2", len(content))
	}
	if got := content[0].Get("type").String(); got != "thinking" {
		t.Fatalf("user content[0].type = %q, want %q", got, "thinking")
	}
}

func TestSanitizeClaudeRequestBody_RemovesSyntheticPrefixedThinkingSignatures(t *testing.T) {
	input := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "gpt synthetic", "signature": "` + syntheticGPTSignature + `"},
					{"type": "thinking", "thinking": "claude synthetic", "signature": "` + syntheticClaudeSignature + `"},
					{"type": "thinking", "thinking": "raw claude", "signature": "` + validClaudeSignature + `"},
					{"type": "text", "text": "keep me"}
				]
			}
		]
	}`)

	out := sanitizeClaudeRequestBody(input)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("content block count = %d, want 2", len(content))
	}
	if got := content[0].Get("signature").String(); got != validClaudeSignature {
		t.Fatalf("content[0].signature = %q, want %q", got, validClaudeSignature)
	}
	if got := content[1].Get("type").String(); got != "text" {
		t.Fatalf("content[1].type = %q, want %q", got, "text")
	}
}

func TestSanitizeClaudeRequestBody_RemovesAssistantMessagesThatBecomeEmpty(t *testing.T) {
	input := []byte(`{
		"model": "claude-3-5-sonnet-20241022",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "keep me"}
				]
			},
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "missing signature"}
				]
			}
		]
		}`)

	out := sanitizeClaudeRequestBody(input)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 1 {
		t.Fatalf("message count = %d, want 1", len(messages))
	}
	if got := messages[0].Get("role").String(); got != "user" {
		t.Fatalf("messages[0].role = %q, want %q", got, "user")
	}
}

func TestClaudeExecutor_SanitizesUnsignedThinkingAcrossClaudeRequestPaths(t *testing.T) {
	testCases := []struct {
		name       string
		invoke     func(t *testing.T, executor *ClaudeExecutor, auth *cliproxyauth.Auth, payload []byte)
		wantPath   string
		wantStream bool
	}{
		{
			name: "execute",
			invoke: func(t *testing.T, executor *ClaudeExecutor, auth *cliproxyauth.Auth, payload []byte) {
				t.Helper()
				_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
					Model:   "claude-3-5-sonnet-20241022",
					Payload: payload,
				}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
				if err != nil {
					t.Fatalf("Execute() error = %v", err)
				}
			},
			wantPath: "/v1/messages",
		},
		{
			name: "execute_stream",
			invoke: func(t *testing.T, executor *ClaudeExecutor, auth *cliproxyauth.Auth, payload []byte) {
				t.Helper()
				result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
					Model:   "claude-3-5-sonnet-20241022",
					Payload: payload,
				}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
				if err != nil {
					t.Fatalf("ExecuteStream() error = %v", err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("ExecuteStream() chunk error = %v", chunk.Err)
					}
				}
			},
			wantPath:   "/v1/messages",
			wantStream: true,
		},
		{
			name: "count_tokens",
			invoke: func(t *testing.T, executor *ClaudeExecutor, auth *cliproxyauth.Auth, payload []byte) {
				t.Helper()
				_, err := executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
					Model:   "claude-3-5-sonnet-20241022",
					Payload: payload,
				}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
				if err != nil {
					t.Fatalf("CountTokens() error = %v", err)
				}
			},
			wantPath: "/v1/messages/count_tokens",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var seenBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.wantPath {
					t.Fatalf("request path = %q, want %q", r.URL.Path, tc.wantPath)
				}
				body, _ := io.ReadAll(r.Body)
				seenBody = bytes.Clone(body)

				if tc.wantStream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
					return
				}

				w.Header().Set("Content-Type", "application/json")
				if tc.wantPath == "/v1/messages/count_tokens" {
					_, _ = w.Write([]byte(`{"input_tokens":42}`))
					return
				}
				_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet-20241022","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
			}))
			defer server.Close()

			executor := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"api_key":  "key-123",
				"base_url": server.URL,
			}}

			payload := []byte(`{
					"model":"claude-3-5-sonnet-20241022",
					"messages": [
						{"role":"user","content":[{"type":"text","text":"hi"}]},
						{"role":"assistant","content":[
							{"type":"thinking","thinking":"unsigned scratchpad"},
							{"type":"text","text":"visible reply"},
							{"type":"thinking","thinking":"synthetic scratchpad","signature":"` + syntheticGPTSignature + `"},
							{"type":"thinking","thinking":"signed scratchpad","signature":"` + validClaudeSignature + `"}
						]}
					]
				}`)

			tc.invoke(t, executor, auth, payload)

			if len(seenBody) == 0 {
				t.Fatal("expected request body to be captured")
			}

			content := gjson.GetBytes(seenBody, "messages.1.content").Array()
			if len(content) != 2 {
				t.Fatalf("assistant content block count = %d, want 2; body=%s", len(content), string(seenBody))
			}
			if got := content[0].Get("type").String(); got != "text" {
				t.Fatalf("content[0].type = %q, want %q", got, "text")
			}
			if got := content[1].Get("signature").String(); got != validClaudeSignature {
				t.Fatalf("content[1].signature = %q, want %q", got, validClaudeSignature)
			}
		})
	}
}

func TestClaudeExecutor_RemovesAssistantMessagesThatBecomeEmptyAcrossClaudeRequestPaths(t *testing.T) {
	testCases := []struct {
		name       string
		invoke     func(t *testing.T, executor *ClaudeExecutor, auth *cliproxyauth.Auth, payload []byte)
		wantPath   string
		wantStream bool
	}{
		{
			name: "execute",
			invoke: func(t *testing.T, executor *ClaudeExecutor, auth *cliproxyauth.Auth, payload []byte) {
				t.Helper()
				_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
					Model:   "claude-3-5-sonnet-20241022",
					Payload: payload,
				}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
				if err != nil {
					t.Fatalf("Execute() error = %v", err)
				}
			},
			wantPath: "/v1/messages",
		},
		{
			name: "execute_stream",
			invoke: func(t *testing.T, executor *ClaudeExecutor, auth *cliproxyauth.Auth, payload []byte) {
				t.Helper()
				result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
					Model:   "claude-3-5-sonnet-20241022",
					Payload: payload,
				}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
				if err != nil {
					t.Fatalf("ExecuteStream() error = %v", err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("ExecuteStream() chunk error = %v", chunk.Err)
					}
				}
			},
			wantPath:   "/v1/messages",
			wantStream: true,
		},
		{
			name: "count_tokens",
			invoke: func(t *testing.T, executor *ClaudeExecutor, auth *cliproxyauth.Auth, payload []byte) {
				t.Helper()
				_, err := executor.CountTokens(context.Background(), auth, cliproxyexecutor.Request{
					Model:   "claude-3-5-sonnet-20241022",
					Payload: payload,
				}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
				if err != nil {
					t.Fatalf("CountTokens() error = %v", err)
				}
			},
			wantPath: "/v1/messages/count_tokens",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var seenBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.wantPath {
					t.Fatalf("request path = %q, want %q", r.URL.Path, tc.wantPath)
				}
				body, _ := io.ReadAll(r.Body)
				seenBody = bytes.Clone(body)

				if tc.wantStream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
					return
				}

				w.Header().Set("Content-Type", "application/json")
				if tc.wantPath == "/v1/messages/count_tokens" {
					_, _ = w.Write([]byte(`{"input_tokens":42}`))
					return
				}
				_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet-20241022","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
			}))
			defer server.Close()

			executor := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"api_key":  "key-123",
				"base_url": server.URL,
			}}

			payload := []byte(`{
				"messages": [
					{"role":"user","content":[{"type":"text","text":"first user"}]},
					{"role":"assistant","content":[
						{"type":"thinking","thinking":"unsigned scratchpad"}
					]},
					{"role":"user","content":[{"type":"text","text":"second user"}]}
				]
			}`)

			tc.invoke(t, executor, auth, payload)

			if len(seenBody) == 0 {
				t.Fatal("expected request body to be captured")
			}

			messages := gjson.GetBytes(seenBody, "messages").Array()
			if len(messages) != 2 {
				t.Fatalf("message count = %d, want 2; body=%s", len(messages), string(seenBody))
			}
			if got := messages[0].Get("role").String(); got != "user" {
				t.Fatalf("messages[0].role = %q, want %q", got, "user")
			}
			if got := messages[1].Get("role").String(); got != "user" {
				t.Fatalf("messages[1].role = %q, want %q", got, "user")
			}
		})
	}
}
