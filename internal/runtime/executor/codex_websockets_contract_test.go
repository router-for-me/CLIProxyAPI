package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexWebsocketsExecutorPrewarmChainsOnSameConnection(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		stream bool
	}{
		{name: "execute"},
		{name: "stream", stream: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			captured := make(chan []byte, 2)
			var connections atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				connections.Add(1)
				conn, errUpgrade := upgrader.Upgrade(w, request, nil)
				if errUpgrade != nil {
					t.Errorf("upgrade websocket: %v", errUpgrade)
					return
				}
				defer func() { _ = conn.Close() }()

				for turn := 1; turn <= 2; turn++ {
					_, payload, errRead := conn.ReadMessage()
					if errRead != nil {
						t.Errorf("read websocket request %d: %v", turn, errRead)
						return
					}
					captured <- bytes.Clone(payload)

					responseID := "resp-warm"
					if turn == 2 {
						responseID = "resp-generated"
						itemDone := []byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg-generated","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"ok"}]}}`)
						if errWrite := conn.WriteMessage(websocket.TextMessage, itemDone); errWrite != nil {
							t.Errorf("write output item: %v", errWrite)
							return
						}
					}
					completed := []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}}`, responseID))
					if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
						t.Errorf("write completed response %d: %v", turn, errWrite)
						return
					}
				}
			}))
			t.Cleanup(server.Close)

			exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
			sessionID := "codex-prewarm-contract-" + testCase.name
			t.Cleanup(func() { exec.CloseExecutionSession(sessionID) })
			auth := &cliproxyauth.Auth{
				ID:       "codex-prewarm-auth",
				Provider: "codex",
				Attributes: map[string]string{
					"api_key":  "test",
					"base_url": server.URL,
				},
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FromString("openai-response"),
				ResponseFormat: sdktranslator.FromString("openai-response"),
				Headers:        http.Header{"User-Agent": []string{"codex-contract-test/1.0"}},
				Metadata: map[string]any{
					cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
				},
			}
			execute := func(payload string) error {
				t.Helper()
				req := cliproxyexecutor.Request{Model: "gpt-5.6-luna", Payload: []byte(payload)}
				if !testCase.stream {
					_, errExecute := exec.Execute(context.Background(), auth, req, opts)
					return errExecute
				}
				result, errExecute := exec.ExecuteStream(context.Background(), auth, req, opts)
				if errExecute != nil {
					return errExecute
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						return chunk.Err
					}
				}
				return nil
			}

			if errExecute := execute(`{"model":"gpt-5.6-luna","store":false,"generate":false,"input":[]}`); errExecute != nil {
				t.Fatalf("prewarm request failed: %v", errExecute)
			}
			if errExecute := execute(`{"model":"gpt-5.6-luna","store":false,"previous_response_id":"resp-warm","input":[]}`); errExecute != nil {
				t.Fatalf("generated request failed: %v", errExecute)
			}

			first := <-captured
			second := <-captured
			if got := gjson.GetBytes(first, "type").String(); got != "response.create" {
				t.Fatalf("prewarm type = %q, want response.create: %s", got, first)
			}
			if !gjson.GetBytes(first, "generate").Exists() || gjson.GetBytes(first, "generate").Bool() {
				t.Fatalf("prewarm generate was not false: %s", first)
			}
			if got := gjson.GetBytes(second, "previous_response_id").String(); got != "resp-warm" {
				t.Fatalf("follow-up previous_response_id = %q, want resp-warm: %s", got, second)
			}
			if input := gjson.GetBytes(second, "input"); !input.IsArray() || len(input.Array()) != 0 {
				t.Fatalf("follow-up input is not the empty incremental input: %s", second)
			}
			if got := connections.Load(); got != 1 {
				t.Fatalf("upstream websocket connections = %d, want 1", got)
			}
		})
	}
}

