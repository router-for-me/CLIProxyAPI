package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorCommentaryConfigStreamAndNonStream(t *testing.T) {
	t.Setenv("CPA_VISIBLE_CODEX_COMMENTARY", "")
	for _, enabled := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("visible_%t/stream_%t", enabled, stream), func(t *testing.T) {
				var requestItems atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Error(errRead)
						return
					}
					requestItems.Store(int64(len(gjson.GetBytes(body, "input").Array())))
					w.Header().Set("Content-Type", "text/event-stream")
					_, errWrite := io.WriteString(w, `data: {"type":"response.created","response":{"id":"r","model":"gpt-6.1-sol"}}

data: {"type":"response.output_item.added","item":{"type":"message","role":"assistant","phase":"commentary"}}

data: {"type":"response.output_text.delta","delta":"Public progress"}

data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Public progress"}]}}

data: {"type":"response.output_item.added","item":{"type":"message","role":"assistant","phase":"final_answer"}}

data: {"type":"response.output_text.delta","delta":"Final answer"}

data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Final answer"}]}}

data: {"type":"response.completed","response":{"id":"r","model":"gpt-6.1-sol","status":"completed","output":[{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Public progress"}]},{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Final answer"}]}],"usage":{"input_tokens":5,"output_tokens":7}}}

`)
					if errWrite != nil {
						t.Error(errWrite)
					}
				}))
				t.Cleanup(server.Close)
				cfg := &config.Config{}
				cfg.Client.Claude.ShowCodexCommentary = enabled
				exec := NewCodexExecutor(cfg)
				auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}
				req := cliproxyexecutor.Request{Model: "gpt-6.1-sol", Payload: []byte(`{"model":"gpt-6.1-sol","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"Previous progress","signature":"cpa:commentary:v1"},{"type":"text","text":"Previous progress"}]},{"role":"user","content":"Status?"}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: stream}
				var text strings.Builder
				if stream {
					result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						for _, line := range strings.Split(string(chunk.Payload), "\n") {
							if !strings.HasPrefix(line, "data: ") {
								continue
							}
							item := gjson.Parse(strings.TrimPrefix(line, "data: "))
							if item.Get("delta.type").String() == "text_delta" {
								text.WriteString(item.Get("delta.text").String())
							}
						}
					}
				} else {
					result, err := exec.Execute(context.Background(), auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for _, block := range gjson.GetBytes(result.Payload, "content").Array() {
						if block.Get("type").String() == "text" {
							text.WriteString(block.Get("text").String())
						}
					}
					if gjson.GetBytes(result.Payload, "usage.output_tokens").Int() != 7 {
						t.Fatal("display mirroring changed output token accounting")
					}
				}
				if strings.Contains(text.String(), "Public progress") != enabled || !strings.Contains(text.String(), "Final answer") {
					t.Fatalf("visible=%t: unexpected text %q", enabled, text.String())
				}
				wantItems := 3
				if enabled {
					wantItems = 2
				}
				if gotItems := requestItems.Load(); gotItems != int64(wantItems) {
					t.Fatalf("request replay items=%d, want %d", gotItems, wantItems)
				}
			})
		}
	}
}
