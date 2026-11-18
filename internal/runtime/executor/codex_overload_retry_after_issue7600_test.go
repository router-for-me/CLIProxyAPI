package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const (
	issue7600CapturedOverload326 = `{"type":"error","error":{"message":"Our servers are currently overloaded. Please try again later.","type":"service_unavailable_error","param":null,"code":"server_is_overloaded","headers":{"retry-after":"326","retry-after-ms":"326000"}},"sequence_number":3}`
	issue7600CapturedOverload303 = `{"type":"error","error":{"message":"Our servers are currently overloaded. Please try again later.","type":"service_unavailable_error","param":null,"code":"server_is_overloaded","headers":{"retry-after":"303","retry-after-ms":"303000"}},"sequence_number":3}`
)

func assertIssue7600RetryAfter(t *testing.T, err error, want time.Duration) {
	t.Helper()
	var retry interface{ RetryAfter() *time.Duration }
	if !errors.As(err, &retry) || retry == nil {
		t.Fatalf("error %T (%v) does not expose RetryAfter()", err, err)
	}
	got := retry.RetryAfter()
	if got == nil {
		t.Fatalf("RetryAfter() = nil, want %v (err=%v)", want, err)
	}
	if *got != want {
		t.Fatalf("RetryAfter() = %v, want %v", *got, want)
	}
}

func TestIssue7600CodexTerminalOverloadHonorsNestedRetryAfter(t *testing.T) {
	tests := []struct {
		name       string
		payload    string
		wantStatus int
		wantDelay  time.Duration
	}{
		{
			name:       "captured ws overload 326s",
			payload:    issue7600CapturedOverload326,
			wantStatus: http.StatusBadGateway,
			wantDelay:  326 * time.Second,
		},
		{
			name:       "captured ws overload 303s",
			payload:    issue7600CapturedOverload303,
			wantStatus: http.StatusBadGateway,
			wantDelay:  303 * time.Second,
		},
		{
			name:       "response failed overload with nested headers",
			payload:    `{"type":"response.failed","response":{"error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded.","headers":{"retry-after":"326"}}},"sequence_number":3}`,
			wantStatus: http.StatusBadGateway,
			wantDelay:  326 * time.Second,
		},
		{
			name:       "error event with top-level headers and omitted status",
			payload:    `{"type":"error","headers":{"retry-after":"180"},"error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded."},"sequence_number":3}`,
			wantStatus: http.StatusBadGateway,
			wantDelay:  180 * time.Second,
		},
		{
			name:       "mixed-case Retry-After inside nested error.headers",
			payload:    `{"type":"error","error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded.","headers":{"Retry-After":"45"}},"sequence_number":3}`,
			wantStatus: http.StatusBadGateway,
			wantDelay:  45 * time.Second,
		},
		{
			name:       "conflicting top-level headers outrank nested error.headers",
			payload:    `{"type":"error","headers":{"retry-after":"326"},"error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded.","headers":{"retry-after":"1"}},"sequence_number":3}`,
			wantStatus: http.StatusBadGateway,
			wantDelay:  326 * time.Second,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errTerminal, _, ok := codexTerminalFailureErrWithCooling([]byte(tc.payload), false)
			if !ok {
				t.Fatal("codexTerminalFailureErrWithCooling returned ok=false, want true")
			}
			if got := errTerminal.StatusCode(); got != tc.wantStatus {
				t.Fatalf("StatusCode() = %d, want %d", got, tc.wantStatus)
			}
			assertIssue7600RetryAfter(t, errTerminal, tc.wantDelay)
		})
	}

	t.Run("invalid or missing retry-after yields nil", func(t *testing.T) {
		for _, payload := range []string{
			`{"type":"error","error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"overloaded","headers":{"retry-after":"soon"}}}`,
			`{"type":"error","error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"overloaded"}}`,
		} {
			errTerminal, _, ok := codexTerminalFailureErrWithCooling([]byte(payload), false)
			if !ok {
				t.Fatal("codexTerminalFailureErrWithCooling returned ok=false")
			}
			if got := errTerminal.RetryAfter(); got != nil {
				t.Fatalf("RetryAfter() = %v, want nil", *got)
			}
		}
	})
}

