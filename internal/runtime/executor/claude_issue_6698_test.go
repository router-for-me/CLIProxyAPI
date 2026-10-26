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

const claudeIssue6698SSE = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func TestClaudeExecutor_Sonnet55ThinkingOffSendsBetweenTools(t *testing.T) {
	sources := []struct {
		name    string
		format  sdktranslator.Format
		payload string
	}{
		{name: "claude", format: sdktranslator.FormatClaude, payload: `{"model":"MODEL","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled","display":"summarized","block_binding":{"prefix_mismatch_behavior":"drop_block"}}}`},
		{name: "openai chat", format: sdktranslator.FormatOpenAI, payload: `{"model":"MODEL","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`},
		{name: "openai responses", format: sdktranslator.FormatOpenAIResponse, payload: `{"model":"MODEL","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"reasoning":{"effort":"none"}}`},
	}
	for _, model := range []string{"claude-sonnet-5-5", "claude-sonnet-5-5[1m]"} {
		for _, source := range sources {
			for _, stream := range []bool{false, true} {
				name := model + " " + source.name
				if stream {
					name += " stream"
				}
				t.Run(name, func(t *testing.T) {
					var upstreamBody []byte
					transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
						upstreamBody, _ = io.ReadAll(req.Body)
						contentType := "application/json"
						body := `{"id":"msg_test","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
						if gjson.GetBytes(upstreamBody, "stream").Bool() {
							contentType = "text/event-stream"
							body = claudeIssue6698SSE
						}
						return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
					})
					ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
					executor := NewClaudeExecutor(&config.Config{})
					auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-between-tools", "cloak_mode": "always"}}
					request := cliproxyexecutor.Request{Model: model, Payload: []byte(strings.ReplaceAll(source.payload, "MODEL", model))}
					options := cliproxyexecutor.Options{SourceFormat: source.format, Stream: stream}
					if stream {
						result, err := executor.ExecuteStream(ctx, auth, request, options)
						if err != nil {
							t.Fatalf("ExecuteStream() error = %v", err)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatalf("stream chunk error = %v", chunk.Err)
							}
						}
					} else if _, err := executor.Execute(ctx, auth, request, options); err != nil {
						t.Fatalf("Execute() error = %v", err)
					}

					if got := gjson.GetBytes(upstreamBody, "thinking").Raw; got != `{"type":"between_tools"}` {
						t.Fatalf("upstream thinking = %s, want {\"type\":\"between_tools\"}; body=%s", got, upstreamBody)
					}
					if got := gjson.GetBytes(upstreamBody, "output_config.effort"); got.Exists() {
						t.Errorf("upstream output_config.effort = %s, want absent", got.Raw)
					}
					if got := gjson.GetBytes(upstreamBody, "context_management"); got.Exists() {
						t.Errorf("upstream context_management = %s, want absent with between_tools", got.Raw)
					}
				})
			}
		}
	}
}

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
	for _, model := range []string{"claude-opus-5-5", "claude-opus-5-5[1m]", "claude-fable-5", "claude-fable-5-1"} {
		for _, source := range sources {
			t.Run(model+" "+source.name, func(t *testing.T) {
				var upstreamBody []byte
				transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					upstreamBody, _ = io.ReadAll(req.Body)
					contentType := "application/json"
					body := `{"id":"msg_test","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
					if gjson.GetBytes(upstreamBody, "stream").Bool() {
						contentType = "text/event-stream"
						body = claudeIssue6698SSE
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

func TestClaudeExecutor_AdaptiveOnlyGeminiBudgetAndAuto(t *testing.T) {
	for _, model := range []string{"claude-opus-4-7", "claude-opus-4-8", "claude-fable-5", "claude-fable-5-1"} {
		t.Run(model+" gemini budget 64000 maps to xhigh", func(t *testing.T) {
			var upstreamBody []byte
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				upstreamBody, _ = io.ReadAll(req.Body)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader(claudeIssue6698SSE)),
					Request:    req,
				}, nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			executor := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-gemini-budget", "cloak_mode": "always"}}
			payload := `{"model":"` + model + `","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":64000}}}`
			request := cliproxyexecutor.Request{Model: model, Payload: []byte(payload)}
			if _, err := executor.Execute(ctx, auth, request, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}); err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if got := gjson.GetBytes(upstreamBody, "thinking.type").String(); got != "adaptive" {
				t.Fatalf("upstream thinking.type = %q, want adaptive; body=%s", got, upstreamBody)
			}
			if gjson.GetBytes(upstreamBody, "thinking.budget_tokens").Exists() {
				t.Fatalf("upstream thinking.budget_tokens still present: %s", upstreamBody)
			}
			if got := gjson.GetBytes(upstreamBody, "output_config.effort").String(); got != "xhigh" {
				t.Fatalf("upstream output_config.effort = %q, want xhigh; body=%s", got, upstreamBody)
			}
		})

		t.Run(model+" gemini auto budget -1 stays plain adaptive", func(t *testing.T) {
			var upstreamBody []byte
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				upstreamBody, _ = io.ReadAll(req.Body)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader(claudeIssue6698SSE)),
					Request:    req,
				}, nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			executor := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-gemini-auto", "cloak_mode": "always"}}
			payload := `{"model":"` + model + `","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`
			request := cliproxyexecutor.Request{Model: model, Payload: []byte(payload)}
			if _, err := executor.Execute(ctx, auth, request, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini}); err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if got := gjson.GetBytes(upstreamBody, "thinking.type").String(); got != "adaptive" {
				t.Fatalf("upstream thinking.type = %q, want adaptive; body=%s", got, upstreamBody)
			}
			if gjson.GetBytes(upstreamBody, "thinking.budget_tokens").Exists() {
				t.Fatalf("upstream thinking.budget_tokens still present: %s", upstreamBody)
			}
			if got := gjson.GetBytes(upstreamBody, "output_config.effort"); got.Exists() {
				t.Fatalf("upstream output_config.effort = %s, want absent; body=%s", got.Raw, upstreamBody)
			}
		})
	}
}
