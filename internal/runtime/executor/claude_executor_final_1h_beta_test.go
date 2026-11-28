package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// A payload rule can turn a main request into the probe shape after the 1h
// upgrade. The body keeps ttl 1h, so the final Anthropic-Beta must still carry
// extended-cache-ttl-2025-04-11 or upstream rejects the request.
func TestClaudeExecutor_Final1hBodyKeepsExtendedCacheBetaAfterProbeShapeRule(t *testing.T) {
	const userID = `{"device_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","account_uuid":"","session_id":"11111111-2222-4333-8444-555555555555"}`
	nativePayload := []byte(`{"model":"claude-opus-4-6","max_tokens":1000,"system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"test","cache_control":{"type":"ephemeral"}}]}],"metadata":{"user_id":` + fmt.Sprintf("%q", userID) + `}}`)
	plainPayload := []byte(`{"model":"claude-opus-4-6","max_tokens":1000,"messages":[{"role":"user","content":[{"type":"text","text":"test"}]}]}`)

	cases := []struct {
		name     string
		payload  []byte
		incoming http.Header
		mainTTL  bool
	}{
		{
			name:    "native main-cache-ttl-1h",
			payload: nativePayload,
			incoming: http.Header{
				"User-Agent":               {"claude-cli/2.1.321 (external, cli)"},
				"X-App":                    {"cli"},
				"Anthropic-Beta":           {"claude-code-20250219"},
				"X-Claude-Code-Session-Id": {"11111111-2222-4333-8444-555555555555"},
			},
			mainTTL: true,
		},
		{
			name:     "claude-code-cli profile",
			payload:  plainPayload,
			incoming: http.Header{"User-Agent": {"curl/8.0"}},
		},
	}
	for _, tc := range cases {
		for _, stream := range []bool{false, true} {
			name := tc.name + "/execute"
			if stream {
				name = tc.name + "/stream"
			}
			t.Run(name, func(t *testing.T) {
				var seenBody []byte
				var seenBeta string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					seenBody, _ = io.ReadAll(r.Body)
					seenBeta = r.Header.Get("Anthropic-Beta")
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = w.Write([]byte("event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"))
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-opus-4-6","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
				}))
				defer server.Close()

				cfg := &config.Config{}
				cfg.ClaudeCode.MainCacheTTL1h = tc.mainTTL
				cfg.Payload.Override = []config.PayloadRule{{
					Models: []config.PayloadModelRule{{Name: "claude-opus-4-6"}},
					Params: map[string]any{"max_tokens": 1},
				}}
				auth := &cliproxyauth.Auth{ID: "final-1h-beta", Metadata: claudeOAuthTestMetadata(), Attributes: map[string]string{
					"api_key": "sk-ant-oat-final-1h-beta", "base_url": server.URL,
				}}
				executor := NewClaudeExecutor(cfg)
				req := cliproxyexecutor.Request{Model: "claude-opus-4-6", Payload: tc.payload}
				opts := cliproxyexecutor.Options{
					Stream:          stream,
					SourceFormat:    sdktranslator.FormatClaude,
					OriginalRequest: tc.payload,
					Headers:         tc.incoming,
				}
				if stream {
					result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
					if errStream != nil {
						t.Fatalf("ExecuteStream() error = %v", errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatalf("stream chunk error = %v", chunk.Err)
						}
					}
				} else if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
					t.Fatalf("Execute() error = %v", errExecute)
				}

				if got := ojson.GetBytesNoCopy(seenBody, "max_tokens").Int(); got != 1 {
					t.Fatalf("max_tokens = %d, want 1 from the payload rule; body=%s", got, seenBody)
				}
				if !helps.IsClaudeProbeOrHelperRequest(seenBody) {
					t.Fatalf("final body is not probe shaped; body=%s", seenBody)
				}
				if !helps.ClaudePayloadHas1hTTL(seenBody) {
					t.Fatalf("final body lost the 1h ttl; body=%s", seenBody)
				}
				if !strings.Contains(seenBeta, claudeExtendedCacheTTLBeta) {
					t.Fatalf("Anthropic-Beta = %q, want %s for a 1h body", seenBeta, claudeExtendedCacheTTLBeta)
				}
			})
		}
	}
}
