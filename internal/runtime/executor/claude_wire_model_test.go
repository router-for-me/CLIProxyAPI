package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// The Claude builders finalize through ApplyPayloadConfigWithTrackedPaths, so a
// payload override rule rewriting the body's model must still report the final
// wire model through RecordWireModelMetadata: structured 404s name that
// rewritten model and the conductor classifies them against the reported wire
// model (#5514 review).
func TestClaudePayloadOverrideReportsWireModel(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "execute"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			requests := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Errorf("read upstream body: %v", errRead)
					return
				}
				requests <- body
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, strings.Join([]string{
						`event: message_start`,
						`data: {"type":"message_start","message":{"id":"msg_wire","type":"message","role":"assistant","model":"configured-wire","usage":{"input_tokens":1,"output_tokens":0}}}`,
						`event: content_block_start`,
						`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
						`event: content_block_delta`,
						`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
						`event: content_block_stop`,
						`data: {"type":"content_block_stop","index":0}`,
						`event: message_delta`,
						`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
						`event: message_stop`,
						`data: {"type":"message_stop"}`,
						``,
					}, "\n\n"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"id":"msg_wire","type":"message","role":"assistant","model":"configured-wire","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
			}))
			defer server.Close()

			cfg := &config.Config{}
			cfg.Payload.Override = []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "claude-opus*", Protocol: "claude"}},
				Params: map[string]any{"model": "configured-wire"},
			}}

			executor := NewClaudeExecutor(cfg)
			auth := &cliproxyauth.Auth{
				ID:         t.Name(),
				Attributes: map[string]string{"api_key": "sk-ant-oat-wire-model", "base_url": server.URL},
				Metadata:   claudeOAuthTestMetadata(),
			}
			opts := cliproxyexecutor.Options{
				Metadata:       map[string]any{},
				SourceFormat:   sdktranslator.FormatClaude,
				ResponseFormat: sdktranslator.FormatClaude,
			}
			req := cliproxyexecutor.Request{
				Model:   "claude-opus-5",
				Payload: []byte(`{"model":"claude-opus-5","system":"p","messages":[{"role":"user","content":"hi"}]}`),
			}

			if stream {
				result, errExecute := executor.ExecuteStream(context.Background(), auth, req, opts)
				if errExecute != nil {
					t.Fatalf("ExecuteStream: %v", errExecute)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("chunk: %v", chunk.Err)
					}
				}
			} else {
				if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
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
