package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexPolicyErrorEnvelopesAcrossTransports(t *testing.T) {
	for _, tc := range []struct{ name, payload string }{
		{"typeless", `{"error":{"code":"bio_policy","message":"blocked"},"sequence_number":7}`},
		{"body", `{"type":"error","body":{"error":{"code":"bio_policy","message":" "}},"sequence_number":7}`},
		{"typeless_body", `{"body":{"error":{"code":"bio_policy"}},"sequence_number":7}`},
		{"typeless_status", `{"status":400,"body":{"error":{"code":"bio_policy"}},"sequence_number":7}`},
		{"null_error", `{"type":"error","error":null,"body":{"error":{"code":"bio_policy"}},"sequence_number":7}`},
		{"failed_body", `{"type":"response.failed","body":{"error":{"code":"bio_policy"}},"sequence_number":7}`},
		{"typeless_response", `{"response":{"error":{"code":"bio_policy"}},"sequence_number":7}`},
		{"typeless_top_level", `{"code":"bio_policy","message":"blocked","sequence_number":7}`},
		{"typeless_top_level_status", `{"status":400,"code":"bio_policy","message":"blocked","sequence_number":7}`},
	} {
		for _, transport := range []string{"sse", "websocket"} {
			for _, mode := range []string{"execute", "stream", "buffered_stream"} {
				for _, format := range []string{"openai-response", "claude"} {
					t.Run(fmt.Sprintf("%s/%s/%s/%s", tc.name, transport, mode, format), func(t *testing.T) {
						created := `{"type":"response.created","response":{"id":"policy-test","model":"gpt-5.5"}}`
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							if transport == "sse" {
								w.Header().Set("Content-Type", "text/event-stream")
								_, _ = fmt.Fprintf(w, "data: %s\n\nevent: error\ndata: %s\n\n", created, tc.payload)
								return
							}
							conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
							if errUpgrade != nil {
								t.Error(errUpgrade)
								return
							}
							defer func() {
								if errClose := conn.Close(); errClose != nil {
									t.Error(errClose)
								}
							}()
							if _, _, errRead := conn.ReadMessage(); errRead != nil {
								t.Error(errRead)
								return
							}
							for _, payload := range []string{created, tc.payload} {
								if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(payload)); errWrite != nil {
									t.Error(errWrite)
									return
								}
							}
						}))
						defer server.Close()
						cfg := &config.Config{}
						cfg.Codex.StreamBootstrapBuffering = mode == "buffered_stream"
						var executor cliproxyauth.ProviderExecutor = NewCodexExecutor(cfg)
						if transport == "websocket" {
							executor = NewCodexWebsocketsExecutor(cfg)
						}
						auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}
						payload := []byte(`{"model":"gpt-5.5","input":"hello"}`)
						if format == "claude" {
							payload = []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}]}`)
						}
						req := cliproxyexecutor.Request{Model: "gpt-5.5", Payload: payload}
						opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString(format), Stream: mode != "execute"}
						var executionErr error
						if mode == "execute" {
							_, executionErr = executor.Execute(context.Background(), auth, req, opts)
						} else {
							result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
							executionErr = errStream
							if result != nil {
								for chunk := range result.Chunks {
									if chunk.Err != nil {
										executionErr = chunk.Err
									}
								}
							}
						}
						if executionErr == nil {
							t.Fatal("missing policy error")
						}
						if got := statusCodeFromTestError(t, executionErr); got != http.StatusBadRequest {
							t.Fatalf("status = %d: %v", got, executionErr)
						}
						if got := ojson.Get(executionErr.Error(), "error.code").String(); got != "bio_policy" {
							t.Fatalf("lost policy code: %v", executionErr)
						}
						if got := ojson.Get(executionErr.Error(), "error.message").String(); got == "" || got == " " {
							t.Fatalf("missing fallback message: %v", executionErr)
						}
						if got := ojson.Get(executionErr.Error(), "sequence_number").Int(); got != 7 {
							t.Fatalf("sequence = %d, want 7: %v", got, executionErr)
						}
						if !clienterror.IsRequestFault(400, executionErr) {
							t.Fatalf("policy error is not request scoped: %v", executionErr)
						}
					})
				}
			}
		}
	}
}

func TestCodexWebsocketStringErrorsPreserveMessage(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		for _, message := range []string{"This content was flagged for possible biological risk.", "Invalid signature in thinking block"} {
			t.Run(fmt.Sprintf("wrapped=%t/%s", wrapped, message), func(t *testing.T) {
				payload := ojson.NewDocString(`{"type":"error","status":400,"headers":{"x-request-id":"req-test"}}`)
				path := "error"
				if wrapped {
					path = "body.error"
				}
				_ = payload.Set(path, message)
				err, ok := parseCodexWebsocketError(payload.Bytes())
				if !ok || err == nil {
					t.Fatal("missing websocket error")
				}
				if got := ojson.Get(err.Error(), "error.message").String(); got != message {
					t.Fatalf("message = %q, want %q", got, message)
				}
				if got := err.(interface{ Headers() http.Header }).Headers().Get("X-Request-Id"); got != "req-test" {
					t.Fatalf("request id = %q", got)
				}
				if wrapped && ojson.Get(err.Error(), "body.error").String() != message {
					t.Fatalf("original body lost: %v", err)
				}
			})
		}
	}
}

func TestCodexTypelessErrorsPreserveCredentialStatus(t *testing.T) {
	for _, status := range []int{401, 402, 429} {
		payload := fmt.Sprintf(`{"status":%d,"error":{"type":"authentication_error","code":"invalid_request_error","message":"rejected"},"headers":{"retry-after":"7"}}`, status)
		err, ok := parseCodexWebsocketError([]byte(payload))
		if !ok || statusCodeFromTestError(t, err) != status {
			t.Fatalf("status %d: %v", status, err)
		}
		if clienterror.IsRequestFault(status, err) {
			t.Fatalf("credential status %d became a request fault", status)
		}
		if status == 429 {
			retry := err.(interface{ RetryAfter() *time.Duration }).RetryAfter()
			if retry == nil || *retry != 7*time.Second {
				t.Fatalf("retry-after = %v", retry)
			}
		}
	}
}
