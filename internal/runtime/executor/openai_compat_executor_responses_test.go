package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v3"
)

func TestOpenAICompatResponsesConfig(t *testing.T) {
	var model config.OpenAICompatibilityModel
	if err := yaml.Unmarshal([]byte("name: gpt-6.1-sol\nuse-responses-api: true\n"), &model); err != nil || !model.UseResponsesAPI {
		t.Fatalf("Responses option not decoded: %+v %v", model, err)
	}
}

func TestOpenAICompatResponsesAPI(t *testing.T) {
	for _, model := range []string{"gpt-6.1-terra", "gpt-6.1-sol"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", model, stream), func(t *testing.T) {
				var got []byte
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					got, _ = io.ReadAll(r.Body)
					if r.URL.Path != "/v1/responses" {
						t.Errorf("wrong upstream path: %s", r.URL.Path)
					}
					if r.Header.Get("Authorization") != "Bearer test-key" {
						t.Error("upstream auth missing")
					}
					response := `{"id":"resp_test","object":"response","status":"completed","model":"` + model + `","output":[{"type":"function_call","id":"fc_test","call_id":"call_test","name":"lookup","arguments":"{\"value\":7}"}],"usage":{"input_tokens":20,"output_tokens":5,"total_tokens":25}}`
					if !stream {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, response)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					for _, event := range []string{
						`{"type":"response.created","response":{"id":"resp_test","created_at":1,"model":"` + model + `"}}`,
						`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_test","type":"function_call","call_id":"call_test","name":"lookup","arguments":""}}`,
						`{"type":"response.function_call_arguments.delta","item_id":"fc_test","output_index":0,"delta":"{\"value\":7}"}`,
						`{"type":"response.output_item.done","output_index":0,"item":{"id":"fc_test","type":"function_call","call_id":"call_test","name":"lookup","arguments":"{\"value\":7}"}}`,
						`{"type":"response.completed","response":` + response + `}`,
					} {
						fmt.Fprintf(w, "event: %s\ndata: %s\n\n", gjson.Get(event, "type").String(), event)
					}
				}))
				defer server.Close()
				cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "test", Models: []config.OpenAICompatibilityModel{{Name: model, UseResponsesAPI: true}}}}}
				// Rules must run last: translation must not reintroduce filtered values
				// or replace the user-selected output limit.
				cfg.Payload.Filter = []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: model, Protocol: "openai-response"}}, Params: []string{"temperature"}}}
				cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: model, Protocol: "openai-response"}}, Params: map[string]any{"max_output_tokens": 768}}}
				e := NewOpenAICompatExecutor("test", cfg)
				auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL + "/v1", "api_key": "test-key", "compat_name": "test"}}
				payload := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"look up 7"}],"max_completion_tokens":1024,"temperature":0.2,"reasoning_effort":"low","tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"value":{"type":"integer"}},"required":["value"]}}}],"tool_choice":{"type":"function","function":{"name":"lookup"}},"stream":` + fmt.Sprint(stream) + `}`)
				req := cliproxyexecutor.Request{Model: model, Payload: payload}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: stream}
				var reply []byte
				if stream {
					result, err := e.ExecuteStream(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						reply = append(reply, chunk.Payload...)
					}
					if !strings.Contains(string(reply), `"finish_reason":"tool_calls"`) || !strings.Contains(string(reply), `call_test`) || !strings.Contains(string(reply), `lookup`) {
						t.Fatalf("tool stream not translated: %s", reply)
					}
				} else {
					result, err := e.Execute(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					reply = result.Payload
					if gjson.GetBytes(reply, "choices.0.message.tool_calls.0.id").String() != "call_test" || gjson.GetBytes(reply, "usage.total_tokens").Int() != 25 {
						t.Fatalf("tool response not translated: %s", reply)
					}
				}
				for path, want := range map[string]string{"model": model, "reasoning.effort": "low", "tools.0.name": "lookup", "tool_choice.name": "lookup"} {
					if gjson.GetBytes(got, path).String() != want {
						t.Errorf("%s mismatch: %s", path, got)
					}
				}
				if gjson.GetBytes(got, "temperature").Exists() || gjson.GetBytes(got, "stream_options").Exists() || gjson.GetBytes(got, "messages").Exists() || gjson.GetBytes(got, "max_output_tokens").Int() != 768 {
					t.Fatalf("invalid final Responses payload: %s", got)
				}
			})
		}
	}
}

func TestOpenAICompatResponsesToolResultAndStreamFailure(t *testing.T) {
	for _, mode := range []string{"tool-result", "truncated", "error"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if mode == "tool-result" {
					if !strings.Contains(string(body), `"type":"function_call_output"`) || !strings.Contains(string(body), `"output":"tool result"`) {
						t.Errorf("tool result lost: %s", body)
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if mode == "error" {
					fmt.Fprint(w, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"upstream failure\"}}}\n\n")
				} else {
					fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
				}
			}))
			defer server.Close()
			cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{Name: "test", Models: []config.OpenAICompatibilityModel{{Name: "gpt-6.1-sol", UseResponsesAPI: true}}}}}
			e := NewOpenAICompatExecutor("test", cfg)
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL, "compat_name": "test"}}
			req := cliproxyexecutor.Request{Model: "gpt-6.1-sol", Payload: []byte(`{"messages":[{"role":"user","content":"look up"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"tool result"}]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}
			if mode == "tool-result" {
				result, err := e.Execute(context.Background(), auth, req, opts)
				if err != nil || gjson.GetBytes(result.Payload, "choices.0.message.content").String() != "done" {
					t.Fatalf("tool round trip: %s %v", result.Payload, err)
				}
				return
			}
			result, err := e.ExecuteStream(context.Background(), auth, req, opts)
			if err != nil {
				t.Fatal(err)
			}
			failed := false
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					failed = true
				}
			}
			if !failed {
				t.Fatal("missing terminal/error event was accepted as success")
			}
		})
	}
}
