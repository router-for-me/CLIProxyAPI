package executor

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestClaudeExecutor_Issue7000ForcedToolChoiceFallsBackToAuto(t *testing.T) {
	const tools = `[{"name":"lookup_weather","description":"Look up the weather for a city.","input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false},"cache_control":{"type":"ephemeral"}}]`
	const messages = `[{"role":"user","content":[{"type":"text","text":"What is the weather in Paris?"}]}]`

	choices := []struct {
		name string
		body string
	}{
		{name: "any", body: `{"type":"any","disable_parallel_tool_use":true}`},
		{name: "tool", body: `{"type":"tool","name":"lookup_weather","disable_parallel_tool_use":true}`},
	}
	for _, model := range []string{"claude-opus-5-5", "claude-sonnet-5-5"} {
		for _, choice := range choices {
			for _, method := range []string{"Execute", "ExecuteStream"} {
				t.Run(model+"/"+choice.name+"/"+method, func(t *testing.T) {
					stream := method == "ExecuteStream"
					var upstreamBody []byte
					transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
						body, errRead := io.ReadAll(req.Body)
						if errRead != nil {
							return nil, errRead
						}
						if errClose := req.Body.Close(); errClose != nil {
							return nil, errClose
						}
						upstreamBody = body
						contentType := "application/json"
						responseBody := `{"id":"msg_issue7000","type":"message","role":"assistant","model":"` + model + `","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`
						if stream {
							contentType = "text/event-stream"
							responseBody = strings.ReplaceAll(claudeIssue6698SSE, "claude-opus-5-5", model)
						}
						return &http.Response{
							StatusCode: http.StatusOK,
							Header:     http.Header{"Content-Type": {contentType}},
							Body:       io.NopCloser(strings.NewReader(responseBody)),
							Request:    req,
						}, nil
					})
					ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
					executor := NewClaudeExecutor(&config.Config{})
					auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-ant-api03-issue7000"}}
					payload := []byte(`{"model":"` + model + `","max_tokens":4096,"tools":` + tools + `,"tool_choice":` + choice.body + `,"messages":` + messages + `,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`)
					request := cliproxyexecutor.Request{Model: model, Payload: payload}
					options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: stream}
					if stream {
						result, errStream := executor.ExecuteStream(ctx, auth, request, options)
						if errStream != nil {
							t.Fatalf("ExecuteStream() error = %v", errStream)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Errorf("stream chunk error = %v", chunk.Err)
							}
						}
					} else if _, errExecute := executor.Execute(ctx, auth, request, options); errExecute != nil {
						t.Fatalf("Execute() error = %v", errExecute)
					}

					if !gjson.ValidBytes(upstreamBody) {
						t.Fatalf("expected a captured JSON upstream request, got %q", upstreamBody)
					}
					for _, assertion := range []struct {
						path string
						want any
					}{
						{path: "tool_choice.type", want: "auto"},
						{path: "tool_choice.disable_parallel_tool_use", want: true},
						{path: "thinking.type", want: "adaptive"},
						{path: "output_config.effort", want: "high"},
						{path: "tools", want: gjson.Parse(tools).Value()},
						{path: "messages", want: gjson.Parse(messages).Value()},
					} {
						if got := gjson.GetBytes(upstreamBody, assertion.path).Value(); !reflect.DeepEqual(got, assertion.want) {
							t.Errorf("upstream %s = %#v, want %#v", assertion.path, got, assertion.want)
						}
					}
					if got := gjson.GetBytes(upstreamBody, "tool_choice.name"); got.Exists() {
						t.Errorf("upstream tool_choice.name = %s, want absent after fallback to auto", got.Raw)
					}
				})
			}
		}
	}
}
