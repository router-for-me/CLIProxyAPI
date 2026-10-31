package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// These opaque fixtures test the gateway contract, not Anthropic's classifier
// schema or verdict semantics. The proxy must not interpret or fabricate them.
func TestClaudeExecutor_AutoModeSafeguardsPassthrough(t *testing.T) {
	for _, profile := range []struct {
		name          string
		oauth, native bool
	}{
		{"api-key", false, false}, {"oauth", true, false}, {"native-api-key", false, true}, {"native-oauth", true, true},
	} {
		for _, streaming := range []bool{false, true} {
			for _, verdict := range []bool{false, true} {
				name := profile.name
				if streaming {
					name += "/stream"
				} else {
					name += "/json"
				}
				if verdict {
					name += "/verdict"
				} else {
					name += "/no-verdict"
				}
				t.Run(name, func(t *testing.T) {
					const model = "claude-opus-5-5"
					const toolID = "toolu_AutoMode-01"
					const safeguards = `[{"type":"dangerous_tool_use","future_request_field":{"opaque":"preserve"}}]`
					const results = `[{"tool_use_id":"toolu_AutoMode-01","future_verdict_field":{"opaque":"preserve"}}]`
					requestBody := []byte(`{"model":"claude-opus-5-5","max_tokens":64,"safeguards":` + safeguards + `,"future_request_field":{"opaque":true},"tools":[{"name":"Read","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"read the file"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_AutoMode-01","name":"Read","input":{"path":"README.md"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_AutoMode-01","content":"file contents"}]}]}`)
					resultField := ""
					if verdict {
						resultField = `,"safeguard_results":` + results
					}
					responseJSON := `{"id":"msg_safeguards","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"toolu_AutoMode-01","name":"Read","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":4,"output_tokens":2},"future_response_field":{"opaque":true}` + resultField + `}`
					responseSSE := "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_safeguards","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":4,"output_tokens":0}}}` + "\n\n" +
						"event: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_AutoMode-01","name":"Read","input":{}}}` + "\n\n" +
						"event: content_block_stop\ndata: " + `{"type":"content_block_stop","index":0}` + "\n\n" +
						"event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"tool_use"` + resultField + `},"usage":{"output_tokens":2},"future_response_field":{"opaque":true}` + resultField + `}` + "\n\n" +
						"event: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n"
					var seenBody []byte
					var seenHeaders http.Header
					calls := 0
					transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
						calls++
						var err error
						seenBody, err = io.ReadAll(req.Body)
						if err != nil {
							return nil, err
						}
						seenHeaders = req.Header.Clone()
						contentType, body := "application/json", responseJSON
						if streaming {
							contentType, body = "text/event-stream", responseSSE
						}
						return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
					})
					ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
					var auth *cliproxyauth.Auth
					if profile.oauth {
						auth = directClaudeOAuthAuth()
					} else {
						auth = directClaudeAPIKeyAuth()
					}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{
						"Anthropic-Beta": {"dangerous-tool-use-2026-09-03,future-safeguard-beta-2099-01-01"},
					}}
					if profile.native {
						opts.Headers.Set("User-Agent", "claude-cli/2.1.280 (external, cli)")
						opts.Headers.Set("X-App", "cli")
						opts.Headers.Set("Anthropic-Beta", "claude-code-20250219,"+opts.Headers.Get("Anthropic-Beta"))
						var err error
						requestBody, err = sjson.SetBytes(requestBody, "metadata.user_id", `{"device_id":"0000000000000000000000000000000000000000000000000000000000000000","account_uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","session_id":"11111111-2222-4333-8444-555555555555"}`)
						if err != nil {
							t.Fatal(err)
						}
						if !helps.DetectClaudeCodeRequest(opts.Headers, requestBody, false, &config.Config{}).Confirmed {
							t.Fatal("native fixture was not recognized as Claude Code")
						}
					}
					executor := NewClaudeExecutor(&config.Config{})
					request := cliproxyexecutor.Request{Model: model, Payload: requestBody}
					var output []byte
					if streaming {
						stream, err := executor.ExecuteStream(ctx, auth, request, opts)
						if err != nil {
							t.Fatal(err)
						}
						for chunk := range stream.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
							output = append(output, chunk.Payload...)
						}
					} else {
						response, err := executor.Execute(ctx, auth, request, opts)
						if err != nil {
							t.Fatal(err)
						}
						output = response.Payload
					}
					if calls != 1 {
						t.Fatalf("upstream calls = %d, want 1", calls)
					}
					if got := gjson.GetBytes(seenBody, "safeguards").Raw; got != safeguards {
						t.Fatalf("safeguards = %s, want %s", got, safeguards)
					}
					if !gjson.GetBytes(seenBody, "future_request_field.opaque").Bool() {
						t.Fatal("unknown request field lost")
					}
					for _, path := range []string{"messages.1.content.0.id", "messages.2.content.0.tool_use_id"} {
						if got := gjson.GetBytes(seenBody, path).String(); got != toolID {
							t.Fatalf("%s = %q, want %q", path, got, toolID)
						}
					}
					betas := strings.Split(helps.HeaderValueCaseInsensitive(seenHeaders, "Anthropic-Beta"), ",")
					for _, want := range []string{"dangerous-tool-use-2026-09-03", "future-safeguard-beta-2099-01-01"} {
						found := false
						for _, beta := range betas {
							if strings.TrimSpace(beta) == want {
								found = true
							}
						}
						if !found {
							t.Fatalf("missing beta %q in %v", want, betas)
						}
					}
					if streaming {
						if !bytes.Equal(output, []byte(responseSSE)) {
							t.Fatalf("stream changed: got %s, want %s", output, responseSSE)
						}
					} else {
						if got := gjson.GetBytes(output, "content.0.id").String(); got != toolID {
							t.Fatalf("response tool ID = %q", got)
						}
						if !gjson.GetBytes(output, "future_response_field.opaque").Bool() {
							t.Fatal("unknown response field lost")
						}
						if verdict {
							if got := gjson.GetBytes(output, "safeguard_results").Raw; got != results {
								t.Fatalf("safeguard_results = %s, want %s", got, results)
							}
						} else if gjson.GetBytes(output, "safeguard_results").Exists() {
							t.Fatal("proxy fabricated a verdict")
						}
					}
				})
			}
		}
	}
}
