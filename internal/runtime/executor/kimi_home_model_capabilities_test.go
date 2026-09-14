package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type kimiHomeModelCapabilityDispatcher struct{}

func (*kimiHomeModelCapabilityDispatcher) HeartbeatOK() bool { return true }

func (*kimiHomeModelCapabilityDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	return json.Marshal(map[string]any{
		"model":    "kimi-k3",
		"provider": "kimi",
		"model_info": map[string]any{
			"id":             "kimi-k3",
			"type":           "kimi",
			"context_length": 1048576,
			"thinking": map[string]any{
				"levels": []string{"low", "high"},
			},
			"user_defined": false,
		},
		"auth": cliproxyauth.Auth{
			ID:         "home-kimi-k3-auth",
			Provider:   "kimi",
			Status:     cliproxyauth.StatusActive,
			Attributes: map[string]string{},
			Metadata: map[string]any{
				"access_token": "token",
				"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		},
	})
}

func (*kimiHomeModelCapabilityDispatcher) AbortAmbiguousDispatch() {}

func TestKimiHomeModelThinkingCapabilitiesOverrideLocalRegistry(t *testing.T) {
	tests := []struct {
		name   string
		stream bool
	}{
		{name: "non-streaming"},
		{name: "streaming", stream: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var upstreamBody []byte
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				var errRead error
				upstreamBody, errRead = io.ReadAll(req.Body)
				if errRead != nil {
					return nil, errRead
				}
				if tt.stream {
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
						Body: io.NopCloser(strings.NewReader(
							"data: {\"id\":\"chatcmpl_test\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"k3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n" +
								"data: [DONE]\n\n",
						)),
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body: io.NopCloser(strings.NewReader(
						`{"id":"chatcmpl_test","object":"chat.completion","created":1,"model":"k3","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
					)),
				}, nil
			}))

			cfg := &config.Config{RequestRetry: 1}
			cfg.Home.Enabled = true
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.RegisterExecutor(NewKimiExecutor(cfg))
			manager.PublishHomeDispatch(&kimiHomeModelCapabilityDispatcher{}, executionregistry.New(), 1)

			payload := []byte(`{"model":"kimi-k3","reasoning_effort":"max","messages":[{"role":"user","content":"hello"}]}`)
			req := cliproxyexecutor.Request{Model: "kimi-k3", Payload: payload}
			opts := cliproxyexecutor.Options{
				Stream:          tt.stream,
				SourceFormat:    sdktranslator.FormatOpenAI,
				ResponseFormat:  sdktranslator.FormatOpenAI,
				OriginalRequest: payload,
			}
			if tt.stream {
				result, errExecute := manager.ExecuteStream(ctx, []string{"kimi"}, req, opts)
				if errExecute != nil {
					t.Fatalf("ExecuteStream() error = %v", errExecute)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error = %v", chunk.Err)
					}
				}
			} else {
				if _, errExecute := manager.Execute(ctx, []string{"kimi"}, req, opts); errExecute != nil {
					t.Fatalf("Execute() error = %v", errExecute)
				}
			}

			if got := gjson.GetBytes(upstreamBody, "thinking.type").String(); got != "enabled" {
				t.Fatalf("thinking.type = %q, want enabled; body=%s", got, upstreamBody)
			}
			if got := gjson.GetBytes(upstreamBody, "thinking.effort").String(); got != "high" {
				t.Fatalf("thinking.effort = %q, want high from Home model capabilities; body=%s", got, upstreamBody)
			}
			if effort := gjson.GetBytes(upstreamBody, "reasoning_effort"); effort.Exists() {
				t.Fatalf("reasoning_effort should be absent, got %s; body=%s", effort.Raw, upstreamBody)
			}
		})
	}
}
