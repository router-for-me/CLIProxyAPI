package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexWebsocketToolArgumentPolicy(t *testing.T) {
	for _, mode := range []string{"stream", "buffered", "duplex"} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/enabled=%v", mode, enabled), func(t *testing.T) {
				events := []string{
					`{"type":"response.created","response":{"id":"resp_policy"}}`,
					`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_policy","type":"function_call","call_id":"call_policy","name":"exec_command","arguments":"{\"count\":2500.0}"}}`,
					`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_policy","delta":"{\"count\":2500.0}"}`,
					`{"type":"response.function_call_arguments.done","output_index":0,"item_id":"fc_policy","arguments":"{\"count\":2500.0}"}`,
					`{"type":"response.output_item.done","output_index":0,"item":{"id":"fc_policy","type":"function_call","call_id":"call_policy","name":"exec_command","arguments":"{\"count\":2500.0}"}}`,
					`{"type":"response.output_item.done","output_index":1,"item":{"id":"ct_policy","type":"custom_tool_call","call_id":"patch_policy","name":"apply_patch","input":"+timeout = 2500.0"}}`,
					`{"type":"response.completed","response":{"id":"resp_policy","output":[{"id":"fc_policy","type":"function_call","call_id":"call_policy","name":"exec_command","arguments":"{\"count\":2500.0}"},{"id":"ct_policy","type":"custom_tool_call","call_id":"patch_policy","name":"apply_patch","input":"+timeout = 2500.0"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
				}
				upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, errUpgrade := upgrader.Upgrade(w, r, nil)
					if errUpgrade != nil {
						t.Errorf("upgrade: %v", errUpgrade)
						return
					}
					defer func() { _ = conn.Close() }()
					if _, _, errRead := conn.ReadMessage(); errRead != nil {
						t.Errorf("read request: %v", errRead)
						return
					}
					for _, event := range events {
						if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(event)); errWrite != nil {
							t.Errorf("write fixture: %v", errWrite)
							return
						}
					}
					// Keep a duplex connection open until cancellation releases it.
					if mode == "duplex" {
						_, _, _ = conn.ReadMessage()
					}
				}))
				defer server.Close()
				cfg := &config.Config{Codex: config.CodexConfig{StreamBootstrapBuffering: mode == "buffered", ResponseSteering: mode == "duplex"}}
				exec := NewCodexWebsocketsExecutor(cfg)
				auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "fixture-key", "base_url": server.URL}}
				ctx, cancel := context.WithCancel(sdktranslator.WithCodexToolArgumentNormalization(context.Background(), enabled))
				defer cancel()
				ctx = core.WithDownstreamWebsocket(ctx)
				if mode == "duplex" {
					ctx = core.WithWebsocketInput(ctx, make(chan core.WebsocketInput))
				}
				result, errExecute := exec.ExecuteStream(ctx, auth, core.Request{Model: "gpt-5-codex", Payload: []byte(`{"model":"gpt-5-codex","input":[]}`)}, core.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse})
				if errExecute != nil {
					t.Fatal(errExecute)
				}
				want := `{"count":2500.0}`
				if enabled {
					want = `{"count":2500}`
				}
				for i, original := range events {
					select {
					case chunk, ok := <-result.Chunks:
						if !ok || chunk.Err != nil {
							t.Fatalf("event %d: closed=%v error=%v", i, !ok, chunk.Err)
						}
						eventType := gjson.GetBytes(chunk.Payload, "type").String()
						if eventType != gjson.Get(original, "type").String() {
							t.Fatalf("event %d: unexpected payload %s", i, chunk.Payload)
						}
						switch i {
						case 0, 1, 2, 5:
							if string(chunk.Payload) != original {
								t.Errorf("untouched event %d changed: %s", i, chunk.Payload)
							}
						case 3:
							if got := gjson.GetBytes(chunk.Payload, "arguments").String(); got != want {
								t.Errorf("argument-done=%s; want %s", got, want)
							}
						case 4:
							if got := gjson.GetBytes(chunk.Payload, "item.arguments").String(); got != want {
								t.Errorf("item-done=%s; want %s", got, want)
							}
						case 6:
							if got := gjson.GetBytes(chunk.Payload, "response.output.0.arguments").String(); got != want {
								t.Errorf("terminal arguments=%s; want %s", got, want)
							}
							if got := gjson.GetBytes(chunk.Payload, "response.output.1.input").String(); got != "+timeout = 2500.0" {
								t.Errorf("terminal custom text changed: %s", got)
							}
						}
					case <-time.After(5 * time.Second):
						t.Fatalf("timed out on event %d", i)
					}
				}
				cancel()
				for range result.Chunks {
				}
			})
		}
	}
}