func TestIssue7600CodexWebsocketOverloadExplicit503HonorsRetryAfter(t *testing.T) {
	tests := []struct {
		name           string
		payload        string
		wantHeader     string
		wantRetryAfter time.Duration
	}{
		{
			name:           "explicit 503 with nested error.headers",
			payload:        `{"type":"error","status":503,"error":{"message":"Our servers are currently overloaded. Please try again later.","type":"service_unavailable_error","param":null,"code":"server_is_overloaded","headers":{"retry-after":"326","retry-after-ms":"326000"}},"sequence_number":3}`,
			wantHeader:     "326",
			wantRetryAfter: 326 * time.Second,
		},
		{
			name:           "explicit 503 with top-level headers",
			payload:        `{"type":"error","status":503,"headers":{"retry-after":"303"},"error":{"message":"Our servers are currently overloaded.","type":"service_unavailable_error","code":"server_is_overloaded"},"sequence_number":3}`,
			wantHeader:     "303",
			wantRetryAfter: 303 * time.Second,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errWS, ok := parseCodexWebsocketErrorWithCooling([]byte(tc.payload), false)
			if !ok {
				t.Fatal("parseCodexWebsocketErrorWithCooling returned ok=false, want true")
			}
			var status interface{ StatusCode() int }
			if !errors.As(errWS, &status) || status.StatusCode() != http.StatusServiceUnavailable {
				t.Fatalf("StatusCode() = %v, want 503", errWS)
			}
			var withHeaders interface{ Headers() http.Header }
			if !errors.As(errWS, &withHeaders) || withHeaders.Headers().Get("Retry-After") != tc.wantHeader {
				t.Fatalf("Headers().Get(Retry-After) = %v, want %q", withHeaders, tc.wantHeader)
			}
			assertIssue7600RetryAfter(t, errWS, tc.wantRetryAfter)
		})
	}
}

func TestIssue7600CodexBootstrapOverloadPreservesRetryAfter(t *testing.T) {
	_, terminalBody, ok := codexTerminalFailureErrWithCooling([]byte(issue7600CapturedOverload326), false)
	if !ok {
		t.Fatal("codexTerminalFailureErrWithCooling returned ok=false, want true")
	}
	bootstrapErr := newCodexBootstrapOverloadErr(terminalBody)
	if got := bootstrapErr.StatusCode(); got != http.StatusServiceUnavailable {
		t.Fatalf("StatusCode() = %d, want 503", got)
	}
	assertIssue7600RetryAfter(t, bootstrapErr, 326*time.Second)

	t.Run("top-level headers outrank nested error.headers across bootstrap failover", func(t *testing.T) {
		conflicting := `{"type":"error","headers":{"retry-after":"326"},"error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded.","headers":{"retry-after":"1"}}}`
		streamErr, body, okTerm := codexTerminalFailureErrWithCooling([]byte(conflicting), false)
		if !okTerm {
			t.Fatal("codexTerminalFailureErrWithCooling returned ok=false")
		}
		assertIssue7600RetryAfter(t, streamErr, 326*time.Second)

		overloadErr := newCodexBootstrapOverloadErrWithRetryAfter(body, streamErr.RetryAfter())
		if got := overloadErr.StatusCode(); got != http.StatusServiceUnavailable {
			t.Fatalf("StatusCode() = %d, want 503", got)
		}
		assertIssue7600RetryAfter(t, overloadErr, 326*time.Second)
	})

	t.Run("invalid top-level Retry-After still shadows nested error.headers across bootstrap", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			payload string
		}{
			{
				name:    "non-numeric",
				payload: `{"type":"error","headers":{"retry-after":"soon"},"error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded.","headers":{"retry-after":"1"}}}`,
			},
			{
				name:    "empty-string",
				payload: `{"type":"error","headers":{"retry-after":""},"error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded.","headers":{"retry-after":"1"}}}`,
			},
			{
				name:    "whitespace",
				payload: `{"type":"error","headers":{"retry-after":"   "},"error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded.","headers":{"retry-after":"1"}}}`,
			},
			{
				name:    "null-value",
				payload: `{"type":"error","headers":{"retry-after":null},"error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded.","headers":{"retry-after":"1"}}}`,
			},
			{
				name:    "unsupported-object",
				payload: `{"type":"error","headers":{"retry-after":{"seconds":1}},"error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded.","headers":{"retry-after":"1"}}}`,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				streamErr, body, okTerm := codexTerminalFailureErrWithCooling([]byte(tc.payload), false)
				if !okTerm {
					t.Fatal("codexTerminalFailureErrWithCooling returned ok=false")
				}
				if got := streamErr.RetryAfter(); got != nil {
					t.Fatalf("streamErr.RetryAfter() = %v, want nil", *got)
				}

				overloadErr := newCodexBootstrapOverloadErrWithRetryAfter(body, streamErr.RetryAfter())
				if got := overloadErr.StatusCode(); got != http.StatusServiceUnavailable {
					t.Fatalf("StatusCode() = %d, want 503", got)
				}
				if got := overloadErr.RetryAfter(); got != nil {
					t.Fatalf("overloadErr.RetryAfter() = %v, want nil", *got)
				}

				wsPayload := `{"type":"error","status":503,"headers":` + tc.payload[strings.Index(tc.payload, `"headers":`)+len(`"headers":`):]
				wsErr, okWS := parseCodexWebsocketErrorWithCooling([]byte(wsPayload), false)
				if !okWS {
					t.Fatal("parseCodexWebsocketErrorWithCooling returned ok=false")
				}
				var retry interface{ RetryAfter() *time.Duration }
				if errors.As(wsErr, &retry) && retry.RetryAfter() != nil {
					t.Fatalf("wsErr.RetryAfter() = %v, want nil", *retry.RetryAfter())
				}
			})
		}
	})
}