func TestCodexWebsocketsExecutorReconnectsAfterOfficialConnectionLimitError(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		stream bool
	}{
		{name: "execute"},
		{name: "stream", stream: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			captured := make(chan []byte, 2)
			var connections atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				connection := connections.Add(1)
				conn, errUpgrade := upgrader.Upgrade(w, request, nil)
				if errUpgrade != nil {
					t.Errorf("upgrade websocket: %v", errUpgrade)
					return
				}
				defer func() { _ = conn.Close() }()

				_, payload, errRead := conn.ReadMessage()
				if errRead != nil {
					t.Errorf("read websocket request: %v", errRead)
					return
				}
				captured <- bytes.Clone(payload)
				if connection == 1 {
					errorPayload := []byte(`{"type":"error","status":400,"error":{"type":"invalid_request_error","code":"websocket_connection_limit_reached","message":"Responses websocket connection limit reached (60 minutes). Create a new websocket connection to continue."}}`)
					if errWrite := conn.WriteMessage(websocket.TextMessage, errorPayload); errWrite != nil {
						t.Errorf("write connection-limit error: %v", errWrite)
					}
					return
				}
				completed := []byte(`{"type":"response.completed","response":{"id":"resp-recovered","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}}`)
				if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
					t.Errorf("write recovered response: %v", errWrite)
				}
			}))
			t.Cleanup(server.Close)

			exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
			sessionID := "codex-connection-limit-contract-" + testCase.name
			t.Cleanup(func() { exec.CloseExecutionSession(sessionID) })
			auth := &cliproxyauth.Auth{
				ID:       "codex-connection-limit-auth",
				Provider: "codex",
				Attributes: map[string]string{
					"api_key":  "test",
					"base_url": server.URL,
				},
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FromString("openai-response"),
				ResponseFormat: sdktranslator.FromString("openai-response"),
				Headers:        http.Header{"User-Agent": []string{"codex-contract-test/1.0"}},
				Metadata: map[string]any{
					cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
				},
			}
			execute := func(payload string) error {
				t.Helper()
				req := cliproxyexecutor.Request{Model: "gpt-5.6-luna", Payload: []byte(payload)}
				if !testCase.stream {
					_, errExecute := exec.Execute(context.Background(), auth, req, opts)
					return errExecute
				}
				result, errExecute := exec.ExecuteStream(context.Background(), auth, req, opts)
				if errExecute != nil {
					return errExecute
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						return chunk.Err
					}
				}
				return nil
			}

			firstErr := execute(`{"model":"gpt-5.6-luna","store":false,"previous_response_id":"resp-stale","input":[{"type":"message","id":"msg-incremental","role":"user","content":"continue"}]}`)
			if firstErr == nil {
				t.Fatal("connection-limit request error = nil")
			}
			withStatus, okStatus := firstErr.(interface{ StatusCode() int })
			if !okStatus || withStatus.StatusCode() != http.StatusBadRequest {
				t.Fatalf("connection-limit error = %T %v, want status 400", firstErr, firstErr)
			}
			if errExecute := execute(`{"model":"gpt-5.6-luna","store":false,"input":[{"type":"message","id":"msg-full","role":"user","content":"full replay"}]}`); errExecute != nil {
				t.Fatalf("full replay after reconnect failed: %v", errExecute)
			}

			first := <-captured
			second := <-captured
			if got := gjson.GetBytes(first, "previous_response_id").String(); got != "resp-stale" {
				t.Fatalf("first previous_response_id = %q, want resp-stale: %s", got, first)
			}
			if gjson.GetBytes(second, "previous_response_id").Exists() {
				t.Fatalf("recovered full request retained previous_response_id: %s", second)
			}
			if got := gjson.GetBytes(second, "input.0.id").String(); got != "msg-full" {
				t.Fatalf("recovered full request input = %q, want msg-full: %s", got, second)
			}
			if got := connections.Load(); got != 2 {
				t.Fatalf("upstream websocket connections = %d, want 2", got)
			}
		})
	}
}
