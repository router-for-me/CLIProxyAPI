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
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestClaudeExecutor_MainCacheTTL1hUpgradesNativeMainConversation(t *testing.T) {
	const userID = `{"device_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","account_uuid":"","session_id":"11111111-2222-4333-8444-555555555555"}`
	payload := []byte(`{"model":"claude-opus-4-6","system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}],"metadata":{"user_id":` + fmt.Sprintf("%q", userID) + `}}`)

	tests := []struct {
		name     string
		enabled  bool
		oauth    bool
		subagent bool
		stream   bool
		want1h   bool
	}{
		{name: "enabled main", enabled: true, oauth: true, want1h: true},
		{name: "enabled main stream", enabled: true, oauth: true, stream: true, want1h: true},
		{name: "disabled main", oauth: true},
		{name: "enabled subagent", enabled: true, oauth: true, subagent: true},
		{name: "enabled api key credential", enabled: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seenBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seenBody, _ = io.ReadAll(r.Body)
				if tt.stream {
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
			if tt.subagent {
				incoming.Set("X-Claude-Code-Agent-Id", "agent-sub-123")
			}
			if !helps.DetectClaudeCodeRequest(incoming, payload, false).Confirmed {
				t.Fatal("fixture must be classified as a native Claude Code request")
			}

			apiKey := "key-main-cache-ttl"
			var metadata map[string]any
			if tt.oauth {
				apiKey = "sk-ant-oat-main-cache-ttl"
				metadata = claudeOAuthTestMetadata()
			}
			cfg := &config.Config{}
			cfg.ClaudeCode.MainCacheTTL1h = tt.enabled
			auth := &cliproxyauth.Auth{ID: "main-cache-ttl", Metadata: metadata, Attributes: map[string]string{
				"api_key": apiKey, "base_url": server.URL,
			}}
			executor := NewClaudeExecutor(cfg)
			req := cliproxyexecutor.Request{Model: "claude-opus-4-6", Payload: payload}
			opts := cliproxyexecutor.Options{
				Stream:          tt.stream,
				SourceFormat:    sdktranslator.FormatClaude,
				OriginalRequest: payload,
				Headers:         incoming,
			}
			if tt.stream {
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

			markers := 0
			forEachClaudeCacheControlBlock(seenBody, func(path string, block ojson.Result) {
				cacheControl := block.Get("cache_control")
				if !cacheControl.IsObject() {
					return
				}
				markers++
				got := cacheControl.Get("ttl").String()
				if tt.want1h && got != "1h" {
					t.Errorf("%s ttl = %q, want 1h; body=%s", path, got, seenBody)
				}
				if !tt.want1h && got == "1h" {
					t.Errorf("%s upgraded to 1h unexpectedly; body=%s", path, seenBody)
				}
			})
			if markers == 0 {
				t.Fatalf("no cache_control markers reached upstream: %s", seenBody)
			}
		})
	}
}
