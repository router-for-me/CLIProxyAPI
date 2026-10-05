package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestAccountingTransportFailuresRetainUsage(t *testing.T) {
	for _, transport := range []string{"http", "sse", "codex-ws", "xai-ws"} {
		t.Run(transport, func(t *testing.T) {
			tokens := `{"input_tokens":10,"output_tokens":6,"total_tokens":16,"input_tokens_details":{"cached_tokens":4},"output_tokens_details":{"reasoning_tokens":5},"prompt":"private-prompt"}`
			handler := func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Private-Header", "private-header")
				switch transport {
				case "http":
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = fmt.Fprintf(w, `{"error":{"message":"private-failure"},"usage":%s}`, tokens)
				case "sse":
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: {\"model\":\"served\",\"usage\":%s}\n\ndata: {\"error\":{\"message\":\"private-failure\",\"code\":\"429\"}}\n\n", tokens)
				default:
					conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
					if err != nil {
						t.Error(err)
						return
					}
					defer func() { _ = conn.Close() }()
					if _, _, errRead := conn.ReadMessage(); errRead != nil {
						t.Error(errRead)
						return
					}
					payload := fmt.Sprintf(`{"type":"response.failed","response":{"model":"served","usage":%s,"error":{"message":"private-failure","code":"server_error"}}}`, tokens)
					if transport == "xai-ws" {
						payload = fmt.Sprintf(`{"type":"error","status":429,"error":{"message":"private-failure"},"response":{"model":"served","usage":%s}}`, tokens)
					}
					if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(payload)); errWrite != nil {
						t.Error(errWrite)
					}
				}
			}
			server := httptest.NewServer(http.HandlerFunc(handler))
			defer server.Close()
			capture := &codexResponseModelUsageCapture{alias: t.Name(), records: make(chan usage.Record, 4)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), codexResponseModelNoopUsagePlugin{}) })
			ctx := usage.WithRequestedModelAlias(usage.WithTraceID(context.Background(), "parent"), t.Name())
			auth := &cliproxyauth.Auth{ID: "private-account", Provider: "openai", Attributes: map[string]string{"api_key": "private-key", "base_url": server.URL}}
			req := cliproxyexecutor.Request{Model: "gpt-5.5", Payload: []byte(`{"model":"gpt-5.5","input":"private-request-prompt","messages":[{"role":"user","content":"private-request-prompt"}]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
			var errExecute error
			switch transport {
			case "http":
				_, errExecute = NewOpenAICompatExecutor("openai", &config.Config{}).Execute(ctx, auth, req, opts)
			case "sse":
				var result *cliproxyexecutor.StreamResult
				result, errExecute = NewOpenAICompatExecutor("openai", &config.Config{}).ExecuteStream(ctx, auth, req, opts)
				if errExecute == nil {
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							errExecute = chunk.Err
						}
					}
				}
			case "codex-ws":
				_, errExecute = NewCodexWebsocketsExecutor(&config.Config{}).Execute(ctx, codexOAuthTestAuth(server.URL), req, opts)
			case "xai-ws":
				auth.Provider = "xai"
				var result *cliproxyexecutor.StreamResult
				result, errExecute = NewXAIWebsocketsExecutor(&config.Config{}).ExecuteStream(ctx, auth, req, opts)
				if errExecute == nil {
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							errExecute = chunk.Err
						}
					}
				}
			}
			if errExecute == nil {
				t.Fatal("expected upstream failure")
			}
			record := capture.await(t)
			event := usage.NewAccountingEvent(ctx, record)
			if event.Status != "failed" || event.ExecutionID != record.RequestID || event.TraceID != "parent" || event.Tokens == nil || event.Tokens.Breakdown.TotalTokens != 16 || event.Tokens.Breakdown.Output.NonReasoningTokens != 1 || event.Tokens.Evidence["input_tokens"] != 10 {
				t.Fatalf("event = %+v, tokens = %+v", event, event.Tokens)
			}
			data, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "private-") {
				t.Fatalf("unsafe event: %s", data)
			}
		})
	}
}
