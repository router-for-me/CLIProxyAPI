package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

type interactionsWireModelExecutor interface {
	Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}

// The body-routed Gemini Interactions builders finalize through a direct
// ApplyPayloadConfigWithRequest call, so a payload override rule rewriting the
// body's model must still report the final wire model through
// RecordWireModelMetadata: structured 404s name that rewritten model and the
// conductor classifies them against the reported wire model (#5514 review).
func TestGeminiInteractionsPayloadOverrideReportsWireModel(t *testing.T) {
	for _, tc := range []struct {
		name   string
		vertex bool
		stream bool
	}{{"gemini", false, false}, {"gemini-stream", false, true}, {"vertex", true, false}, {"vertex-stream", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Error(errRead)
					return
				}
				requests <- body
				interaction := `{"id":"interaction_1","environment_id":"env_1","status":"completed","steps":[]}`
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"interaction_1\",\"environment_id\":\"env_1\"}}\n\ndata: {\"event_type\":\"interaction.completed\",\"interaction\":%s}\n\n", interaction)
					return
				}
				_, _ = w.Write([]byte(interaction))
			}))
			defer server.Close()

			cfg := &config.Config{}
			cfg.Payload.Override = []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "antigravity*", Protocol: "interactions"}},
				Params: map[string]any{"model": "configured-wire"},
			}}

			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "gemini-interactions", Attributes: map[string]string{"api_key": "key", "base_url": server.URL}}
			opts := cliproxyexecutor.Options{Metadata: map[string]any{}}
			req := cliproxyexecutor.Request{Model: "antigravity-preview-05-2026", Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}
			var exec interactionsWireModelExecutor
			if tc.vertex {
				exec = NewGeminiVertexExecutor(cfg)
				auth.Provider = "vertex"
				auth.Attributes["interactions"] = "true"
				opts.SourceFormat = sdktranslator.FormatInteractions
				opts.ResponseFormat = sdktranslator.FormatInteractions
				req.Payload = []byte(`{"model":"antigravity-preview-05-2026","input":[{"type":"user_input","content":"hi"}]}`)
			} else {
				exec = NewGeminiInteractionsExecutor(cfg)
				opts.SourceFormat = sdktranslator.FormatOpenAI
				opts.ResponseFormat = sdktranslator.FormatOpenAI
			}

			if tc.stream {
				result, errExecute := exec.ExecuteStream(context.Background(), auth, req, opts)
				if errExecute != nil {
					t.Fatalf("ExecuteStream: %v", errExecute)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("chunk: %v", chunk.Err)
					}
				}
			} else {
				if _, errExecute := exec.Execute(context.Background(), auth, req, opts); errExecute != nil {
					t.Fatalf("Execute: %v", errExecute)
				}
			}

			body := <-requests
			if got := gjson.GetBytes(body, "model").String(); got != "configured-wire" {
				t.Fatalf("request body model = %q, want the override value", got)
			}
			wire, _ := opts.Metadata[cliproxyexecutor.WireModelMetadataKey].(string)
			if wire != "configured-wire" {
				t.Fatalf("Metadata[WireModelMetadataKey] = %q, want the final wire model to be reported", wire)
			}
		})
	}
}
