package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestGeminiInteractionsResponsesReplaysThoughtBeforeToolCalls(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream bool
		filter bool
	}{
		{name: "stream", stream: true},
		{name: "non-stream"},
		{name: "payload-filter", stream: true, filter: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			thoughtSignature := testNativeGemini3ThoughtSignature()
			const call = `{"type":"function_call","id":"call_1","name":"lookup_color","arguments":{"object":"pear"}}`
			requests := make(chan []byte, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Error(errRead)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				requests <- body
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"i1\"}}\n\n"+
						"data: {\"event_type\":\"step.start\",\"index\":0,\"step\":{\"type\":\"thought\"}}\n\n"+
						"data: {\"event_type\":\"step.delta\",\"index\":0,\"delta\":{\"type\":\"thought_signature\",\"signature\":%q}}\n\n"+
						"data: {\"event_type\":\"step.stop\",\"index\":0}\n\n"+
						"data: {\"event_type\":\"step.start\",\"index\":1,\"step\":%s}\n\n"+
						"data: {\"event_type\":\"step.stop\",\"index\":1}\n\n"+
						"data: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"i1\",\"status\":\"requires_action\"}}\n\n", thoughtSignature, call)
				} else {
					_, _ = fmt.Fprintf(w, `{"id":"i1","status":"requires_action","steps":[{"type":"thought","signature":%q},%s]}`, thoughtSignature, call)
				}
			}))
			defer server.Close()

			const model = "custom-model"
			cfg := &config.Config{}
			if tc.filter {
				cfg.Payload.Filter = []config.PayloadFilterRule{{
					Models: []config.PayloadModelRule{{Name: model, Protocol: "interactions"}},
					Params: []string{"input.1.signature"},
				}}
			}
			exec := NewGeminiInteractionsExecutor(cfg)
			auth := &cliproxyauth.Auth{Provider: "gemini-interactions", Attributes: map[string]string{"base_url": server.URL, "api_key": "test-key"}}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
			execute := func(payload []byte) ojson.Result {
				t.Helper()
				req := cliproxyexecutor.Request{Model: model, Payload: payload}
				if !tc.stream {
					result, errExecute := exec.Execute(context.Background(), auth, req, opts)
					if errExecute != nil {
						t.Fatal(errExecute)
					}
					return ojson.GetBytes(result.Payload, "output")
				}
				result, errExecute := exec.ExecuteStream(context.Background(), auth, req, opts)
				if errExecute != nil {
					t.Fatal(errExecute)
				}
				var output ojson.Result
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
					item := ojson.ParseBytes(geminiInteractionsSSEPayload(chunk.Payload))
					if item.Get("type").String() == "response.completed" {
						output = item.Get("response.output")
					}
				}
				return output
			}

			payload := ojson.NewDocString(`{
				"input":[{"type":"message","role":"user","content":"Look up the color of a pear."}],
				"tools":[{"type":"function","name":"lookup_color","parameters":{"type":"object"}}],
				"include":["reasoning.encrypted_content"],"store":false
			}`)
			output := execute(payload.Bytes()).Array()
			<-requests
			if len(output) != 2 || output[0].Get("encrypted_content").String() != thoughtSignature || output[1].Get("call_id").String() != "call_1" {
				t.Fatalf("first response didn't return the signed reasoning and its tool call: %v", output)
			}

			_ = payload.SetRaw("input.-1", `{"type":"reasoning","encrypted_content":"gAAAAother-provider"}`)
			_ = payload.SetRaw("input.-1", fmt.Sprintf(`{"type":"reasoning","encrypted_content":%q}`, signature.GeminiSkipThoughtSignatureValidator))
			for _, item := range output {
				_ = payload.SetRaw("input.-1", item.Raw)
			}
			_ = payload.SetRaw("input.-1", `{"type":"function_call_output","call_id":"call_1","output":"green"}`)
			execute(payload.Bytes())

			body := ojson.ParseBytes(<-requests)
			steps := body.Get("input").Array()
			if len(steps) != 4 || steps[1].Get("type").String() != "thought" ||
				steps[2].Get("type").String() != "function_call" || steps[2].Get("id").String() != "call_1" ||
				steps[3].Get("type").String() != "function_result" || steps[3].Get("call_id").String() != "call_1" {
				t.Fatalf("second request lost the expected order of thought, call and result: %s", body.Raw)
			}
			wantSignature := thoughtSignature
			if tc.filter {
				wantSignature = ""
			}
			if got := steps[1].Get("signature"); got.String() != wantSignature || got.Exists() == tc.filter {
				t.Fatalf("replayed thought signature = %s, want %q", got.Raw, wantSignature)
			}
			if steps[1].Get("content").Exists() || body.Get("previous_interaction_id").Exists() {
				t.Fatalf("expected a native thought step in stateless replay: %s", body.Raw)
			}
		})
	}
}

func TestIssue5190StatelessTwoToolRounds(t *testing.T) {
	for _, rule := range []string{"baseline", "override", "filter", "explicit"} {
		t.Run(rule, func(t *testing.T) { testIssue5190ToolRounds(t, rule) })
	}
}