func TestIssue7600HintPrecedencePreservedWithNestedHeaders(t *testing.T) {
	t.Run("usage_limit_reached wins over nested error.headers", func(t *testing.T) {
		payload := `{"type":"error","status":429,"error":{"type":"usage_limit_reached","message":"limit","resets_in_seconds":3600,"headers":{"retry-after":"15"}}}`
		errWS, ok := parseCodexWebsocketErrorWithCooling([]byte(payload), false)
		if !ok {
			t.Fatal("parseCodexWebsocketErrorWithCooling returned ok=false")
		}
		assertIssue7600RetryAfter(t, errWS, time.Hour)
	})

	t.Run("websocket_connection_limit_reached wins over nested error.headers", func(t *testing.T) {
		payload := `{"type":"error","status":429,"error":{"code":"websocket_connection_limit_reached","message":"too many","headers":{"retry-after":"30"}}}`
		errWS, ok := parseCodexWebsocketErrorWithCooling([]byte(payload), false)
		if !ok {
			t.Fatal("parseCodexWebsocketErrorWithCooling returned ok=false")
		}
		assertIssue7600RetryAfter(t, errWS, 0)
	})
}

func TestIssue7600CodexWebsocketsExecuteStreamPreservesOverloadRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		name      string
		buffering bool
		payload   string
		wantCode  int
	}{
		{
			name:      "unbuffered",
			buffering: false,
			payload:   issue7600CapturedOverload326,
			wantCode:  http.StatusServiceUnavailable,
		},
		{
			name:      "bootstrap_buffering",
			buffering: true,
			payload:   issue7600CapturedOverload326,
			wantCode:  http.StatusServiceUnavailable,
		},
		{
			name:      "bootstrap_buffering_conflicting_headers",
			buffering: true,
			payload:   `{"type":"error","headers":{"retry-after":"326"},"error":{"message":"Our servers are currently overloaded. Please try again later.","type":"service_unavailable_error","code":"server_is_overloaded","headers":{"retry-after":"1"}},"sequence_number":3}`,
			wantCode:  http.StatusServiceUnavailable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, errUpgrade := upgrader.Upgrade(w, r, nil)
				if errUpgrade != nil {
					t.Errorf("upgrade websocket: %v", errUpgrade)
					return
				}
				defer func() { _ = conn.Close() }()
				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					return
				}
				for _, frame := range []string{
					`{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`,
					`{"type":"response.in_progress","response":{"id":"resp_1","status":"in_progress"}}`,
					tc.payload,
				} {
					if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(frame)); errWrite != nil {
						return
					}
				}
			}))
			defer server.Close()

			exec := NewCodexWebsocketsExecutor(&config.Config{
				Codex: config.CodexConfig{
					StreamBootstrapBuffering: tc.buffering,
				},
			})
			auth := &cliproxyauth.Auth{
				ID:       "codex-issue7600",
				Provider: "codex",
				Attributes: map[string]string{
					"base_url":   server.URL,
					"websockets": "true",
					"api_key":    "sk-test",
				},
			}
			req := cliproxyexecutor.Request{
				Model:   "gpt-6.1-sol",
				Payload: []byte(`{"model":"gpt-6.1-sol","input":[{"type":"message","id":"msg-1"}]}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FromString("openai-response"),
				ResponseFormat: sdktranslator.FromString("openai-response"),
				Stream:         true,
			}

			result, errStream := exec.ExecuteStream(context.Background(), auth, req, opts)
			if tc.buffering {
				if errStream == nil {
					t.Fatal("expected ExecuteStream to fail the attempt during bootstrap buffering")
				}
				if got := statusCodeFromTestError(t, errStream); got != tc.wantCode {
					t.Fatalf("StatusCode() = %d, want %d", got, tc.wantCode)
				}
				assertIssue7600RetryAfter(t, errStream, 326*time.Second)
				return
			}

			if errStream != nil {
				t.Fatalf("ExecuteStream() unexpected error: %v", errStream)
			}
			var chunkErr error
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					chunkErr = chunk.Err
				}
			}
			if chunkErr == nil {
				t.Fatal("expected stream chunk error for overload event")
			}
			if got := statusCodeFromTestError(t, chunkErr); got != tc.wantCode {
				t.Fatalf("StatusCode() = %d, want %d", got, tc.wantCode)
			}
			assertIssue7600RetryAfter(t, chunkErr, 326*time.Second)
		})
	}
}

func TestIssue7600CodexSSEBootstrapConflictingHeadersPreservesTopLevelRetryAfter(t *testing.T) {
	conflicting := `{"type":"error","headers":{"retry-after":"326"},"error":{"message":"Our servers are currently overloaded. Please try again later.","type":"service_unavailable_error","code":"server_is_overloaded","headers":{"retry-after":"1"}},"sequence_number":3}`
	server := codexSSEServer(codexCreatedEvent, codexInProgressEvent, conflicting)
	defer server.Close()

	req, opts := codexTestRequest()
	result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
	if err == nil {
		t.Fatal("expected ExecuteStream to fail the attempt during SSE bootstrap buffering")
	}
	if result != nil {
		t.Fatal("expected nil result so buffered frames do not leak")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusServiceUnavailable {
		t.Fatalf("StatusCode() = %d, want 503", got)
	}
	assertIssue7600RetryAfter(t, err, 326*time.Second)
}

func TestIssue7600ManagerAppliesNestedOverloadCooldownInsteadOf60sFallback(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()
		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			return
		}
		for _, frame := range []string{
			`{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`,
			`{"type":"response.in_progress","response":{"id":"resp_1","status":"in_progress"}}`,
			issue7600CapturedOverload326,
		} {
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(frame)); errWrite != nil {
				return
			}
		}
	}))
	defer server.Close()

	cfg := &config.Config{
		Codex: config.CodexConfig{
			StreamBootstrapBuffering: false,
		},
	}
	manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
	manager.RegisterExecutor(NewCodexWebsocketsExecutor(cfg))

	model := "gpt-6.1-sol-issue7600-mgr"
	auth := &cliproxyauth.Auth{
		ID:       "codex-issue7600-mgr",
		Provider: "codex",
		Status:   cliproxyauth.StatusActive,
		Attributes: map[string]string{
			"base_url":   server.URL,
			"websockets": "true",
			"api_key":    "sk-test",
		},
	}
	if _, errReg := manager.Register(cliproxyauth.WithSkipPersist(context.Background()), auth); errReg != nil {
		t.Fatalf("Register() error = %v", errReg)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	before := time.Now()
	result, errStream := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"model":"gpt-6.1-sol-issue7600-mgr","input":[{"type":"message","id":"msg-1"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
		Stream:         true,
	})
	if errStream == nil {
		for chunk := range result.Chunks {
			_ = chunk
		}
	}

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to remain registered")
	}
	state := updated.ModelStates[model]
	if state == nil || !state.Unavailable {
		t.Fatalf("expected model state to enter cooldown, got %+v", state)
	}
	if state.NextRetryAfter.Before(before.Add(300*time.Second)) || state.NextRetryAfter.After(before.Add(350*time.Second)) {
		t.Fatalf("NextRetryAfter = %v (remaining %v), want ~326s from %v (must not fall back to 60s)", state.NextRetryAfter, state.NextRetryAfter.Sub(before), before)
	}
}
