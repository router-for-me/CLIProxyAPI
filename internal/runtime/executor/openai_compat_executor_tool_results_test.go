package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorToolResultContentByInputModalities(t *testing.T) {
	tests := []struct {
		name            string
		stream          bool
		inputModalities []string
		textOnly        bool
	}{
		{name: "non-stream text-only", stream: false, inputModalities: []string{"text"}, textOnly: true},
		{name: "stream text-only", stream: true, inputModalities: []string{"text"}, textOnly: true},
		{name: "non-stream multimodal", stream: false, inputModalities: []string{"text", "image"}},
		{name: "non-stream unspecified", stream: false, inputModalities: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotBody, _ = io.ReadAll(r.Body)
				if tt.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: [DONE]\n\n"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
			}))
			defer server.Close()

			executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
				OpenAICompatibility: []config.OpenAICompatibility{{
					Name: "compat",
					Models: []config.OpenAICompatibilityModel{{
						Name:            "mapped-model",
						Alias:           "claude-client",
						InputModalities: tt.inputModalities,
					}},
				}},
			})
			auth := &cliproxyauth.Auth{
				Provider: "openai-compatibility",
				Attributes: map[string]string{
					"base_url":     server.URL + "/v1",
					"api_key":      "test",
					"compat_name":  "compat",
					"provider_key": "compat",
				},
			}
			payload := []byte(`{"model":"claude-client","max_tokens":64,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"inspect_image","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"image inspected"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]}]}]}`)
			req := cliproxyexecutor.Request{Model: "mapped-model", Payload: payload}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatClaude,
				ResponseFormat: sdktranslator.FormatOpenAI,
				Stream:         tt.stream,
			}

			if tt.stream {
				result, errExecute := executor.ExecuteStream(context.Background(), auth, req, opts)
				if errExecute != nil {
					t.Fatalf("ExecuteStream error: %v", errExecute)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error: %v", chunk.Err)
					}
				}
			} else if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
				t.Fatalf("Execute error: %v", errExecute)
			}

			toolContent := gjson.GetBytes(gotBody, "messages.1.content")
			if toolContent.Type != gjson.String || toolContent.String() != "image inspected" {
				t.Fatalf("tool content = %s, want %q; body=%s", toolContent.Raw, "image inspected", string(gotBody))
			}

			relayContent := gjson.GetBytes(gotBody, "messages.2.content")
			if tt.textOnly {
				want := "Images returned by the preceding tool call(s):\n\n[image omitted: unsupported by upstream]"
				if relayContent.Type != gjson.String || relayContent.String() != want {
					t.Fatalf("relay content = %s, want %q; body=%s", relayContent.Raw, want, string(gotBody))
				}
			} else if imageURL := relayContent.Get("1.image_url.url").String(); imageURL != "data:image/png;base64,AA==" {
				t.Fatalf("relay image URL = %q; body=%s", imageURL, string(gotBody))
			}
		})
	}
}
