package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
		wantString      bool
	}{
		{name: "non-stream text-only", stream: false, inputModalities: []string{"text"}, wantString: true},
		{name: "stream text-only", stream: true, inputModalities: []string{"text"}, wantString: true},
		{name: "non-stream multimodal", stream: false, inputModalities: []string{"text", "image"}, wantString: false},
		{name: "non-stream unspecified", stream: false, inputModalities: nil, wantString: false},
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
			if tt.wantString {
				if toolContent.Type != gjson.String {
					t.Fatalf("tool content type = %s, want string; body=%s", toolContent.Type, string(gotBody))
				}
				want := "image inspected\n\n[image omitted: unsupported by upstream]"
				if toolContent.String() != want {
					t.Fatalf("tool content = %q, want %q", toolContent.String(), want)
				}
				if strings.Contains(string(gotBody), "image_url") {
					t.Fatalf("text-only model still received an image_url part:\n%s", string(gotBody))
				}
			} else {
				if toolContent.Type != gjson.String {
					t.Fatalf("tool content type = %s, want string; body=%s", toolContent.Type, string(gotBody))
				}
				if toolContent.String() != "image inspected" {
					t.Fatalf("tool content = %q, want %q", toolContent.String(), "image inspected")
				}
				if !strings.Contains(string(gotBody), "image_url") {
					t.Fatalf("multimodal model did not receive relayed image_url part:\n%s", string(gotBody))
				}
			}
		})
	}
}

func TestTextOnlyModelDoesNotReceiveImageURL(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "non-stream", true: "stream"}[stream], func(t *testing.T) {
			var gotBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotBody, _ = io.ReadAll(r.Body)
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: [DONE]\n\n"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
			}))
			defer server.Close()

			exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
				OpenAICompatibility: []config.OpenAICompatibility{{
					Name: "compat",
					Models: []config.OpenAICompatibilityModel{{
						Name:            "mapped-model",
						Alias:           "claude-client",
						InputModalities: []string{"text"},
					}},
				}},
			})
			auth := &cliproxyauth.Auth{
				Provider: "openai-compatibility",
				Attributes: map[string]string{
					"base_url": server.URL + "/v1", "api_key": "test",
					"compat_name": "compat", "provider_key": "compat",
				},
			}
			payload := []byte(`{"model":"claude-client","max_tokens":64,"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"inspect_image","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"image inspected"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]}]}]}`)

			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatClaude,
				ResponseFormat: sdktranslator.FormatOpenAI,
				Stream:         stream,
			}
			req := cliproxyexecutor.Request{Model: "mapped-model", Payload: payload}

			if stream {
				result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
				if err != nil {
					t.Fatalf("ExecuteStream: %v", err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("chunk error: %v", chunk.Err)
					}
				}
			} else {
				if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
					t.Fatalf("Execute: %v", err)
				}
			}

			if strings.Contains(string(gotBody), "image_url") {
				t.Fatalf("text-only model still received an image_url part:\n%s", string(gotBody))
			}
		})
	}
}
