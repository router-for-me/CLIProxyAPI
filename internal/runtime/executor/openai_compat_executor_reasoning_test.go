package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestOpenAICompatExecutorUsesCompatibleClaudeTranslation(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test-key",
		},
	}
	request := cliproxyexecutor.Request{
		Model:   "deepseek-v4-flash",
		Payload: []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"prior reasoning","signature":""},{"type":"tool_use","id":"call_1","name":"Read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"ok"}]}]}`),
		Metadata: map[string]any{
			"cliproxy.resolved_api_key_model_info": &registry.ModelInfo{IsCompat: true},
		},
	}
	options := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatOpenAI,
	}

	if _, errExecute := executor.Execute(context.Background(), auth, request, options); errExecute != nil {
		t.Fatalf("Execute error: %v", errExecute)
	}

	assistant := ojson.GetBytes(upstreamBody, "messages.0")
	if got := assistant.Get("reasoning_content").String(); got != "prior reasoning" {
		t.Fatalf("reasoning_content = %q, want %q; body=%s", got, "prior reasoning", upstreamBody)
	}
	if !assistant.Get("tool_calls").Exists() {
		t.Fatalf("tool_calls missing from upstream request: %s", upstreamBody)
	}
}

func TestOpenAICompatExecutorClaudeStandaloneEffortFinalPayload(t *testing.T) {
	models := []config.PayloadModelRule{{Name: "custom-openai", Protocol: "openai"}}
	for _, tt := range []struct {
		name       string
		payload    config.PayloadConfig
		wantEffort string
	}{
		{name: "standalone effort preserved", wantEffort: "high"},
		{
			name: "final override wins",
			payload: config.PayloadConfig{Override: []config.PayloadRule{
				{Models: models, Params: map[string]any{"reasoning_effort": "low"}},
			}},
			wantEffort: "low",
		},
		{
			name: "final filter is not restored",
			payload: config.PayloadConfig{Filter: []config.PayloadFilterRule{
				{Models: models, Params: []string{"reasoning_effort"}},
			}},
		},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", tt.name, stream), func(t *testing.T) {
				bodies := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Errorf("read upstream request: %v", errRead)
					}
					bodies <- body
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
				}))
				t.Cleanup(server.Close)

				executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{Payload: tt.payload})
				auth := &cliproxyauth.Auth{
					Provider: "openai-compatibility",
					Attributes: map[string]string{
						"base_url": server.URL,
						"api_key":  "test-key",
					},
				}
				req := cliproxyexecutor.Request{
					Model:   "custom-openai",
					Payload: []byte(`{"model":"custom-openai","max_tokens":16,"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`),
				}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: stream}
				if stream {
					result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
					if errStream != nil {
						t.Fatalf("ExecuteStream: %v", errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatalf("stream chunk: %v", chunk.Err)
						}
					}
				} else if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
					t.Fatalf("Execute: %v", errExecute)
				}
				body := <-bodies
				effort := ojson.GetBytes(body, "reasoning_effort")
				if effort.String() != tt.wantEffort || (tt.wantEffort == "" && effort.Exists()) {
					t.Fatalf("upstream reasoning_effort = %s, want %q; body=%s", effort.Raw, tt.wantEffort, body)
				}
			})
		}
	}
}

func TestOpenAICompatExecutorStripsAssistantReasoningContentForMistralUpstream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			bodies := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Errorf("read upstream request: %v", errRead)
				}
				bodies <- body
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
			}))
			t.Cleanup(server.Close)

			executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
			auth := &cliproxyauth.Auth{
				Provider: "openai-compatibility",
				ProxyURL: server.URL,
				Attributes: map[string]string{
					"base_url": "http://api.mistral.ai/v1",
					"api_key":  "test-key",
				},
			}
			req := cliproxyexecutor.Request{
				Model: "deepseek-v4-1-flash-1017",
				Payload: []byte(`{
					"model": "deepseek-v4.1-flash",
					"reasoning": {"effort": "max"},
					"input": [
						{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
						{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"running tool"}]},
						{"type":"function_call","call_id":"call_1","name":"terminal","arguments":"{}"},
						{"type":"function_call_output","call_id":"call_1","output":"ok"}
					]
				}`),
				Metadata: map[string]any{
					"cliproxy.resolved_api_key_model_info": &registry.ModelInfo{IsCompat: true},
				},
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatOpenAIResponse,
				ResponseFormat: sdktranslator.FormatOpenAIResponse,
				Stream:         stream,
			}
			if stream {
				result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
				if errStream != nil {
					t.Fatalf("ExecuteStream: %v", errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk: %v", chunk.Err)
					}
				}
			} else if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
				t.Fatalf("Execute: %v", errExecute)
			}

			body := <-bodies
			messages := ojson.GetBytes(body, "messages").Array()
			if len(messages) != 3 {
				t.Fatalf("messages count = %d, want 3; body=%s", len(messages), body)
			}
			for i, msg := range messages {
				if msg.Get("reasoning_content").Exists() {
					t.Fatalf("messages[%d] unexpectedly contains reasoning_content: %s", i, msg.Raw)
				}
			}
			if got := messages[1].Get("tool_calls.0.id").String(); got != "call_1" {
				t.Fatalf("messages[1] tool_calls.0.id = %q, want call_1; body=%s", got, body)
			}
		})
	}
}

