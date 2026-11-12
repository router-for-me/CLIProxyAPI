package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexSIWCNormalizesNativeResponsesEffort(t *testing.T) {
	const completed = `{"type":"response.completed","response":{"id":"resp_effort","object":"response","status":"completed","model":"gpt-6.1-sol","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`
	for _, mode := range []string{"http", "stream", "compact", "websocket", "websocket stream preparation"} {
		for _, tc := range []struct {
			name       string
			body       string
			override   bool
			wantEffort string
		}{
			{
				name:       "baseline none uses minimum effort",
				body:       `{"model":"gpt-6.1-sol","reasoning":{"effort":"none","summary":"auto"},"input":[{"role":"user","content":"hi"}]}`,
				wantEffort: "low",
			},
			{
				name:       "collapsed update cannot restore none",
				body:       `{"model":"gpt-6.1-sol","reasoning":{"effort":"high","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"none"}},{"role":"user","content":"hi"}]}`,
				wantEffort: "low",
			},
			{
				name:       "user payload override remains final",
				body:       `{"model":"gpt-6.1-sol","reasoning":{"effort":"high","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"none"}},{"role":"user","content":"hi"}]}`,
				override:   true,
				wantEffort: "none",
			},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				captured := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if websocket.IsWebSocketUpgrade(r) {
						upgrader := websocket.Upgrader{}
						conn, errUpgrade := upgrader.Upgrade(w, r, nil)
						if errUpgrade != nil {
							t.Errorf("upgrade websocket: %v", errUpgrade)
							return
						}
						defer func() {
							if errClose := conn.Close(); errClose != nil {
								t.Errorf("close websocket: %v", errClose)
							}
						}()
						_, body, errRead := conn.ReadMessage()
						if errRead != nil {
							t.Errorf("read websocket: %v", errRead)
							return
						}
						captured <- body
						if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(completed)); errWrite != nil {
							t.Errorf("write websocket: %v", errWrite)
						}
						return
					}
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Errorf("read request: %v", errRead)
						return
					}
					captured <- body
					if strings.HasSuffix(r.URL.Path, "/compact") {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, `{"id":"resp_effort","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: "+completed+"\n\n")
				}))
				defer server.Close()

				cfg := &config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}}
				if tc.override {
					protocol := "codex"
					if mode == "compact" {
						protocol = "openai-response"
					}
					cfg.Payload.Override = []config.PayloadRule{{
						Models: []config.PayloadModelRule{{Name: "gpt-6.1-sol", Protocol: protocol}},
						Params: map[string]any{"reasoning.effort": "none"},
					}}
				}
				auth := codexSIWCAuthForUpdateTest(server.URL)
				req := cliproxyexecutor.Request{Model: "gpt-6.1-sol", Payload: []byte(tc.body)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")}
				executor := NewCodexExecutor(cfg)
				var err error
				switch mode {
				case "stream":
					var result *cliproxyexecutor.StreamResult
					result, err = executor.ExecuteStream(context.Background(), auth, req, opts)
					if err == nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Errorf("stream chunk: %v", chunk.Err)
							}
						}
					}
				case "compact":
					opts.Alt = "responses/compact"
					_, err = executor.Execute(context.Background(), auth, req, opts)
				case "websocket":
					_, err = NewCodexWebsocketsExecutor(cfg).Execute(context.Background(), auth, req, opts)
				case "websocket stream preparation":
					prepared, errPrepare := NewCodexWebsocketsExecutor(cfg).prepareCodexWebsocketStream(context.Background(), auth, req, opts)
					err = errPrepare
					if err == nil {
						captured <- prepared.clientBody
					}
				default:
					_, err = executor.Execute(context.Background(), auth, req, opts)
				}
				if err != nil {
					t.Fatalf("execute request: %v", err)
				}
				select {
				case body := <-captured:
					assertCodexSIWCUpdateStripped(t, body, tc.wantEffort)
					if summary := ojson.GetBytes(body, "reasoning.summary").String(); summary != "auto" {
						t.Fatalf("reasoning.summary = %q, want auto; body=%s", summary, body)
					}
				default:
					t.Fatal("upstream request was not captured")
				}
			})
		}
	}
}