func testIssue5190ToolRounds(t *testing.T, rule string) {
	for _, tc := range []struct {
		stream bool
		format sdktranslator.Format
	}{{false, sdktranslator.FormatOpenAI}, {true, sdktranslator.FormatOpenAI}, {false, sdktranslator.FormatOpenAIResponse}, {true, sdktranslator.FormatOpenAIResponse}} {
		stream := tc.stream
		t.Run(fmt.Sprint(tc.format, stream), func(t *testing.T) {
			requests := make(chan []byte, 3)
			round := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Error(errRead)
					return
				}
				requests <- body
				round++
				step := fmt.Sprintf(`{"type":"function_call","id":"call_%d","name":"external_read_file","arguments":{"path":"x"}}`, round)
				interaction := fmt.Sprintf(`{"id":"interaction_%d","environment_id":"env_1","status":"requires_action","steps":[%s]}`, round, step)
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"interaction_%d\",\"environment_id\":\"env_1\"}}\n\ndata: {\"event_type\":\"step.start\",\"index\":0,\"step\":%s}\n\ndata: {\"event_type\":\"interaction.completed\",\"interaction\":%s}\n\n", round, step, interaction)
				} else {
					_, _ = w.Write([]byte(interaction))
				}
			}))
			defer server.Close()
			cfg := &config.Config{}
			models := []config.PayloadModelRule{{Name: "antigravity*", Protocol: "interactions", Exist: []string{"previous_interaction_id"}}}
			if rule == "override" {
				cfg.Payload.Override = []config.PayloadRule{{Models: models, Params: map[string]any{"previous_interaction_id": "configured", "environment_id": "configured-env", "input": []any{map[string]any{"type": "user_input", "content": "configured-input"}}}}}
			}
			if rule == "filter" {
				cfg.Payload.Filter = []config.PayloadFilterRule{{Models: models, Params: []string{"previous_interaction_id", "environment_id", "input"}}}
			}
			exec := NewGeminiInteractionsExecutor(cfg)
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "gemini-interactions", Attributes: map[string]string{"api_key": "key", "base_url": server.URL}}
			messages := `{"role":"user","content":"read two files"}`
			input := messages
			opts := cliproxyexecutor.Options{SourceFormat: tc.format, ResponseFormat: tc.format, Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: t.Name()}}
			for i := 0; i < 3; i++ {
				req := cliproxyexecutor.Request{Model: "antigravity-preview-05-2026", Payload: []byte(`{"messages":[` + messages + `],"tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}]}`)}
				if tc.format == sdktranslator.FormatOpenAIResponse {
					req.Payload = []byte(`{"input":[` + input + `],"tools":[{"type":"function","name":"read_file","parameters":{"type":"object"}}]}`)
				}
				if i > 0 && rule == "explicit" {
					req.Payload = append([]byte(`{"previous_response_id":"explicit",`), req.Payload[1:]...)
				}
				if stream {
					result, errExecute := exec.ExecuteStream(context.Background(), auth, req, opts)
					if errExecute != nil {
						t.Fatal(errExecute)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else {
					if _, errExecute := exec.Execute(context.Background(), auth, req, opts); errExecute != nil {
						t.Fatal(errExecute)
					}
				}
				body := <-requests
				if i > 0 && rule == "explicit" && ojson.GetBytes(body, "previous_interaction_id").String() != "explicit" {
					t.Errorf("explicit continuation lost: %s", body)
				}
				if i > 0 && rule == "override" {
					if ojson.GetBytes(body, "previous_interaction_id").String() != "configured" || ojson.GetBytes(body, "environment_id").String() != "configured-env" || ojson.GetBytes(body, "input.0.content").String() != "configured-input" {
						t.Errorf("payload override lost: %s", body)
					}
				}
				if i > 0 && rule == "filter" {
					for _, path := range []string{"previous_interaction_id", "environment_id", "input"} {
						if ojson.GetBytes(body, path).Exists() {
							t.Errorf("filtered %s restored: %s", path, body)
						}
					}
				}
				if i > 0 && rule == "baseline" {
					if got := ojson.GetBytes(body, "previous_interaction_id").String(); got != fmt.Sprintf("interaction_%d", i) {
						t.Errorf("round %d continuation=%q body=%s", i, got, body)
					}
					if got := ojson.GetBytes(body, "environment_id").String(); got != "env_1" {
						t.Errorf("round %d environment=%q", i, got)
					}
					if got := ojson.GetBytes(body, "input.#").Int(); got != 1 {
						t.Errorf("round %d replayed history: %s", i, body)
					}
				}
				input += fmt.Sprintf(`,{"type":"function_call","call_id":"call_%d","name":"read_file","arguments":"{\"path\":\"x\"}"},{"type":"function_call_output","call_id":"call_%d","output":"file content"}`, i+1, i+1)
				messages += fmt.Sprintf(`,{"role":"assistant","tool_calls":[{"id":"call_%d","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"x\"}"}}]},{"role":"tool","tool_call_id":"call_%d","content":"file content"}`, i+1, i+1)
			}
		})
	}
}
