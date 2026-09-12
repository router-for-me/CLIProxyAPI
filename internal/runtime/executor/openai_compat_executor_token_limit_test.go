package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v3"
)

func TestOpenAICompatExecutorCompletionTokenParameter(t *testing.T) {
	const configYAML = `
openai-compatibility:
  - name: compat
    models:
      - name: reasoning-model
        alias: claude-client
        use-max-completion-tokens: true
      - name: legacy-model
        alias: claude-client
  - name: other-provider
    models:
      - name: reasoning-model
        alias: claude-client
`
	tests := []struct {
		name           string
		format         sdktranslator.Format
		payload        string
		model          string
		provider       string
		alt            string
		override       map[string]any
		filter         []string
		wantLegacy     string
		wantCompletion string
		wantOutput     string
	}{
		{name: "Claude client limit", format: sdktranslator.FormatClaude, payload: `{"model":"claude-client","max_tokens":8192,"messages":[{"role":"user","content":"hi"}]}`, wantCompletion: "8192"},
		{name: "Claude different client limit", format: sdktranslator.FormatClaude, payload: `{"model":"claude-client","max_tokens":32768,"messages":[{"role":"user","content":"hi"}]}`, wantCompletion: "32768"},
		{name: "Responses client limit", format: sdktranslator.FormatOpenAIResponse, payload: `{"model":"claude-client","max_output_tokens":16384,"input":"hi"}`, wantCompletion: "16384"},
		{name: "Chat client limit", payload: `{"max_tokens":4096,"messages":[{"role":"user","content":"hi"}]}`, wantCompletion: "4096"},
		{name: "explicit modern limit wins", payload: `{"max_tokens":4096,"max_completion_tokens":2048,"messages":[]}`, wantCompletion: "2048"},
		{name: "explicit modern null wins", payload: `{"max_tokens":4096,"max_completion_tokens":null,"messages":[]}`, wantCompletion: "null"},
		{name: "modern limit only", payload: `{"max_completion_tokens":1024,"messages":[]}`, wantCompletion: "1024"},
		{name: "null legacy limit", payload: `{"max_tokens":null,"messages":[]}`, wantCompletion: "null"},
		{name: "no limit added", payload: `{"messages":[]}`},
		{name: "disabled model in same alias pool", model: "legacy-model", payload: `{"model":"claude-client","max_tokens":4096,"messages":[]}`, wantLegacy: "4096"},
		{name: "unconfigured upstream sharing client alias", model: "unknown-model", payload: `{"model":"claude-client","max_tokens":4096,"messages":[]}`, wantLegacy: "4096"},
		{name: "same model on other provider", provider: "other-provider", payload: `{"max_tokens":4096,"messages":[]}`, wantLegacy: "4096"},
		{name: "unconfigured provider", provider: "unknown-provider", payload: `{"max_tokens":4096,"messages":[]}`, wantLegacy: "4096"},
		{name: "thinking suffix", model: "reasoning-model(high)", payload: `{"max_tokens":4096,"messages":[]}`, wantCompletion: "4096"},
		{name: "legacy payload override", payload: `{"max_tokens":4096,"messages":[]}`, override: map[string]any{"max_tokens": 3072}, wantCompletion: "3072"},
		{name: "modern payload override", payload: `{"max_tokens":4096,"messages":[]}`, override: map[string]any{"max_completion_tokens": 2048}, wantCompletion: "2048"},
		{name: "payload filter removes limit", payload: `{"max_tokens":4096,"messages":[]}`, filter: []string{"max_tokens"}},
		{name: "Responses compact keeps native limit", format: sdktranslator.FormatOpenAIResponse, alt: "responses/compact", payload: `{"input":[],"max_output_tokens":8192}`, wantOutput: "8192"},
	}
	for _, tt := range tests {
		for _, stream := range []bool{false, true} {
			if stream && tt.alt != "" {
				continue
			}
			t.Run(fmt.Sprintf("%s/stream=%t", tt.name, stream), func(t *testing.T) {
				var cfg config.Config
				if err := yaml.Unmarshal([]byte(configYAML), &cfg); err != nil {
					t.Fatal(err)
				}
				if tt.override != nil {
					cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*", Protocol: "openai"}}, Params: tt.override}}
				}
				if tt.filter != nil {
					cfg.Payload.Filter = []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*", Protocol: "openai"}}, Params: tt.filter}}
				}
				provider := tt.provider
				if provider == "" {
					provider = "compat"
				}
				model := tt.model
				if model == "" {
					model = "reasoning-model"
				}
				format := tt.format
				if format == "" {
					format = sdktranslator.FormatOpenAI
				}
				var gotBody []byte
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					var err error
					gotBody, err = io.ReadAll(req.Body)
					if err != nil {
						return nil, err
					}
					wantPath := "/v1/chat/completions"
					if tt.alt != "" {
						wantPath = "/v1/" + tt.alt
					}
					if req.URL.Path != wantPath {
						t.Errorf("upstream path = %q, want %q", req.URL.Path, wantPath)
					}
					body := `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`
					contentType := "application/json"
					if stream {
						body = "data: [DONE]\n\n"
						contentType = "text/event-stream"
					} else if tt.alt != "" {
						body = `{"id":"resp_1","object":"response.compaction","output":[]}`
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body))}, nil
				}))
				executor := NewOpenAICompatExecutor(provider, &cfg)
				auth := &cliproxyauth.Auth{Provider: provider, Attributes: map[string]string{"base_url": "https://upstream.example/v1", "compat_name": provider}}
				req := cliproxyexecutor.Request{Model: model, Payload: []byte(tt.payload)}
				opts := cliproxyexecutor.Options{
					SourceFormat:   format,
					ResponseFormat: sdktranslator.FormatOpenAI,
					Stream:         stream,
					Alt:            tt.alt,
					Metadata:       map[string]any{cliproxyexecutor.RequestedModelMetadataKey: "claude-client"},
				}
				if stream {
					result, err := executor.ExecuteStream(ctx, auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, err := executor.Execute(ctx, auth, req, opts); err != nil {
					t.Fatal(err)
				}
				if len(gotBody) == 0 {
					t.Fatal("upstream request was not sent")
				}
				for path, want := range map[string]string{"max_tokens": tt.wantLegacy, "max_completion_tokens": tt.wantCompletion, "max_output_tokens": tt.wantOutput} {
					if got := gjson.GetBytes(gotBody, path).Raw; got != want {
						t.Errorf("%s = %q, want %q; body=%s", path, got, want, gotBody)
					}
				}
			})
		}
	}
}