func TestIsMistralUpstreamURL(t *testing.T) {
	for _, tt := range []struct {
		url  string
		want bool
	}{
		{"https://api.mistral.ai/v1", true},
		{"https://API.MISTRAL.AI/v1/", true},
		{"https://mistral.ai/v1", true},
		{"https://api.mistral.ai./v1", true},
		{"https://eu.api.mistral.ai:443/v1", true},
		{"https://notmistral.ai/v1", false},
		{"https://mistral.ai.evil.com/v1", false},
		{"https://api.deepseek.com/v1", false},
		{"https://openrouter.ai/api/v1", false},
		{"", false},
		{"://bad", false},
	} {
		if got := isMistralUpstreamURL(tt.url); got != tt.want {
			t.Errorf("isMistralUpstreamURL(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

func TestStripAssistantReasoningContentDoc(t *testing.T) {
	for _, tt := range []struct {
		name        string
		in          string
		wantStrip   int
		wantDropped int
		check       func(t *testing.T, out []byte)
	}{
		{
			name:      "tool call message keeps tool_calls",
			in:        `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"","reasoning_content":"[reasoning unavailable]","tool_calls":[{"id":"call_1","type":"function","function":{"name":"Read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"ok"}]}`,
			wantStrip: 1,
			check: func(t *testing.T, out []byte) {
				if n := len(ojson.GetBytes(out, "messages").Array()); n != 3 {
					t.Fatalf("messages len = %d, want 3; body=%s", n, out)
				}
				if ojson.GetBytes(out, "messages.1.reasoning_content").Exists() {
					t.Fatalf("reasoning_content not stripped: %s", out)
				}
				if ojson.GetBytes(out, "messages.1.tool_calls.0.id").String() != "call_1" {
					t.Fatalf("tool_calls lost: %s", out)
				}
			},
		},
		{
			name:      "text content message keeps content",
			in:        `{"messages":[{"role":"assistant","content":"answer","reasoning_content":"why"}]}`,
			wantStrip: 1,
			check: func(t *testing.T, out []byte) {
				if ojson.GetBytes(out, "messages.0.content").String() != "answer" {
					t.Fatalf("content lost: %s", out)
				}
				if ojson.GetBytes(out, "messages.0.reasoning_content").Exists() {
					t.Fatalf("reasoning_content not stripped: %s", out)
				}
			},
		},
		{
			name:        "reasoning-only messages are dropped",
			in:          `{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"","reasoning_content":"r1"},{"role":"user","content":"b"},{"role":"assistant","content":null,"reasoning_content":"r2"},{"role":"assistant","content":[],"reasoning_content":"r3"},{"role":"assistant","reasoning_content":"r4"},{"role":"user","content":"c"}]}`,
			wantStrip:   4,
			wantDropped: 4,
			check: func(t *testing.T, out []byte) {
				msgs := ojson.GetBytes(out, "messages").Array()
				if len(msgs) != 3 {
					t.Fatalf("messages len = %d, want 3; body=%s", len(msgs), out)
				}
				for i, want := range []string{"a", "b", "c"} {
					if msgs[i].Get("role").String() != "user" || msgs[i].Get("content").String() != want {
						t.Fatalf("messages[%d] = %s, want user %q; body=%s", i, msgs[i].Raw, want, out)
					}
				}
			},
		},
		{
			name:      "legacy function_call message is kept",
			in:        `{"messages":[{"role":"assistant","content":null,"reasoning_content":"r","function_call":{"name":"f","arguments":"{}"}}]}`,
			wantStrip: 1,
			check: func(t *testing.T, out []byte) {
				if !ojson.GetBytes(out, "messages.0.function_call").Exists() {
					t.Fatalf("function_call message dropped: %s", out)
				}
				if ojson.GetBytes(out, "messages.0.reasoning_content").Exists() {
					t.Fatalf("reasoning_content not stripped: %s", out)
				}
			},
		},
		{
			name: "non-assistant and clean messages untouched",
			in:   `{"messages":[{"role":"user","content":"hi","reasoning_content":"user-side"},{"role":"assistant","content":"ok"}]}`,
			check: func(t *testing.T, out []byte) {
				if ojson.GetBytes(out, "messages.0.reasoning_content").String() != "user-side" {
					t.Fatalf("non-assistant message modified: %s", out)
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc := ojson.NewDoc([]byte(tt.in))
			stripped, dropped := stripAssistantReasoningContentDoc(doc)
			if stripped != tt.wantStrip || dropped != tt.wantDropped {
				t.Fatalf("stripped/dropped = %d/%d, want %d/%d", stripped, dropped, tt.wantStrip, tt.wantDropped)
			}
			tt.check(t, doc.Bytes())
		})
	}
}

func TestSanitizeAssistantReasoningContentForUpstreamDocGatesOnHost(t *testing.T) {
	const body = `{"model":"m","reasoning_effort":"max","messages":[{"role":"assistant","content":"","reasoning_content":"[reasoning unavailable]","tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`

	doc := ojson.NewDoc([]byte(body))
	sanitizeAssistantReasoningContentForUpstreamDoc(context.Background(), doc, "https://api.deepseek.com/v1")
	if !ojson.GetBytes(doc.Bytes(), "messages.0.reasoning_content").Exists() {
		t.Fatalf("non-Mistral upstream must keep reasoning_content: %s", doc.Bytes())
	}

	doc = ojson.NewDoc([]byte(body))
	sanitizeAssistantReasoningContentForUpstreamDoc(context.Background(), doc, "https://api.mistral.ai/v1")
	if ojson.GetBytes(doc.Bytes(), "messages.0.reasoning_content").Exists() {
		t.Fatalf("Mistral upstream must strip reasoning_content: %s", doc.Bytes())
	}
	if ojson.GetBytes(doc.Bytes(), "reasoning_effort").String() != "max" {
		t.Fatalf("top-level reasoning_effort must be preserved: %s", doc.Bytes())
	}
}
