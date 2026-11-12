package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorClaudeStandaloneEffortFinalPayload(t *testing.T) {
	models := []config.PayloadModelRule{{Name: "gpt-6.1-sol", Protocol: "codex"}}
	for _, tt := range []struct {
		name       string
		payload    config.PayloadConfig
		wantEffort string
	}{
		{name: "standalone effort preserved", wantEffort: "high"},
		{
			name: "final override wins",
			payload: config.PayloadConfig{Override: []config.PayloadRule{
				{Models: models, Params: map[string]any{"reasoning.effort": "low"}},
			}},
			wantEffort: "low",
		},
		{
			name: "final filter is not restored",
			payload: config.PayloadConfig{Filter: []config.PayloadFilterRule{
				{Models: models, Params: []string{"reasoning.effort"}},
			}},
		},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", tt.name, stream), func(t *testing.T) {
				bodies := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Errorf("read upstream request: %v", errRead)
					}
					bodies <- body
					w.Header().Set("Content-Type", "text/event-stream")
					if _, errWrite := io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_effort\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-6.1-sol\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n"); errWrite != nil {
						t.Errorf("write upstream response: %v", errWrite)
					}
				}))
				t.Cleanup(server.Close)

				executor := NewCodexExecutor(&config.Config{Payload: tt.payload})
				req := cliproxyexecutor.Request{
					Model:   "gpt-6.1-sol",
					Payload: []byte(`{"model":"gpt-6.1-sol","max_tokens":256,"messages":[{"role":"user","content":"Reply exactly OK."}],"output_config":{"effort":"high"}}`),
				}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: stream}
				if stream {
					result, errStream := executor.ExecuteStream(context.Background(), codexOAuthTestAuth(server.URL), req, opts)
					if errStream != nil {
						t.Fatalf("ExecuteStream: %v", errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatalf("stream chunk: %v", chunk.Err)
						}
					}
				} else if _, errExecute := executor.Execute(context.Background(), codexOAuthTestAuth(server.URL), req, opts); errExecute != nil {
					t.Fatalf("Execute: %v", errExecute)
				}
				body := <-bodies
				effort := gjson.GetBytes(body, "reasoning.effort")
				if effort.String() != tt.wantEffort || (tt.wantEffort == "" && effort.Exists()) {
					t.Fatalf("upstream reasoning.effort = %s, want %q; body=%s", effort.Raw, tt.wantEffort, body)
				}
			})
		}
	}
}
