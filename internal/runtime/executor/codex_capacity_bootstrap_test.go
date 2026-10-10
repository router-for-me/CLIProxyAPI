package executor

import (
	"context"
	"net/http"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const codexCapacityEvent = `{"type":"error","error":{"type":"server_error","message":"Selected model is at capacity. Please try a different model."}}`

func TestCodexCapacityBootstrapClassification(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"server type", `{"error":{"type":"server_error","message":"Selected model is at capacity. Please try a different model."}}`, true},
		{"server code", `{"error":{"code":"server_error","message":"Selected model is at capacity. Please try a different model."}}`, true},
		{"request fault", `{"error":{"type":"invalid_request_error","message":"Selected model is at capacity. Please try a different model."}}`, false},
		{"quota", `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`, false},
		{"unrelated server error", `{"error":{"type":"server_error","message":"Internal failure"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCodexOverloadBootstrapFailure([]byte(tc.body)); got != tc.want {
				t.Fatalf("bootstrap eligibility = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestCodexCapacityBootstrapCommitBoundary(t *testing.T) {
	for _, transport := range []string{"sse", "websocket"} {
		for _, tc := range []struct {
			name      string
			enabled   bool
			prefix    []string
			bootstrap bool
		}{
			{"handshake only", true, []string{codexCreatedEvent, codexInProgressEvent}, true},
			{"private metadata", true, []string{`{"type":"codex.rate_limits"}`, `{"type":"codex.response.metadata"}`, codexCreatedEvent}, true},
			{"text output", true, []string{codexCreatedEvent, `{"type":"response.output_text.delta","delta":"hello"}`}, false},
			{"function call", true, []string{codexCreatedEvent, `{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"run","arguments":"{}"}}`}, false},
			{"server tool", true, []string{codexCreatedEvent, `{"type":"response.web_search_call.in_progress","item_id":"ws_1","output_index":0}`}, false},
			{"unknown event", true, []string{codexCreatedEvent, `{"type":"response.future_event"}`}, false},
			{"disabled", false, []string{codexCreatedEvent}, false},
			{"buffer limit", true, repeatCapacityHandshake(), false},
		} {
			t.Run(transport+"/"+tc.name, func(t *testing.T) {
				frames := append(append([]string(nil), tc.prefix...), codexCapacityEvent)
				var result *cliproxyexecutor.StreamResult
				var err error
				if transport == "sse" {
					server := codexSSEServer(frames...)
					defer server.Close()
					req, opts := codexTestRequest()
					result, err = NewCodexExecutor(codexBufferingConfig(tc.enabled)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
				} else {
					server := codexWebsocketServer(t, frames...)
					defer server.Close()
					req, opts := codexWebsocketRequest()
					result, err = NewCodexWebsocketsExecutor(codexBufferingConfig(tc.enabled)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
				}
				if tc.bootstrap {
					if result != nil || err == nil {
						t.Fatalf("capacity rejection must fail before exposing a stream: result=%v, err=%v", result, err)
					}
				} else {
					if result == nil || err != nil {
						t.Fatalf("committed stream must not be replaced: result=%v, err=%v", result, err)
					}
					var payload string
					payload, err = drainChunks(result)
					if !strings.Contains(payload, "response.created") {
						t.Fatalf("missing committed handshake: %s", payload)
					}
				}
				if got := statusCodeFromTestError(t, err); got != http.StatusTooManyRequests {
					t.Fatalf("capacity status = %d, want 429", got)
				}
				if scoped, ok := err.(interface{ IsCredentialScoped() bool }); ok && scoped.IsCredentialScoped() {
					t.Fatal("capacity must not cool all models on the credential")
				}
			})
		}
	}
}

func repeatCapacityHandshake() []string {
	events := []string{codexCreatedEvent}
	for i := 0; i <= codexBootstrapMaxBufferedEvents; i++ {
		events = append(events, codexInProgressEvent)
	}
	return events
}

func TestCodexCapacityBootstrapKeepsDownstreamWebsocketOpen(t *testing.T) {
	notified, err := executeWebsocketStreamInSession(t, codexCreatedEvent, codexInProgressEvent, codexCapacityEvent)
	if err == nil || statusCodeFromTestError(t, err) != http.StatusTooManyRequests {
		t.Fatalf("expected capacity rejection, got %v", err)
	}
	if notified {
		t.Fatal("a retryable bootstrap rejection must not disconnect the downstream websocket")
	}
}
