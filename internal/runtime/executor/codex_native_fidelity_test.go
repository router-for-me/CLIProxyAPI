package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexNativeStreamFidelity(t *testing.T) {
	for _, source := range []sdktranslator.Format{sdktranslator.FormatCodex, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, sdktranslator.FormatOpenAI} {
		t.Run(source.String(), func(t *testing.T) { testCodexNativeStreamFidelity(t, source) })
	}
}

func testCodexNativeStreamFidelity(t *testing.T, source sdktranslator.Format) {
	t.Helper()
	for _, transport := range []string{"http", "websocket"} {
		for _, lite := range []string{"", "header", "metadata"} {
			for _, buffering := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/lite=%s/buffering=%t", transport, lite, buffering), func(t *testing.T) {
					metadata := `{"type":"codex.response.metadata","headers":{"x-models-etag":"models-v1","x-codex-turn-state":"turn-1","x-codex-safety-buffering-enabled":"true","x-codex-safety-buffering-faster-model":"fixture-model"},"future":{"ok":true}}`
					completed := `{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"future":{"ok":true},"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`
					events := []string{metadata, `{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}`, completed}
					captured := make(chan []byte, 1)
					capturedHeaders := make(chan http.Header, 1)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						capturedHeaders <- r.Header.Clone()
						if transport == "websocket" {
							upgrader := websocket.Upgrader{}
							conn, err := upgrader.Upgrade(w, r, nil)
							if err != nil {
								t.Error(err)
								return
							}
							defer func() { _ = conn.Close() }()
							_, body, errRead := conn.ReadMessage()
							if errRead != nil {
								t.Error(errRead)
								return
							}
							captured <- body
							for _, event := range events {
								if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(event)); errWrite != nil {
									t.Error(errWrite)
								}
							}
							return
						}
						body, errRead := io.ReadAll(r.Body)
						if errRead != nil {
							t.Error(errRead)
						}
						captured <- body
						w.Header().Set("Content-Type", "text/event-stream")
						for _, event := range events {
							_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
						}
					}))
					defer server.Close()
					payload := []byte(`{"model":"gpt-5.6-sol","input":[],"parallel_tool_calls":false}`)
					headers := http.Header{"Session-Id": {"session-1"}, "Thread-Id": {"thread-1"}}
					if lite == "header" {
						headers.Set(codexResponsesLiteHeader, "true")
					} else if lite == "metadata" {
						payload = []byte(`{"model":"gpt-5.6-sol","input":[],"parallel_tool_calls":false,"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"}}`)
					}
					cfg := &config.Config{Codex: config.CodexConfig{StreamBootstrapBuffering: buffering, DisableCodexCloaking: true}}
					executeStream := NewCodexExecutor(cfg).ExecuteStream
					if transport == "websocket" {
						executeStream = NewCodexWebsocketsExecutor(cfg).ExecuteStream
					}
					result, err := executeStream(context.Background(), codexTestAuth(server.URL), cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: payload}, cliproxyexecutor.Options{SourceFormat: source, ResponseFormat: sdktranslator.FormatCodex, Headers: headers, Stream: true})
					if err != nil {
						t.Fatal(err)
					}
					var terminal []byte
					var metadataEvents []string
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						for _, line := range bytes.Split(chunk.Payload, []byte("\n")) {
							data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
							if gjson.GetBytes(data, "type").String() == "codex.response.metadata" {
								metadataEvents = append(metadataEvents, string(data))
							}
							if gjson.GetBytes(data, "type").String() == "response.completed" {
								terminal = bytes.Clone(data)
							}
						}
					}
					body := <-captured
					upstreamHeaders := <-capturedHeaders
					native := lite != "" && (source == sdktranslator.FormatCodex || source == sdktranslator.FormatOpenAIResponse)
					if transport == "websocket" {
						wantLiteHeader := ""
						if native && lite == "header" {
							wantLiteHeader = "true"
						}
						if got := upstreamHeaders.Get(codexResponsesLiteHeader); got != wantLiteHeader {
							t.Errorf("upstream Lite header = %q, want %q", got, wantLiteHeader)
						}
						alias := headerValueCaseInsensitive(upstreamHeaders, "session_id")
						t.Logf("upstream session alias: %q", alias)
						if (alias == "") != native {
							t.Errorf("session alias = %q, native = %t", alias, native)
						}
					}
					t.Logf("downstream metadata: %q", metadataEvents)
					if len(metadataEvents) != 1 || metadataEvents[0] != metadata {
						t.Errorf("metadata event changed or duplicated: %q", metadataEvents)
					}
					t.Logf("upstream request: %s; downstream completion: %s", body, terminal)
					if native {
						if gjson.GetBytes(body, "instructions").Exists() {
							t.Errorf("native request gained instructions: %s", body)
						}
						if string(terminal) != completed {
							t.Errorf("native completion changed: %s", terminal)
						}
					} else if gjson.GetBytes(body, "instructions").Type != gjson.String || gjson.GetBytes(terminal, "response.output.0.id").String() != "msg_1" {
						t.Errorf("compatibility normalization/backfill lost: %s; %s", body, terminal)
					}
				})
			}
		}
	}
}

func TestCodexWebsocketLiteHeaderWithoutSessionHeaders(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, disableCloaking := range []bool{false, true} {
			cfg := &config.Config{Codex: config.CodexConfig{DisableCodexCloaking: disableCloaking}}
			headers := http.Header{}
			headers.Set(codexResponsesLiteHeader, "true")
			got := applyCodexWebsocketHeaders(context.Background(), nil, nil, "fixture-token", cfg, native, headers)
			want := ""
			if native {
				want = "true"
			}
			if value := got.Get(codexResponsesLiteHeader); value != want {
				t.Errorf("native=%t disableCloaking=%t: Lite header = %q, want %q", native, disableCloaking, value, want)
			}
		}
	}
}
