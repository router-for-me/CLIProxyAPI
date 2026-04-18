package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorCompactFallsBackToChatCompletionsForProfile(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"kimi-k2","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("newapi-provider", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name: "newapi-provider",
			Kind: "newapi",
		}},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL + "/v1",
		"api_key":     "test",
		"compat_name": "newapi-provider",
		"compat_kind": "newapi",
	}}
	payload := []byte(`{"model":"kimi-k2","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "kimi-k2",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/chat/completions")
	}
	if !gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("expected chat completions payload, got %s", string(gotBody))
	}
	if gjson.GetBytes(gotBody, "input").Exists() {
		t.Fatalf("unexpected responses input payload, got %s", string(gotBody))
	}
	if got := gjson.GetBytes(resp.Payload, "object").String(); got != "response" {
		t.Fatalf("response object = %q, want %q; payload=%s", got, "response", string(resp.Payload))
	}
}

func TestOpenAICompatExecutorParsesRetryAfterHints(t *testing.T) {
	tests := []struct {
		name       string
		header     string
		body       string
		want       time.Duration
		wantStatus int
	}{
		{
			name:       "header",
			header:     "7",
			body:       `{"error":{"message":"rate limit exceeded"}}`,
			want:       7 * time.Second,
			wantStatus: http.StatusTooManyRequests,
		},
		{
			name:       "body",
			body:       `{"error":{"message":"quota exhausted","retry_after":9}}`,
			want:       9 * time.Second,
			wantStatus: http.StatusTooManyRequests,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.header != "" {
					w.Header().Set("Retry-After", tt.header)
				}
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{
				"base_url": server.URL + "/v1",
				"api_key":  "test",
			}}
			_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gpt-5",
				Payload: []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai"),
			})
			if err == nil {
				t.Fatal("expected error")
			}
			status, ok := err.(statusErr)
			if !ok {
				t.Fatalf("error type = %T, want statusErr", err)
			}
			if status.StatusCode() != tt.wantStatus {
				t.Fatalf("status = %d, want %d", status.StatusCode(), tt.wantStatus)
			}
			retryAfter := status.RetryAfter()
			if retryAfter == nil {
				t.Fatal("expected retry-after hint")
			}
			if *retryAfter != tt.want {
				t.Fatalf("retry-after = %v, want %v", *retryAfter, tt.want)
			}
		})
	}
}

func TestOpenAICompatExecutorStreamScrubsUnsupportedFieldsForProfile(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("newapi-provider", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name: "newapi-provider",
			Kind: "newapi",
		}},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL + "/v1",
		"api_key":     "test",
		"compat_name": "newapi-provider",
		"compat_kind": "newapi",
	}}

	stream, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model: "kimi-k2",
		Payload: []byte(`{
			"model":"kimi-k2",
			"messages":[{"role":"assistant","content":"thinking","reasoning_content":"hidden"}],
			"stream":true,
			"parallel_tool_calls":true,
			"reasoning":{"effort":"high"},
			"metadata":{"tenant":"demo"},
			"store":true
		}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
	}

	for _, path := range []string{
		"stream_options",
		"parallel_tool_calls",
		"reasoning",
		"metadata",
		"store",
		"messages.0.reasoning_content",
	} {
		if gjson.GetBytes(gotBody, path).Exists() {
			t.Fatalf("unexpected field %s in payload: %s", path, string(gotBody))
		}
	}
}
