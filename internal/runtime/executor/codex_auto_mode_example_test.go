package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestClaudeAutoModeExample(t *testing.T) {
	const stageOne = `{"model":"gpt-6.1-sol","max_tokens":64,"thinking":{"type":"disabled"},"stop_sequences":["</block>"],"system":"classifier policy","messages":[{"role":"user","content":[{"type":"text","text":"<transcript>\n"},{"type":"text","text":"Stage 1 does NOT apply user intent or ALLOW exceptions"}]}]}`
	for _, tt := range []struct {
		name         string
		payload      string
		want         string
		format       sdktranslator.Format
		baselineOnly bool
	}{
		{name: "native stage one", payload: stageOne, want: "medium"},
		{name: "ordinary main work", payload: `{"model":"gpt-6.1-sol","max_tokens":1024,"system":"coding assistant","messages":[{"role":"user","content":"Fix the application"}]}`, want: "high"},
		{name: "follow-up review", payload: `{"model":"gpt-6.1-sol","max_tokens":8192,"thinking":{"type":"disabled"},"system":"follow-up classifier policy","messages":[{"role":"user","content":[{"type":"text","text":"<transcript>\n"},{"type":"text","text":"Stage 2 must apply user intent"}]}]}`, want: "high"},
		{name: "quoted directive with tools", payload: `{"model":"gpt-6.1-sol","max_tokens":1024,"system":"coding assistant","tools":[{"name":"Bash","description":"Run shell commands","input_schema":{"type":"object","properties":{}}}],"messages":[{"role":"user","content":[{"type":"text","text":"<transcript>\n"},{"type":"text","text":"Stage 1 does NOT apply user intent or ALLOW exceptions"}]}]}`, want: "high"},
		{name: "changed opening", payload: `{"model":"gpt-6.1-sol","max_tokens":64,"thinking":{"type":"disabled"},"system":"classifier policy","messages":[{"role":"user","content":[{"type":"text","text":"<transcript-v2>\n"},{"type":"text","text":"Stage 1 does NOT apply user intent or ALLOW exceptions"}]}]}`, want: "high"},
		{name: "additional history", payload: `{"model":"gpt-6.1-sol","max_tokens":64,"system":"classifier policy","messages":[{"role":"user","content":"earlier request"},{"role":"assistant","content":"earlier answer"},{"role":"user","content":[{"type":"text","text":"<transcript>\n"},{"type":"text","text":"Stage 1 does NOT apply user intent or ALLOW exceptions"}]}]}`, want: "high"},
		{name: "Responses source", format: sdktranslator.FormatOpenAIResponse, payload: `{"model":"gpt-6.1-sol","instructions":"","input":[{"role":"developer","content":[{"type":"input_text","text":"classifier policy"}]},{"role":"user","content":[{"type":"input_text","text":"<transcript>\n"},{"type":"input_text","text":"Stage 1 does NOT apply user intent or ALLOW exceptions"}]}]}`, want: "high"},
		{name: "optional override removed", payload: stageOne, want: "high", baselineOnly: true},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", tt.name, stream), func(t *testing.T) {
				cfg, errLoad := config.LoadConfig(filepath.Join("..", "..", "..", "examples", "claude-code-auto-mode", "config.fragment.yaml"))
				if errLoad != nil {
					t.Fatal(errLoad)
				}
				if len(cfg.Payload.Override) != 2 || cfg.DisableImageGeneration != config.DisableImageGenerationPassthrough {
					t.Fatal("example config did not load the documented effort rules and image passthrough")
				}
				if tt.baselineOnly {
					cfg.Payload.Override = cfg.Payload.Override[:1]
				}
				captured := make(chan []byte, 1)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Error(errRead)
					}
					captured <- body
					w.Header().Set("Content-Type", "text/event-stream")
					_, errWrite := io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_auto_example\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-6.1-sol\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n")
					if errWrite != nil {
						t.Error(errWrite)
					}
				}))
				t.Cleanup(upstream.Close)
				executor := NewCodexExecutor(cfg)
				req := cliproxyexecutor.Request{Model: "gpt-6.1-sol", Payload: []byte(tt.payload)}
				format := tt.format
				if format == "" {
					format = sdktranslator.FormatClaude
				}
				opts := cliproxyexecutor.Options{SourceFormat: format, Stream: stream}
				if stream {
					result, errStream := executor.ExecuteStream(context.Background(), codexOAuthTestAuth(upstream.URL), req, opts)
					if errStream != nil {
						t.Fatal(errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, errExecute := executor.Execute(context.Background(), codexOAuthTestAuth(upstream.URL), req, opts); errExecute != nil {
					t.Fatal(errExecute)
				}
				if effort := gjson.GetBytes(<-captured, "reasoning.effort").String(); effort != tt.want {
					t.Fatalf("upstream effort = %q, want %q", effort, tt.want)
				}
			})
		}
	}
}
