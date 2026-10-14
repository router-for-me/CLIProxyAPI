package executor

import (
	"context"
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

// TestClaudeExecutor_PayloadDefaultMaxTokensKeepsThinkingBudgetBelowLimit is the
// CPA-005 regression: a payload.default max_tokens applied after thinking must
// not leave thinking.budget_tokens >= max_tokens, which Anthropic rejects.
func TestClaudeExecutor_PayloadDefaultMaxTokensKeepsThinkingBudgetBelowLimit(t *testing.T) {
	const model = "claude-sonnet-4-5-20250929"
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"" + model + "\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "fixture-key", "base_url": server.URL}}
	payload := []byte(`{"model":"` + model + `","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`)

	run := func(t *testing.T, executor *ClaudeExecutor, stream bool) []byte {
		t.Helper()
		seenBody = nil
		req := cliproxyexecutor.Request{Model: model, Payload: payload}
		opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAI, OriginalRequest: payload, Stream: stream}
		if stream {
			result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
			if errStream != nil {
				t.Fatalf("ExecuteStream() error = %v", errStream)
			}
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatalf("ExecuteStream() chunk error = %v", chunk.Err)
				}
			}
		} else if _, errExec := executor.Execute(context.Background(), auth, req, opts); errExec != nil {
			t.Fatalf("Execute() error = %v", errExec)
		}
		return seenBody
	}
	withDefault := func(params map[string]any) *ClaudeExecutor {
		return NewClaudeExecutor(&config.Config{Payload: config.PayloadConfig{Default: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{Name: model, Protocol: "claude"}},
			Params: params,
		}}}})
	}

	for _, stream := range []bool{false, true} {
		name := map[bool]string{false: "execute", true: "stream"}[stream]
		t.Run(name+"/budget clamped below payload default", func(t *testing.T) {
			body := run(t, withDefault(map[string]any{"max_tokens": 8000}), stream)
			if got := gjson.GetBytes(body, "max_tokens").Int(); got != 8000 {
				t.Fatalf("max_tokens = %d, want 8000; body=%s", got, body)
			}
			if got := gjson.GetBytes(body, "thinking.type").String(); got != "enabled" {
				t.Fatalf("thinking.type = %q, want enabled; body=%s", got, body)
			}
			if got := gjson.GetBytes(body, "thinking.budget_tokens").Int(); got != 7999 {
				t.Fatalf("thinking.budget_tokens = %d, want 7999; body=%s", got, body)
			}
		})
		t.Run(name+"/thinking disabled when limit is below model minimum", func(t *testing.T) {
			body := run(t, withDefault(map[string]any{"max_tokens": 1000}), stream)
			if got := gjson.GetBytes(body, "max_tokens").Int(); got != 1000 {
				t.Fatalf("max_tokens = %d, want 1000; body=%s", got, body)
			}
			if got := gjson.GetBytes(body, "thinking.type").String(); got != "disabled" {
				t.Fatalf("thinking.type = %q, want disabled; body=%s", got, body)
			}
			if gjson.GetBytes(body, "thinking.budget_tokens").Exists() {
				t.Fatalf("thinking.budget_tokens must be removed; body=%s", body)
			}
		})
		t.Run(name+"/configured thinking values are not overwritten", func(t *testing.T) {
			executor := NewClaudeExecutor(&config.Config{Payload: config.PayloadConfig{
				Default: []config.PayloadRule{{
					Models: []config.PayloadModelRule{{Name: model, Protocol: "claude"}},
					Params: map[string]any{"max_tokens": 8000},
				}},
				Override: []config.PayloadRule{{
					Models: []config.PayloadModelRule{{Name: model, Protocol: "claude"}},
					Params: map[string]any{"thinking.budget_tokens": 9000},
				}},
			}})
			body := run(t, executor, stream)
			// Payload configuration is the final semantic barrier: an operator
			// rule targeting thinking must reach upstream unchanged.
			if got := gjson.GetBytes(body, "thinking.budget_tokens").Int(); got != 9000 {
				t.Fatalf("thinking.budget_tokens = %d, want configured 9000; body=%s", got, body)
			}
		})
	}
}
