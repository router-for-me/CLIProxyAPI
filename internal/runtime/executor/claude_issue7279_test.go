package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestClaudeExecutorIssue7279GeminiToolResultsAndPayloadRules(t *testing.T) {
	const model = "claude-sonnet-4"
	const response = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	const payload = `{"contents":[{"role":"user","parts":[{"text":"read a file"}]},{"role":"model","parts":[{"functionCall":{"name":"read_file","id":"toolu_1","args":{}}}]},{"role":"model","parts":[{"functionResponse":{"name":"read_file","id":"toolu_1","response":{"result":"original"}}}]}],"tools":[{"functionDeclarations":[{"name":"read_file","parameters":{"type":"object","properties":{}}}]}]}`

	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			var upstreamBody []byte
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				upstreamBody, _ = io.ReadAll(req.Body)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader(response)),
					Request:    req,
				}, nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			models := []config.PayloadModelRule{{Name: model, Match: []map[string]any{{"messages.2.role": "user", "messages.2.content.0.type": "tool_result"}}}}
			cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: models, Params: map[string]any{"messages.2.content.0.content": "configured"}}}}}
			executor := NewClaudeExecutor(cfg)
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-issue7279", "cloak_mode": "never"}}
			request := cliproxyexecutor.Request{Model: model, Payload: []byte(payload)}
			options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini, Stream: stream}
			if stream {
				result, errStream := executor.ExecuteStream(ctx, auth, request, options)
				if errStream != nil {
					t.Fatalf("ExecuteStream failed: %v", errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream failed: %v", chunk.Err)
					}
				}
			} else if _, errExecute := executor.Execute(ctx, auth, request, options); errExecute != nil {
				t.Fatalf("Execute failed: %v", errExecute)
			}

			messages := ojson.GetBytesNoCopy(upstreamBody, "messages").Array()
			if len(messages) != 3 || messages[1].Get("role").String() != "assistant" || messages[1].Get("content.0.type").String() != "tool_use" || messages[2].Get("role").String() != "user" || messages[2].Get("content.0.type").String() != "tool_result" {
				t.Fatalf("upstream messages do not separate the call and result: %s", ojson.GetBytesNoCopy(upstreamBody, "messages").Raw)
			}
			if got := messages[2].Get("content.0.tool_use_id").String(); got != "toolu_1" {
				t.Fatalf("tool result id = %q, want toolu_1", got)
			}
			if got := messages[2].Get("content.0.content").String(); got != "configured" {
				t.Fatalf("tool result content = %q, want final payload override", got)
			}
		})
	}
}
