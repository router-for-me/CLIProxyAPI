package executor

import (
	"context"
	"fmt"
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
				if i > 0 && rule == "explicit" && gjson.GetBytes(body, "previous_interaction_id").String() != "explicit" {
					t.Errorf("explicit continuation lost: %s", body)
				}
				if i > 0 && rule == "override" {
					if gjson.GetBytes(body, "previous_interaction_id").String() != "configured" || gjson.GetBytes(body, "environment_id").String() != "configured-env" || gjson.GetBytes(body, "input.0.content").String() != "configured-input" {
						t.Errorf("payload override lost: %s", body)
					}
				}
				if i > 0 && rule == "filter" {
					for _, path := range []string{"previous_interaction_id", "environment_id", "input"} {
						if gjson.GetBytes(body, path).Exists() {
							t.Errorf("filtered %s restored: %s", path, body)
						}
					}
				}
				if i > 0 && rule == "baseline" {
					if got := gjson.GetBytes(body, "previous_interaction_id").String(); got != fmt.Sprintf("interaction_%d", i) {
						t.Errorf("round %d continuation=%q body=%s", i, got, body)
					}
					if got := gjson.GetBytes(body, "environment_id").String(); got != "env_1" {
						t.Errorf("round %d environment=%q", i, got)
					}
					if got := gjson.GetBytes(body, "input.#").Int(); got != 1 {
						t.Errorf("round %d replayed history: %s", i, body)
					}
				}
				input += fmt.Sprintf(`,{"type":"function_call","call_id":"call_%d","name":"read_file","arguments":"{\"path\":\"x\"}"},{"type":"function_call_output","call_id":"call_%d","output":"file content"}`, i+1, i+1)
				messages += fmt.Sprintf(`,{"role":"assistant","tool_calls":[{"id":"call_%d","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"x\"}"}}]},{"role":"tool","tool_call_id":"call_%d","content":"file content"}`, i+1, i+1)
			}
		})
	}
}
