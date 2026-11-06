package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexClaudeModelSelectionMinimumOutputBudget(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			requests := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Errorf("read upstream request: %v", errRead)
					return
				}
				requests <- body
				if ojson.GetBytes(body, "max_output_tokens").Int() < 16 {
					http.Error(w, `Invalid 'max_output_tokens': integer below minimum value`, http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, errWrite := io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-5.6-sol\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hi\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
				if errWrite != nil {
					t.Errorf("write upstream response: %v", errWrite)
				}
			}))
			t.Cleanup(server.Close)

			executor := NewCodexExecutor(&config.Config{})
			req := cliproxyexecutor.Request{
				Model:   "gpt-5.6-sol",
				Payload: []byte(`{"model":"gpt-5.6-sol","max_tokens":1,"messages":[{"role":"user","content":[{"type":"text","text":"Hi","cache_control":{"type":"ephemeral"}}]}]}`),
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

			body := <-requests
			if got := ojson.GetBytes(body, "max_output_tokens").Int(); got != 16 {
				t.Fatalf("upstream max_output_tokens = %d, want 16; body=%s", got, body)
			}
		})
	}
}

func TestCodexClaudeOutputBudgetBoundariesAndPayloadRules(t *testing.T) {
	modelRule := []config.PayloadModelRule{{Name: "gpt-5.6-sol", Protocol: "codex"}}
	matchedRule := []config.PayloadModelRule{{
		Name: "gpt-5.6-sol", Protocol: "codex", Match: []map[string]any{{"max_output_tokens": 16}},
	}}
	cases := []struct {
		name    string
		field   string
		from    sdktranslator.Format
		payload config.PayloadConfig
		want    string
	}{
		{name: "one", field: `,"max_tokens":1`, want: "16"},
		{name: "fifteen", field: `,"max_tokens":15`, want: "16"},
		{name: "sixteen", field: `,"max_tokens":16`, want: "16"},
		{name: "above minimum", field: `,"max_tokens":17`, want: "17"},
		{name: "normal budget", field: `,"max_tokens":1024`, want: "1024"},
		{name: "absent"},
		{name: "fallback field", field: `,"max_output_tokens":1`, want: "16"},
		{name: "zero remains invalid", field: `,"max_tokens":0`, want: "0"},
		{name: "negative remains invalid", field: `,"max_tokens":-1`, want: "-1"},
		{name: "fractional input is not normalized", field: `,"max_tokens":1.5`, want: "1"},
		{name: "non-numeric input", field: `,"max_tokens":"1"`},
		{
			name: "override matches normalized value", field: `,"max_tokens":1`, want: "8",
			payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: matchedRule, Params: map[string]any{"max_output_tokens": 8}}}},
		},
		{
			name: "final filter wins", field: `,"max_tokens":1`,
			payload: config.PayloadConfig{Filter: []config.PayloadFilterRule{{Models: modelRule, Params: []string{"max_output_tokens"}}}},
		},
		{name: "native responses unchanged", field: `,"max_output_tokens":1`, from: sdktranslator.FormatOpenAIResponse, want: "1"},
	}
	for _, tc := range cases {
		for _, useWebsocket := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/websocket=%t/stream=%t", tc.name, useWebsocket, stream), func(t *testing.T) {
					requests := make(chan []byte, 1)
					completed := `{"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.6-sol","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hi"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if useWebsocket {
							upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
							conn, errUpgrade := upgrader.Upgrade(w, r, nil)
							if errUpgrade != nil {
								t.Errorf("upgrade: %v", errUpgrade)
								return
							}
							defer func() {
								if errClose := conn.Close(); errClose != nil {
									t.Errorf("close websocket: %v", errClose)
								}
							}()
							_, body, errRead := conn.ReadMessage()
							if errRead != nil {
								t.Errorf("read websocket request: %v", errRead)
								return
							}
							requests <- body
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
						requests <- body
						w.Header().Set("Content-Type", "text/event-stream")
						if _, errWrite := io.WriteString(w, "data: "+completed+"\n\n"); errWrite != nil {
							t.Errorf("write HTTP response: %v", errWrite)
						}
					}))
					t.Cleanup(server.Close)

					cfg := &config.Config{Payload: tc.payload}
					var executor cliproxyauth.ProviderExecutor = NewCodexExecutor(cfg)
					if useWebsocket {
						executor = NewCodexWebsocketsExecutor(cfg)
					}
					from := tc.from
					if from == "" {
						from = sdktranslator.FormatClaude
					}
					payload := `{"model":"gpt-5.6-sol"` + tc.field + `,"messages":[{"role":"user","content":"Hi"}]}`
					if from == sdktranslator.FormatOpenAIResponse {
						payload = `{"model":"gpt-5.6-sol"` + tc.field + `,"input":"Hi"}`
					}
					req := cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: []byte(payload)}
					opts := cliproxyexecutor.Options{SourceFormat: from, Stream: stream}
					auth := codexOAuthTestAuth(server.URL)
					auth.ID = "codex-output-budget-test"
					if stream {
						result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
						if errStream != nil {
							t.Fatalf("ExecuteStream: %v", errStream)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatalf("stream chunk: %v", chunk.Err)
							}
						}
					} else if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
						t.Fatalf("Execute: %v", errExecute)
					}
					body := <-requests
					if got := ojson.GetBytes(body, "max_output_tokens").Raw; got != tc.want {
						t.Fatalf("upstream max_output_tokens = %q, want %q; body=%s", got, tc.want, body)
					}
				})
			}
		}
	}
}
