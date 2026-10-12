package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const claudeDisabledFallbackSSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

// A caller turning thinking off on an adaptive-only model gets the lowest effort,
// and cloaking must not make that fallback visible by adding progress updates.
func TestClaudeExecutor_AdaptiveOnlyThinkingOffKeepsThinkingHidden(t *testing.T) {
	sources := []struct {
		name    string
		format  sdktranslator.Format
		payload string
	}{
		{name: "claude", format: sdktranslator.FormatClaude, payload: `{"model":"MODEL","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"}}`},
		{name: "openai chat", format: sdktranslator.FormatOpenAI, payload: `{"model":"MODEL","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`},
		{name: "openai responses", format: sdktranslator.FormatOpenAIResponse, payload: `{"model":"MODEL","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"reasoning":{"effort":"none"}}`},
	}
	for _, model := range []string{"claude-opus-5-5", "claude-opus-5-5[1m]", "claude-fable-5-1"} {
		for _, source := range sources {
			t.Run(model+" "+source.name, func(t *testing.T) {
				var upstreamBody []byte
				transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					upstreamBody, _ = io.ReadAll(req.Body)
					contentType := "application/json"
					body := `{"id":"msg_test","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
					// Non-Claude sources are always sent upstream as a stream.
					if gjson.GetBytes(upstreamBody, "stream").Bool() {
						contentType = "text/event-stream"
						body = claudeDisabledFallbackSSE
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
				})
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
				executor := NewClaudeExecutor(&config.Config{})
				auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-disabled-fallback", "cloak_mode": "always"}}
				payload := strings.ReplaceAll(source.payload, "MODEL", model)
				request := cliproxyexecutor.Request{Model: model, Payload: []byte(payload)}
				if _, err := executor.Execute(ctx, auth, request, cliproxyexecutor.Options{SourceFormat: source.format}); err != nil {
					t.Fatalf("Execute() error = %v", err)
				}

				if got := gjson.GetBytes(upstreamBody, "thinking.type").String(); got != "adaptive" {
					t.Fatalf("upstream thinking.type = %q, want adaptive; body=%s", got, upstreamBody)
				}
				if got := gjson.GetBytes(upstreamBody, "output_config.effort").String(); got != "low" {
					t.Errorf("upstream output_config.effort = %q, want low", got)
				}
				if got := gjson.GetBytes(upstreamBody, "thinking.display").String(); got != "omitted" {
					t.Errorf("upstream thinking.display = %q, want omitted; body=%s", got, upstreamBody)
				}
			})
		}
	}
}
