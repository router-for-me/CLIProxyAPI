package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestClaudeExecutor_ThreadDoesNotInjectFallbacks(t *testing.T) {
	for _, model := range []string{"claude-opus-5-5", "claude-fable-5-1"} {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct {
				name            string
				extra           string
				wantFallback    bool
				configureThread bool
			}{
				{"create", `,"thread":{"type":"create"}`, false, false},
				{"continue", `,"thread":{"type":"continue","previous_message_id":"msg_previous"}`, false, false},
				{"unthreaded", "", true, false},
				{"explicit fallback", `,"thread":{"type":"create"},"fallbacks":[{"model":"caller-model"}]`, true, false},
				{"configured thread", "", false, true},
				{"explicit fallback with configured thread", `,"fallbacks":[{"model":"caller-model"}]`, true, true},
			} {
				t.Run(fmt.Sprintf("%s/stream=%t/%s", model, stream, tc.name), func(t *testing.T) {
					var seenBody []byte
					transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
						seenBody, _ = io.ReadAll(req.Body)
						body := `{"id":"msg_test","type":"message","role":"assistant","model":"` + model + `","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
						contentType := "application/json"
						if stream {
							body = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":" + body + "}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
							contentType = "text/event-stream"
						}
						return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
					})
					ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
					payload := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hello"}]` + tc.extra + `}`)
					auth := directClaudeOAuthAuth()
					cfg := &config.Config{}
					wantThread := gjson.GetBytes(payload, "thread").Raw
					if tc.configureThread {
						cfg.Payload.Override = []config.PayloadRule{{
							Models: []config.PayloadModelRule{{Name: model, Protocol: "claude"}},
							Params: map[string]any{"thread": map[string]any{"type": "create"}},
						}}
						wantThread = `{"type":"create"}`
					}
					exec := NewClaudeExecutor(cfg)
					req := cliproxyexecutor.Request{Model: model, Payload: payload}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
					if stream {
						resp, err := exec.ExecuteStream(ctx, auth, req, opts)
						if err != nil {
							t.Fatal(err)
						}
						for chunk := range resp.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					} else if _, err := exec.Execute(ctx, auth, req, opts); err != nil {
						t.Fatal(err)
					}
					if got := gjson.GetBytes(seenBody, "fallbacks").Exists(); got != tc.wantFallback {
						t.Errorf("upstream fallbacks present = %t, want %t", got, tc.wantFallback)
					}
					if got := gjson.GetBytes(seenBody, "thread").Raw; got != wantThread {
						t.Errorf("thread = %s, want %s", got, wantThread)
					}
					if strings.HasPrefix(tc.name, "explicit fallback") && gjson.GetBytes(seenBody, "fallbacks.0.model").String() != "caller-model" {
						t.Error("caller fallback was overwritten")
					}
				})
			}
		}
	}
}
