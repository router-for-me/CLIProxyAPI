package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Issue 7600: the captured overload events omit status and nest retry headers in error.
const codexOverloadRetryEvent = `{"type":"error","error":{"message":"Our servers are currently overloaded. Please try again later.","type":"service_unavailable_error","param":null,"code":"server_is_overloaded","headers":{"retry-after":"326","retry-after-ms":"326000"}},"sequence_number":3}`

func TestCodexOverloadRetryAfter(t *testing.T) {
	tests := []struct {
		name       string
		payload    string
		wantStatus int
		wantRetry  time.Duration
	}{
		{
			name:       "captured overload without status",
			payload:    codexOverloadRetryEvent,
			wantStatus: http.StatusBadGateway,
			wantRetry:  326 * time.Second,
		},
		{
			name:       "second captured delay",
			payload:    `{"type":"error","error":{"message":"Our servers are currently overloaded. Please try again later.","type":"service_unavailable_error","param":null,"code":"server_is_overloaded","headers":{"retry-after":"303","retry-after-ms":"303000"}},"sequence_number":3}`,
			wantStatus: http.StatusBadGateway,
			wantRetry:  303 * time.Second,
		},
		{
			name:       "explicit websocket status with nested headers",
			payload:    `{"type":"error","status":503,"error":{"code":"server_is_overloaded","headers":{"retry-after":"326"}}}`,
			wantStatus: http.StatusServiceUnavailable,
			wantRetry:  326 * time.Second,
		},
		{
			name:       "wrapped websocket body with nested headers",
			payload:    `{"type":"error","status_code":503,"body":{"error":{"code":"server_is_overloaded","headers":{"retry-after":303}}},"headers":{"x-request-id":"req-overload"}}`,
			wantStatus: http.StatusServiceUnavailable,
			wantRetry:  303 * time.Second,
		},
		{
			name:       "response failed with nested headers",
			payload:    `{"type":"response.failed","response":{"error":{"code":"server_is_overloaded","headers":{"retry-after":"326"}}}}`,
			wantStatus: http.StatusBadGateway,
			wantRetry:  326 * time.Second,
		},
		{
			name:       "top level header takes precedence",
			payload:    `{"type":"error","status":503,"headers":{"Retry-After":"7"},"error":{"code":"server_is_overloaded","headers":{"retry-after":"326"}}}`,
			wantStatus: http.StatusServiceUnavailable,
			wantRetry:  7 * time.Second,
		},
		{
			name:       "missing hint keeps transient fallback",
			payload:    codexOverloadEvent,
			wantStatus: http.StatusBadGateway,
			wantRetry:  -1,
		},
		{
			name:       "invalid hint keeps transient fallback",
			payload:    `{"type":"error","status":503,"error":{"code":"server_is_overloaded","headers":{"retry-after":"later"}}}`,
			wantStatus: http.StatusServiceUnavailable,
			wantRetry:  -1,
		},
		{
			name:       "quota reset wins over nested header",
			payload:    `{"type":"error","status":429,"error":{"type":"usage_limit_reached","resets_in_seconds":3600,"headers":{"retry-after":"326"}}}`,
			wantStatus: http.StatusTooManyRequests,
			wantRetry:  time.Hour,
		},
		{
			name:       "connection limit retains immediate reconnect",
			payload:    `{"type":"error","status":429,"error":{"code":"websocket_connection_limit_reached","headers":{"retry-after":"326"}}}`,
			wantStatus: http.StatusTooManyRequests,
			wantRetry:  0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(tc.payload)
			err, ok := parseCodexWebsocketErrorWithCooling(payload, false)
			if !ok {
				err, _, ok = codexTerminalFailureErrWithCooling(payload, false)
			}
			if !ok {
				t.Fatal("expected a terminal error")
			}
			if got := statusCodeFromTestError(t, err); got != tc.wantStatus {
				t.Fatalf("StatusCode = %d, want %d", got, tc.wantStatus)
			}
			var hinted interface{ RetryAfter() *time.Duration }
			if !errors.As(err, &hinted) {
				t.Fatalf("error does not expose RetryAfter: %v", err)
			}
			got := hinted.RetryAfter()
			if tc.wantRetry < 0 {
				if got != nil {
					t.Fatalf("RetryAfter = %v, want nil", *got)
				}
			} else if got == nil || *got != tc.wantRetry {
				t.Fatalf("RetryAfter = %v, want %v", got, tc.wantRetry)
			}
		})
	}
}

func TestCodexOverloadRetryAfterCooldownThroughWebsocket(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		t.Run(fmt.Sprintf("buffered=%t", buffered), func(t *testing.T) {
			server := codexWebsocketServer(t, codexCreatedEvent, codexInProgressEvent, codexOverloadRetryEvent)
			defer server.Close()
			cfg := codexBufferingConfig(buffered)
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.SetRetryConfig(0, 0, 0)
			executor := NewCodexWebsocketsExecutor(cfg)
			manager.RegisterExecutor(executor)
			auth := codexTestAuth(server.URL)
			auth.ID = fmt.Sprintf("overload-retry-%t", buffered)
			auth.Provider = "codex"
			auth.Status = cliproxyauth.StatusActive
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatal(errRegister)
			}
			req, opts := codexWebsocketRequest()
			registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: req.Model}})
			defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)

			before := time.Now()
			result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
			if err == nil && result != nil {
				_, err = drainChunks(result)
			}
			after := time.Now()
			if err == nil {
				t.Fatal("expected the upstream overload to fail the request")
			}
			current, ok := manager.GetByID(auth.ID)
			if !ok {
				t.Fatal("credential disappeared")
			}
			state := current.ModelStates[req.Model]
			if state == nil || !state.Unavailable {
				t.Fatal("overloaded model must be cooling down")
			}
			wantDelay := 326 * time.Second
			if state.NextRetryAfter.Before(before.Add(wantDelay)) || state.NextRetryAfter.After(after.Add(wantDelay)) {
				t.Fatalf("cooldown = %v after request start, want %v", state.NextRetryAfter.Sub(before), wantDelay)
			}
			if state.Quota.Exceeded {
				t.Fatal("overload must remain a transient failure")
			}
		})
	}
}

func TestCodexOverloadRetryAfterHTTPHeaders(t *testing.T) {
	for _, tc := range []struct {
		name      string
		headers   http.Header
		wantRetry time.Duration
	}{
		{"transport hint takes precedence", http.Header{"Retry-After": {"7"}}, 7 * time.Second},
		{"nested hint with unrelated transport headers", http.Header{"X-Request-Id": {"req-overload"}}, 326 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := newCodexStatusErrWithHeaders(http.StatusServiceUnavailable, tc.headers, []byte(codexOverloadRetryEvent))
			if got := err.RetryAfter(); got == nil || *got != tc.wantRetry {
				t.Fatalf("RetryAfter = %v, want %v", got, tc.wantRetry)
			}
		})
	}
}
