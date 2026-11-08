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
