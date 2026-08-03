package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

func TestClaudeExecutor_RelaxedFablePreservesSystemLayoutAfterPayload(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, test := range []struct {
			name       string
			model      string
			callerText string
			params     map[string]any
			wantText   string
		}{
			{name: "original Fable", model: "claude-fable-5-1", callerText: "caller guidance", wantText: "caller guidance"},
			{name: "Payload selects Fable", model: "claude-sonnet-5", callerText: "caller guidance", params: map[string]any{"model": "claude-fable-5-1"}, wantText: "caller guidance"},
			{name: "caller reporting text", model: "claude-fable-5-1", callerText: claudeCodeFableReportingOutcomes, wantText: claudeCodeFableReportingOutcomes},
			{name: "Payload reporting text", model: "claude-fable-5-1", callerText: "caller guidance", params: map[string]any{"system.2.text": claudeCodeFableReportingOutcomes}, wantText: claudeCodeFableReportingOutcomes},
		} {
			t.Run(fmt.Sprintf("%s/stream=%t", test.name, stream), func(t *testing.T) {
				type capturedRequest struct {
					body    []byte
					headers http.Header
				}
				captured := make(chan capturedRequest, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Error(errRead)
					}
					captured <- capturedRequest{body: body, headers: r.Header.Clone()}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_relaxed\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-fable-5-1\",\"content\":[]}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"id":"msg_relaxed","type":"message","role":"assistant","model":"claude-fable-5-1","content":[]}`)
				}))
				defer server.Close()

				enabled := true
				cfg := &config.Config{ClaudeKey: []config.ClaudeKey{{
					APIKey:  "sk-ant-oat-relaxed-fable",
					BaseURL: server.URL,
					Cloak:   &config.CloakConfig{RelaxedSystemPrompt: &enabled},
				}}}
				if test.params != nil {
					cfg.Payload.Override = []config.PayloadRule{{
						Models: []config.PayloadModelRule{{Name: "*", Protocol: "claude"}},
						Params: test.params,
					}}
				}
				auth := &cliproxyauth.Auth{
					ID:       t.Name(),
					Metadata: claudeOAuthTestMetadata(),
					Attributes: map[string]string{
						"api_key":  "sk-ant-oat-relaxed-fable",
						"base_url": server.URL,
					},
				}
				payload, errMarshal := json.Marshal(map[string]any{
					"model":      test.model,
					"max_tokens": 1024,
					"thinking":   map[string]any{"type": "adaptive"},
					"system": []any{map[string]any{
						"type": "text", "text": test.callerText,
						"cache_control": map[string]any{"type": "ephemeral", "ttl": "1h"},
					}},
					"messages": []any{map[string]any{"role": "user", "content": "hello"}},
				})
				if errMarshal != nil {
					t.Fatal(errMarshal)
				}
				executor := NewClaudeExecutor(cfg)
				request := cliproxyexecutor.Request{Model: test.model, Payload: payload}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
				if stream {
					result, errExecute := executor.ExecuteStream(context.Background(), auth, request, opts)
					if errExecute != nil {
						t.Fatal(errExecute)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, errExecute := executor.Execute(context.Background(), auth, request, opts); errExecute != nil {
					t.Fatal(errExecute)
				}
				seen := <-captured
				blocks := gjson.GetBytes(seen.body, "system").Array()
				if len(blocks) != 3 || blocks[1].Get("text").String() != claudeCodeCLIIdentity || blocks[2].Get("text").String() != test.wantText {
					t.Fatalf("relaxed system layout changed: %s", gjson.GetBytes(seen.body, "system").Raw)
				}
				if !strings.HasPrefix(blocks[0].Get("text").String(), "x-anthropic-billing-header:") {
					t.Fatalf("missing billing identity: %s", blocks[0].Raw)
				}
				if got, want := gjson.GetBytes(seen.body, "messages").Raw, gjson.GetBytes(payload, "messages").Raw; got != want {
					t.Fatalf("relaxed messages changed: got %s, want %s", got, want)
				}
				if countCacheControls(seen.body) != 1 || blocks[2].Get("cache_control.ttl").String() != "1h" {
					t.Fatalf("caller cache ownership changed: %s", seen.body)
				}
				if gjson.GetBytes(seen.body, "fallbacks.0.model").String() != "claude-opus-5" || gjson.GetBytes(seen.body, "thinking.display").String() != "updates" {
					t.Fatalf("existing Fable transport fields were lost: %s", seen.body)
				}
				for _, beta := range []string{"server-side-fallback-2026-06-01", "thinking-display-updates-2026-08-18", "extended-cache-ttl-2025-04-11"} {
					if !strings.Contains(seen.headers.Get("Anthropic-Beta"), beta) {
						t.Fatalf("missing existing beta %q: %s", beta, seen.headers.Get("Anthropic-Beta"))
					}
				}
				signed, errSign := signAnthropicMessagesBody(seen.body)
				if errSign != nil || !bytes.Equal(signed, seen.body) {
					t.Fatalf("final body CCH is not stable: %v", errSign)
				}
			})
		}
	}
}
