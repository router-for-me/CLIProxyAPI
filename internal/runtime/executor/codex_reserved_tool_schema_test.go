package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// Issue #6244: the upstream rejects reserved collaboration tools if their
// parameter schemas are changed, even when the prompt does not call a tool.
func TestCodexExecutorsPreserveCollaborationToolSchema(t *testing.T) {
	const tools = `[
		{"type":"namespace","name":"collaboration","description":"Agent tools","tools":[
			{"type":"function","name":"wait_agent","strict":false,"parameters":{"type":"object","properties":{"timeout_ms":{"type":"number"}},"additionalProperties":false}}
		]},
		{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"yield_time_ms":{"type":"number"}}}}
	]`
	const completed = `{"type":"response.completed","response":{"id":"resp-1","object":"response","status":"completed","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`

	for _, useWebsocket := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, additionalTools := range []bool{false, true} {
				name := fmt.Sprintf("websocket=%t/stream=%t/additional_tools=%t", useWebsocket, stream, additionalTools)
				t.Run(name, func(t *testing.T) {
					captured := make(chan []byte, 1)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if useWebsocket {
							upgrader := websocket.Upgrader{}
							conn, errUpgrade := upgrader.Upgrade(w, r, nil)
							if errUpgrade != nil {
								t.Errorf("upgrade websocket: %v", errUpgrade)
								return
							}
							defer func() { _ = conn.Close() }()
							_, body, errRead := conn.ReadMessage()
							if errRead != nil {
								t.Errorf("read websocket request: %v", errRead)
								return
							}
							captured <- bytes.Clone(body)
							if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(completed)); errWrite != nil {
								t.Errorf("write websocket response: %v", errWrite)
							}
							return
						}
						body, errRead := io.ReadAll(r.Body)
						if errRead != nil {
							t.Errorf("read HTTP request: %v", errRead)
							return
						}
						captured <- body
						w.Header().Set("Content-Type", "text/event-stream")
						if _, errWrite := fmt.Fprintf(w, "data: %s\n\n", completed); errWrite != nil {
							t.Errorf("write HTTP response: %v", errWrite)
						}
					}))
					defer server.Close()

					var executor cliproxyauth.ProviderExecutor = NewCodexExecutor(&config.Config{})
					if useWebsocket {
						executor = NewCodexWebsocketsExecutor(&config.Config{})
					}
					auth := &cliproxyauth.Auth{Provider: "codex", Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
					payload := fmt.Sprintf(`{"model":"gpt-6.1-sol","input":[{"role":"user","content":"Reply pong"}],"tools":%s}`, tools)
					toolsPath := "tools"
					if additionalTools {
						payload = fmt.Sprintf(`{"model":"gpt-6.1-sol","input":[{"type":"additional_tools","role":"developer","tools":%s},{"role":"user","content":"Reply pong"}]}`, tools)
						toolsPath = "input.0.tools"
					}
					req := cliproxyexecutor.Request{Model: "gpt-6.1-sol", Payload: []byte(payload)}
					opts := cliproxyexecutor.Options{
						SourceFormat: sdktranslator.FormatCodex,
						Headers:      http.Header{"User-Agent": []string{"codex_cli_rs/0.159.2"}},
						Stream:       stream,
					}
					if stream {
						result, errExecute := executor.ExecuteStream(context.Background(), auth, req, opts)
						if errExecute != nil {
							t.Fatalf("ExecuteStream: %v", errExecute)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatalf("stream error: %v", chunk.Err)
							}
						}
					} else if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
						t.Fatalf("Execute: %v", errExecute)
					}

					var body []byte
					select {
					case body = <-captured:
					default:
						t.Fatal("upstream did not receive a request")
					}
					var want, got any
					if errDecode := json.Unmarshal([]byte(gjson.Get(tools, "0").Raw), &want); errDecode != nil {
						t.Fatal(errDecode)
					}
					if errDecode := json.Unmarshal([]byte(gjson.GetBytes(body, toolsPath+".0").Raw), &got); errDecode != nil {
						t.Fatal(errDecode)
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("upstream reserved declaration changed: %s", gjson.GetBytes(body, toolsPath+".0").Raw)
					}
					if gotType := gjson.GetBytes(body, toolsPath+".1.parameters.properties.yield_time_ms.type").String(); gotType != "integer" {
						t.Fatalf("ordinary tool type = %q, want integer", gotType)
					}
				})
			}
		}
	}
}
