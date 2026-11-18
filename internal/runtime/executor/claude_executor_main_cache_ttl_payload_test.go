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
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestClaudeExecutor_MainCacheTTL1hPayloadRuleWins(t *testing.T) {
	const userID = `{"device_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","account_uuid":"","session_id":"11111111-2222-4333-8444-555555555555"}`
	payload := []byte(`{"model":"claude-opus-4-6","system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}],"metadata":{"user_id":` + fmt.Sprintf("%q", userID) + `}}`)

	for _, stream := range []bool{false, true} {
		name := "execute"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			var seenBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seenBody, _ = io.ReadAll(r.Body)
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-opus-4-6","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
			}))
			defer server.Close()

			incoming := http.Header{
				"User-Agent":               {"claude-cli/2.1.321 (external, cli)"},
				"X-App":                    {"cli"},
				"Anthropic-Beta":           {"claude-code-20250219"},
				"X-Claude-Code-Session-Id": {"11111111-2222-4333-8444-555555555555"},
			}
			cfg := &config.Config{}
			cfg.ClaudeCode.MainCacheTTL1h = true
			cfg.Payload.Override = []config.PayloadRule{{
				Models: []config.PayloadModelRule{{Name: "claude-opus-4-6"}},
				Params: map[string]any{"messages.0.content.0.cache_control": map[string]any{"type": "ephemeral", "ttl": "5m"}},
			}}
			auth := &cliproxyauth.Auth{ID: "main-cache-ttl-rule", Metadata: claudeOAuthTestMetadata(), Attributes: map[string]string{
				"api_key": "sk-ant-oat-main-cache-ttl", "base_url": server.URL,
			}}
			executor := NewClaudeExecutor(cfg)
			req := cliproxyexecutor.Request{Model: "claude-opus-4-6", Payload: payload}
			opts := cliproxyexecutor.Options{
				Stream:          stream,
				SourceFormat:    sdktranslator.FormatClaude,
				OriginalRequest: payload,
				Headers:         incoming,
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

			doc := ojson.ParseBytes(seenBody)
			if got := doc.Get("messages.0.content.0.cache_control.ttl").String(); got != "5m" {
				t.Errorf("payload rule ttl = %q, want 5m; body=%s", got, seenBody)
			}
			if got := doc.Get("system.1.cache_control.ttl").String(); got != "1h" {
				t.Errorf("untouched system ttl = %q, want 1h; body=%s", got, seenBody)
			}
		})
	}
}
