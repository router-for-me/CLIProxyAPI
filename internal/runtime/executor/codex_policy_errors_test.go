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
			t.Fatalf("websocket status %d: %v", status, err)
		}
		if clienterror.IsRequestFault(status, err) {
			t.Fatalf("websocket credential status %d became a request fault", status)
		}
		if status == 429 {
			retry := err.(interface{ RetryAfter() *time.Duration }).RetryAfter()
			if retry == nil || *retry != 7*time.Second {
				t.Fatalf("retry-after = %v", retry)
			}
		}

		sseErr, _, okSSE := codexTerminalFailureErr([]byte(payload))
		if !okSSE || statusCodeFromTestError(t, sseErr) != status {
			t.Fatalf("sse status %d: %v", status, sseErr)
		}
		if clienterror.IsRequestFault(statusCodeFromTestError(t, sseErr), sseErr) {
			t.Fatalf("sse credential status %d became a request fault", status)
		}
	}

	untypedRateLimit := []byte(`{"status":429,"error":{"message":"Too many concurrent requests"}}`)
	sseRateLimitErr, _, ok := codexTerminalFailureErr(untypedRateLimit)
	if !ok || statusCodeFromTestError(t, sseRateLimitErr) != http.StatusTooManyRequests {
		t.Fatalf("untyped 429 sse status = %v (ok=%v)", sseRateLimitErr, ok)
	}

	blankScalarWS, okWS := parseCodexWebsocketError([]byte(`{"type":"error","status":503,"error":"   "}`))
	if !okWS || blankScalarWS == nil {
		t.Fatal("missing blank scalar websocket error")
	}
	if got := ojson.Get(blankScalarWS.Error(), "error.message").String(); got != http.StatusText(http.StatusServiceUnavailable) {
		t.Fatalf("blank scalar fallback message = %q, want %q", got, http.StatusText(http.StatusServiceUnavailable))
	}

	for _, nestedPayload := range []string{
		`{"body":{"status":429,"error":{"message":"Too many concurrent requests"}},"headers":{"retry-after":"9","x-request-id":"req-body-429"}}`,
		`{"error":{"status_code":429,"message":"Too many concurrent requests"},"headers":{"retry-after":"9","x-request-id":"req-err-429"}}`,
	} {
		nestedErr, okNested := parseCodexWebsocketError([]byte(nestedPayload))
		if !okNested || statusCodeFromTestError(t, nestedErr) != http.StatusTooManyRequests {
			t.Fatalf("nested websocket 429 = %v (ok=%v)", nestedErr, okNested)
		}
		retry := nestedErr.(interface{ RetryAfter() *time.Duration }).RetryAfter()
		if retry == nil || *retry != 9*time.Second {
			t.Fatalf("nested websocket retry-after = %v, want 9s", retry)
		}
		if gotReqID := nestedErr.(interface{ Headers() http.Header }).Headers().Get("X-Request-Id"); gotReqID == "" {
			t.Fatalf("nested websocket headers lost: %#v", nestedErr)
		}
	}

	for _, tc := range []struct {
		name      string
		payload   string
		wantRetry time.Duration
		wantReqID string
		wantTrans bool
	}{
		{
			name:      "typeless rate_limit_error without numeric status",
			payload:   `{"error":{"type":"rate_limit_error","message":"Too many requests"},"headers":{"retry-after":"11","x-request-id":"req-inferred-type"}}`,
			wantRetry: 11 * time.Second,
			wantReqID: "req-inferred-type",
		},
		{
			name:      "typeless rate_limit_exceeded code without numeric status",
			payload:   `{"error":{"code":"rate_limit_exceeded","message":"Rate limit exceeded"},"headers":{"retry-after":"13","x-request-id":"req-inferred-code"}}`,
			wantRetry: 13 * time.Second,
			wantReqID: "req-inferred-code",
		},
		{
			name:      "typeless nested body turn_rate_limit_exceeded without numeric status",
			payload:   `{"body":{"error":{"type":"rate_limit_error","code":"turn_rate_limit_exceeded","message":"Codex is at capacity"},"headers":{"retry-after":"17","x-request-id":"req-inferred-turn"}}}`,
			wantRetry: 17 * time.Second,
			wantReqID: "req-inferred-turn",
			wantTrans: true,
		},
		{
			name:      "top-level rate_limit_exceeded without numeric status",
			payload:   `{"code":"rate_limit_exceeded","message":"Rate limit exceeded","headers":{"retry-after":"19","x-request-id":"req-inferred-top"}}`,
			wantRetry: 19 * time.Second,
			wantReqID: "req-inferred-top",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inferredErr, okInferred := parseCodexWebsocketError([]byte(tc.payload))
			if !okInferred || statusCodeFromTestError(t, inferredErr) != http.StatusTooManyRequests {
				t.Fatalf("inferred websocket 429 = %v (ok=%v)", inferredErr, okInferred)
			}
			if clienterror.IsRequestFault(http.StatusTooManyRequests, inferredErr) {
				t.Fatalf("inferred 429 became a request fault: %v", inferredErr)
			}
			retry := inferredErr.(interface{ RetryAfter() *time.Duration }).RetryAfter()
			if retry == nil || *retry != tc.wantRetry {
				t.Fatalf("inferred retry-after = %v, want %v", retry, tc.wantRetry)
			}
			if gotReqID := inferredErr.(interface{ Headers() http.Header }).Headers().Get("X-Request-Id"); gotReqID != tc.wantReqID {
				t.Fatalf("request id = %q, want %q", gotReqID, tc.wantReqID)
			}
			if transient, okTrans := inferredErr.(interface{ IsTransientRateLimit() bool }); !okTrans || transient.IsTransientRateLimit() != tc.wantTrans {
				t.Fatalf("transient = %v (ok=%v), want %v", transient, okTrans, tc.wantTrans)
			}

			server := codexWebsocketServer(t, tc.payload)
			defer server.Close()
			req, opts := codexWebsocketRequest()
			_, streamErr := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
			if statusCodeFromTestError(t, streamErr) != http.StatusTooManyRequests {
				t.Fatalf("stream status = %v, want 429", streamErr)
			}
			streamRetry := streamErr.(interface{ RetryAfter() *time.Duration }).RetryAfter()
			if streamRetry == nil || *streamRetry != tc.wantRetry {
				t.Fatalf("stream retry-after = %v, want %v", streamRetry, tc.wantRetry)
			}
		})
	}

	responseFailedPayload := `{"type":"response.failed","headers":{"retry-after":"23"},"response":{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"Rate limit exceeded"}}}`
	failedErr, _, okFailed := codexTerminalFailureErr([]byte(responseFailedPayload))
	if !okFailed || statusCodeFromTestError(t, failedErr) != http.StatusTooManyRequests {
		t.Fatalf("response.failed 429 = %v (ok=%v)", failedErr, okFailed)
	}
	if failedRetry := failedErr.RetryAfter(); failedRetry == nil || *failedRetry != 23*time.Second {
		t.Fatalf("response.failed retry-after = %v, want 23s", failedRetry)
	}
	failedServer := codexWebsocketServer(t, codexCreatedEvent, responseFailedPayload)
	defer failedServer.Close()
	req, opts := codexWebsocketRequest()
	_, failedStreamErr := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(failedServer.URL), req, opts)
	if statusCodeFromTestError(t, failedStreamErr) != http.StatusTooManyRequests {
		t.Fatalf("response.failed bootstrap status = %v, want 429", failedStreamErr)
	}
	if failedBootstrapRetry := failedStreamErr.(interface{ RetryAfter() *time.Duration }).RetryAfter(); failedBootstrapRetry == nil || *failedBootstrapRetry != 23*time.Second {
		t.Fatalf("response.failed bootstrap retry-after = %v, want 23s", failedBootstrapRetry)
	}
}
