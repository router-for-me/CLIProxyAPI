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

func TestOpenAICompatExecutorReasoningEffortCompatibility(t *testing.T) {
	tests := []struct {
		name        string
		model       config.OpenAICompatibilityModel
		payload     []byte
		expectField string
		expectValue string
	}{
		{
			name: "chat completions clamps xhigh",
			model: config.OpenAICompatibilityModel{
				Name:                         "nvidia/nemotron-3-super-120b-a12b",
				Alias:                        "nemotron-3-super",
				ReasoningEffortCompatibility: true,
			},
			payload:     []byte(`{"model":"nemotron-3-super","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"xhigh"}`),
			expectField: "reasoning_effort",
			expectValue: "high",
		},
		{
			name: "responses format clamps auto",
			model: config.OpenAICompatibilityModel{
				Name:                         "stepfun-ai/step-3.5-flash",
				Alias:                        "step-3.5-flash",
				ReasoningEffortCompatibility: true,
			},
			payload:     []byte(`{"model":"step-3.5-flash","input":[{"role":"user","content":"hi"}],"reasoning":{"effort":"auto"}}`),
			expectField: "reasoning.effort",
			expectValue: "medium",
		},
		{
			name: "disabled compatibility preserves xhigh",
			model: config.OpenAICompatibilityModel{
				Name:  "nvidia/nemotron-3-super-120b-a12b",
				Alias: "nemotron-3-super",
			},
			payload:     []byte(`{"model":"nemotron-3-super","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"xhigh"}`),
			expectField: "reasoning_effort",
			expectValue: "xhigh",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				gotBody = body
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"ok","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`))
			}))
			defer server.Close()

			executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
				OpenAICompatibility: []config.OpenAICompatibility{{
					Name:    "nvidia",
					BaseURL: server.URL + "/v1",
					Models:  []config.OpenAICompatibilityModel{tt.model},
				}},
			})
			auth := &cliproxyauth.Auth{
				Provider: "openai-compatibility",
				Attributes: map[string]string{
					"base_url":    server.URL + "/v1",
					"api_key":     "test",
					"compat_name": "nvidia",
				},
			}

			opts := cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai"),
				Stream:       false,
			}
			if gjson.GetBytes(tt.payload, "input").Exists() {
				opts.SourceFormat = sdktranslator.FromString("openai-response")
				opts.Alt = "responses/compact"
			}

			_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   tt.model.Alias,
				Payload: tt.payload,
			}, opts)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}

			if got := gjson.GetBytes(gotBody, tt.expectField).String(); got != tt.expectValue {
				t.Fatalf("%s = %q, want %q, body=%s", tt.expectField, got, tt.expectValue, string(gotBody))
			}
		})
	}
}

func TestOpenAICompatExecutorReasoningEffortCompatibilityPrefersSelectedUpstreamModel(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	const alias = "claude-opus-4.66"
	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "pool",
			BaseURL: server.URL + "/v1",
			Models: []config.OpenAICompatibilityModel{
				{Name: "qwen3.5-plus", Alias: alias},
				{Name: "glm-5", Alias: alias, ReasoningEffortCompatibility: true},
			},
		}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":    server.URL + "/v1",
			"api_key":     "test",
			"compat_name": "pool",
		},
	}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "glm-5",
		Payload: []byte(`{"model":"claude-opus-4.66","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"xhigh"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: alias,
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if got := gjson.GetBytes(gotBody, "reasoning_effort").String(); got != "high" {
		t.Fatalf("reasoning_effort = %q, want %q, body=%s", got, "high", string(gotBody))
	}
}
