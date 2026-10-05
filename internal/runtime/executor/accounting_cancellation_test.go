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
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const cancellationCreated = `{"type":"response.created","response":{"id":"resp_cancel","status":"in_progress","model":"served"}}`

func TestAccountingCancelledStreamIsInterrupted(t *testing.T) {
	for _, transport := range []string{"codex-sse", "codex-ws", "xai-ws", "openai-compat-sse", "claude-sse"} {
		t.Run(transport, func(t *testing.T) {
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if transport == "codex-ws" || transport == "xai-ws" {
					conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer func() { _ = conn.Close() }()
					if _, _, errRead := conn.ReadMessage(); errRead != nil {
						return
					}
					if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(cancellationCreated)); errWrite != nil {
						return
					}
					close(started)
					for {
						if _, _, errRead := conn.ReadMessage(); errRead != nil {
							return
						}
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				event := cancellationCreated
				if transport == "openai-compat-sse" {
					event = `{"id":"chatcmpl","object":"chat.completion.chunk","model":"served","choices":[{"index":0,"delta":{"content":"hi"}}]}`
				}
				if transport == "claude-sse" {
					_, _ = fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"served\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
				} else {
					_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
				}
				w.(http.Flusher).Flush()
				close(started)
				<-r.Context().Done()
			}))
			defer server.Close()

			capture := &codexResponseModelUsageCapture{alias: t.Name(), records: make(chan usage.Record, 4)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), codexResponseModelNoopUsagePlugin{}) })

			ctx, cancel := context.WithCancel(usage.WithRequestedModelAlias(context.Background(), t.Name()))
			defer cancel()
			req := cliproxyexecutor.Request{Model: "gpt-5.5", Payload: []byte(`{"model":"gpt-5.5","input":"hello"}`)}
			opts := cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatOpenAIResponse}
			var result *cliproxyexecutor.StreamResult
			var err error
			switch transport {
			case "codex-sse":
				result, err = NewCodexExecutor(&config.Config{}).ExecuteStream(ctx, codexOAuthTestAuth(server.URL), req, opts)
			case "codex-ws":
				result, err = NewCodexWebsocketsExecutor(&config.Config{}).ExecuteStream(ctx, codexOAuthTestAuth(server.URL), req, opts)
			case "xai-ws":
				auth := &cliproxyauth.Auth{ID: "acct", Provider: "xai", Attributes: map[string]string{"api_key": "key", "base_url": server.URL}}
				result, err = NewXAIWebsocketsExecutor(&config.Config{}).ExecuteStream(ctx, auth, req, opts)
			case "claude-sse":
				auth := &cliproxyauth.Auth{ID: "acct", Provider: "claude", Attributes: map[string]string{"api_key": "key", "base_url": server.URL}}
				result, err = NewClaudeExecutor(&config.Config{}).ExecuteStream(ctx, auth, req, cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatClaude})
			case "openai-compat-sse":
				auth := &cliproxyauth.Auth{ID: "acct", Provider: "openai", Attributes: map[string]string{"api_key": "key", "base_url": server.URL}}
				result, err = NewOpenAICompatExecutor("openai", &config.Config{}).ExecuteStream(ctx, auth, req, opts)
			}
			if err != nil {
				t.Fatal(err)
			}

			select {
			case <-result.Chunks:
			case <-time.After(5 * time.Second):
				t.Fatal("stream never produced output")
			}
			<-started
			cancel()
			for range result.Chunks {
			}

			record := capture.await(t)
			event := usage.NewAccountingEvent(ctx, record)
			if event.Status != "interrupted" {
				t.Fatalf("a cancelled stream must be one interrupted event, got %+v", event)
			}
			// Only Claude reports usage before the cancellation, and only that usage is kept.
			if transport == "claude-sse" {
				if event.Tokens == nil || event.Tokens.Input != 1 || event.Tokens.Output != 1 {
					t.Fatalf("reported usage was not kept: %+v", event.Tokens)
				}
			} else if event.Tokens != nil {
				t.Fatalf("tokens were invented for an attempt that reported none: %+v", event.Tokens)
			}
			select {
			case extra := <-capture.records:
				t.Fatalf("cancellation published a second record: %+v", extra)
			case <-time.After(200 * time.Millisecond):
			}
		})
	}
}
