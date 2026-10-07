package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
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

const searchReplayFailure = "{\"error\":{\"type\":\"invalid_request_error\",\"message\":\"messages.1.content.0: Invalid `encrypted_content` in `search_result` block\"}}"
const searchReplayHistory = `{"model":"claude-opus-5-5","tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"Original question"},{"role":"assistant","content":[{"type":"server_tool_use","id":"srvtoolu_a","name":"web_search","input":{"query":"original search"}},{"type":"web_search_tool_result","tool_use_id":"srvtoolu_a","content":[{"type":"web_search_result","title":"Source title","url":"https://example.com/source","encrypted_content":"old-account-ciphertext"}]},{"type":"text","text":"Prior answer","citations":[{"type":"web_search_result_location","title":"Source title","url":"https://example.com/source","encrypted_index":"old-index","cited_text":"Readable quoted excerpt"},{"type":"char_location","document_index":0,"start_char_index":0,"end_char_index":4}]},{"type":"tool_use","id":"ordinary-tool","name":"read_file","input":{"path":"example.txt"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"ordinary-tool","content":"saved file result"},{"type":"text","text":"Follow-up question"}]}]}`

func TestClaudeSearchRecoveryExecutor(t *testing.T) {
	for _, tc := range []struct {
		name                                            string
		stream, compressed, rejectAgain, signed, policy bool
		status                                          int
		failure, body                                   string
		attempts                                        int
	}{
		{name: "stream continuation", stream: true, status: 400, failure: searchReplayFailure, body: searchReplayHistory, attempts: 2},
		{name: "nonstream continuation", status: 400, failure: searchReplayFailure, body: searchReplayHistory, attempts: 2},
		{name: "compressed rejection", compressed: true, status: 400, failure: searchReplayFailure, body: searchReplayHistory, attempts: 2},
		{name: "signed OAuth stream retry", signed: true, stream: true, status: 400, failure: searchReplayFailure, body: searchReplayHistory, attempts: 2},
		{name: "signed OAuth retry", signed: true, status: 400, failure: searchReplayFailure, body: searchReplayHistory, attempts: 2},
		{name: "payload rules observe recovered history", policy: true, status: 400, failure: searchReplayFailure, body: searchReplayHistory, attempts: 2},
		{name: "bounded retry", rejectAgain: true, status: 400, failure: searchReplayFailure, body: searchReplayHistory, attempts: 2},
		{name: "unrelated validation", status: 400, failure: `{"error":{"type":"invalid_request_error","message":"invalid model"}}`, body: searchReplayHistory, attempts: 1},
		{name: "rate limit", status: 429, failure: searchReplayFailure, body: searchReplayHistory, attempts: 1},
		{name: "no search history", status: 400, failure: searchReplayFailure, body: `{"messages":[{"role":"user","content":"hello"}]}`, attempts: 1},
		{name: "successful search replay untouched", status: 200, failure: `{"content":[]}`, body: searchReplayHistory, attempts: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempts := 0
			firstSession := ""
			apiKey := "synthetic-account-b"
			if tc.signed {
				apiKey = "sk-ant-oat-synthetic-account-b"
			}
			original := tc.body
			if tc.stream {
				original = strings.TrimSuffix(original, "}") + `,"stream":true}`
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if r.Header.Get("X-Api-Key") != apiKey && r.Header.Get("Authorization") != "Bearer "+apiKey {
					t.Error("account changed")
				}
				if attempts == 1 && strings.Contains(original, "old-account-ciphertext") && !strings.Contains(string(body), "old-account-ciphertext") {
					t.Error("original request was modified before rejection")
				}
				if tc.signed {
					session := r.Header.Get("X-Claude-Code-Session-Id")
					if attempts == 1 {
						firstSession = session
					}
					if session == "" || session != firstSession {
						t.Error("session identity changed")
					}
					if _, ok := claudeBillingCCHDigitsOffset(body); !ok {
						t.Error("missing CCH signature")
					}
					resigned, signErr := signAnthropicMessagesBody(body)
					if signErr != nil || !bytes.Equal(resigned, body) {
						t.Error("stale CCH signature")
					}
				}
				if attempts == 2 {
					if tc.policy && gjson.GetBytes(body, "max_tokens").Int() != 77 {
						t.Error("payload policy did not observe recovered history")
					}

					for _, want := range []string{"Original question", "Follow-up question", "Prior answer", "Source title", "Readable quoted excerpt", "example.com/source", "original search", "ordinary-tool", "saved file result", `"char_location"`, `"tools"`, `"web_search_20250305"`, "search again if needed"} {
						if !strings.Contains(string(body), want) {
							t.Errorf("lost history or contract: %s", want)
						}
					}
					for _, unwanted := range []string{"old-account-ciphertext", "old-index", `"server_tool_use"`, `"web_search_tool_result"`} {
						if strings.Contains(string(body), unwanted) {
							t.Errorf("retained invalid artifact: %s", unwanted)
						}
					}
					if !json.Valid(body) || r.ContentLength != int64(len(body)) {
						t.Error("invalid retry wire body")
					}
					if !tc.rejectAgain {
						if tc.stream {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
						} else {
							_, _ = io.WriteString(w, `{"id":"msg_test","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"Continued"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
						}
						return
					}
				}
				if tc.compressed {
					w.Header().Set("Content-Encoding", "gzip")
				}
				w.WriteHeader(tc.status)
				if tc.compressed {
					z := gzip.NewWriter(w)
					_, _ = z.Write([]byte(tc.failure))
					_ = z.Close()
				} else {
					_, _ = io.WriteString(w, tc.failure)
				}
			}))
			defer server.Close()
			auth := &cliproxyauth.Auth{ID: "synthetic-account-b", Provider: "claude", Attributes: map[string]string{"api_key": apiKey, "base_url": server.URL}}
			cfg := &config.Config{}
			if tc.signed {
				auth.Metadata = claudeOAuthTestMetadata()
			}
			if tc.policy {
				cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"messages.1.content.1.type": "text"}}}}, Params: map[string]any{"max_tokens": 77}}}
			}
			executor := NewClaudeExecutor(cfg)
			req := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: []byte(original)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
			var result []byte
			var err error
			if tc.stream {
				response, streamErr := executor.ExecuteStream(context.Background(), auth, req, opts)
				err = streamErr
				if err == nil {
					for chunk := range response.Chunks {
						if chunk.Err != nil {
							err = chunk.Err
						}
						result = append(result, chunk.Payload...)
					}
				}
			} else {
				response, runErr := executor.Execute(context.Background(), auth, req, opts)
				err = runErr
				result = response.Payload
			}
			if attempts != tc.attempts {
				t.Fatalf("attempts=%d want=%d; err=%v", attempts, tc.attempts, err)
			}
			if tc.status != 200 && (tc.attempts == 1 || tc.rejectAgain) {
				if err == nil {
					t.Fatal("expected original error")
				}
			} else if err != nil {
				t.Fatalf("continuation failed: %v %s", err, result)
			}
			if string(req.Payload) != original {
				t.Fatal("caller history mutated")
			}

		})
	}
}
