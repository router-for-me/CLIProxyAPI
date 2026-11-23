package auth

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"syscall"
	"testing"

	"github.com/google/uuid"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func windowsCodexTLSHandshakeError() error {
	return &url.Error{
		Op:  "Post",
		URL: "https://chatgpt.com/backend-api/codex/responses",
		Err: fmt.Errorf("tls: TLS handshake: %w", &net.OpError{
			Op:     "read",
			Net:    "tcp",
			Source: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1},
			Addr:   &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 2},
			Err:    errors.New("wsarecv: A connection attempt failed because the connected party did not properly respond after a period of time, or established connection failed because connected host has failed to respond."),
		}),
	}
}

func dialRefusedError() error {
	return &url.Error{
		Op:  "Post",
		URL: "https://chatgpt.com/backend-api/codex/responses",
		Err: &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
	}
}

func TestManager_ShouldRetryAfterError_RetriesPreHTTPTransportFailure(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(1, 0, 0)
	model := "gpt-transport-retry-" + uuid.NewString()
	authID := "transport-retry-" + uuid.NewString()
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	if _, errRegister := m.Register(context.Background(), &Auth{ID: authID, Provider: "codex"}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "windows tls handshake", err: windowsCodexTLSHandshakeError(), want: true},
		{name: "dial refused", err: dialRefusedError(), want: true},
		{name: "unexpected eof", err: io.ErrUnexpectedEOF, want: true},
		{name: "http2 force closed", err: errors.New("http2: client connection force closed via ClientConn.Close"), want: true},
		{name: "http2 conn closed", err: &url.Error{Op: "Post", URL: "https://example.com", Err: errors.New("http2: client conn is closed")}, want: true},
		{name: "unauthorized", err: &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"}, want: false},
		{name: "canceled", err: context.Canceled, want: false},
		{name: "certificate", err: &url.Error{Op: "Post", URL: "https://chatgpt.com/backend-api/codex/responses", Err: x509.UnknownAuthorityError{}}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wait, shouldRetry := m.shouldRetryAfterError(tc.err, 0, []string{"codex"}, model, 0)
			if shouldRetry != tc.want {
				t.Fatalf("shouldRetryAfterError() = (%v, %t), want retry %t", wait, shouldRetry, tc.want)
			}
			if tc.want && wait != 0 {
				t.Fatalf("shouldRetryAfterError() wait = %v, want 0", wait)
			}
			if tc.want {
				if _, shouldRetry = m.shouldRetryAfterError(tc.err, 1, []string{"codex"}, model, 0); shouldRetry {
					t.Fatal("transport retried after the configured additional round")
				}
			}
		})
	}
}

// TestConfiguredTLSHandshakeTimeoutIsRetryable pins the Issue #6615 retry
// contract: an internal establishment deadline is a transient pre-HTTP
// failure even though its chain contains context.DeadlineExceeded, while
// caller-owned deadline and cancellation shapes remain non-retryable.
func TestConfiguredTLSHandshakeTimeoutIsRetryable(t *testing.T) {
	configuredClaudeErr := &url.Error{
		Op:  "Post",
		URL: "https://api.anthropic.com/v1/messages",
		Err: fmt.Errorf("claude tls: handshake upstream: %w", cliproxyexecutor.NewConfiguredTLSHandshakeTimeoutError(context.DeadlineExceeded)),
	}
	configuredChromeErr := &url.Error{
		Op:  "Post",
		URL: "https://chatgpt.com/backend-api/codex/responses",
		Err: fmt.Errorf("utls: TLS handshake: %w", cliproxyexecutor.NewConfiguredTLSHandshakeTimeoutError(context.DeadlineExceeded)),
	}
	tests := []struct {
		name      string
		err       error
		wantRetry bool
	}{
		{
			name:      "claude configured handshake timeout",
			err:       configuredClaudeErr,
			wantRetry: true,
		},
		{
			name:      "chrome configured handshake timeout",
			err:       configuredChromeErr,
			wantRetry: true,
		},
		{
			name: "caller deadline stays non retryable",
			err: &url.Error{
				Op:  "Post",
				URL: "https://api.anthropic.com/v1/messages",
				Err: context.DeadlineExceeded,
			},
			wantRetry: false,
		},
		{
			name: "percent encoded marker is not trusted",
			err: &url.Error{
				Op:  "Post",
				URL: "https://api.anthropic.com/v1/messages",
				Err: fmt.Errorf("tls%%20handshake%%20timeout: %w", context.DeadlineExceeded),
			},
			wantRetry: false,
		},
		{
			name: "plaintext timeout phrase is not trusted",
			err: &url.Error{
				Op:  "Post",
				URL: "https://api.anthropic.com/v1/messages",
				Err: fmt.Errorf("external transport tls handshake timeout: %w", context.DeadlineExceeded),
			},
			wantRetry: false,
		},
		{
			name: "caller cancellation stays non retryable",
			err: &url.Error{
				Op:  "Post",
				URL: "https://api.anthropic.com/v1/messages",
				Err: context.Canceled,
			},
			wantRetry: false,
		},
		{
			name: "caller cancellation joined with configured timeout stays non retryable",
			err: &url.Error{
				Op:  "Post",
				URL: "https://api.anthropic.com/v1/messages",
				Err: errors.Join(cliproxyexecutor.NewConfiguredTLSHandshakeTimeoutError(context.DeadlineExceeded), context.Canceled),
			},
			wantRetry: false,
		},
		{
			name: "caller deadline joined with configured timeout stays non retryable",
			err: &url.Error{
				Op:  "Post",
				URL: "https://api.anthropic.com/v1/messages",
				Err: errors.Join(cliproxyexecutor.NewConfiguredTLSHandshakeTimeoutError(context.DeadlineExceeded), context.DeadlineExceeded),
			},
			wantRetry: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientTransportError(tt.err); got != tt.wantRetry {
				t.Fatalf("isTransientTransportError(%v) = %t, want %t", tt.err, got, tt.wantRetry)
			}
			if got := isRequestRetryRoundError(tt.err); got != tt.wantRetry {
				t.Fatalf("isRequestRetryRoundError(%v) = %t, want %t", tt.err, got, tt.wantRetry)
			}
			resultErr := resultErrorFromError(tt.err)
			if resultErr == nil {
				t.Fatalf("resultErrorFromError(%v) = nil", tt.err)
			}
			if tt.wantRetry {
				if resultErr.Code != transientTransportErrorCode {
					t.Fatalf("result code = %q, want %q", resultErr.Code, transientTransportErrorCode)
				}
				if !isTransientTransportResultError(resultErr) {
					t.Fatalf("isTransientTransportResultError(%#v) = false, want true", resultErr)
				}
				if isConnectionLifecycleResultError(resultErr) {
					t.Fatalf("configured handshake timeout became a connection-lifecycle result: %#v", resultErr)
				}
			} else if resultErr.Code == transientTransportErrorCode {
				t.Fatalf("caller-owned context error became transient transport: %#v", resultErr)
			}
			if !tt.wantRetry && isTransientTransportResultError(resultErr) {
				t.Fatalf("message-only fallback promoted a non-retryable error: %#v", resultErr)
			}
		})
	}
}

func TestManager_MarkResult_PreHTTPTransportFailureDoesNotCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	model := "gpt-6-astra"
	cases := []struct {
		name string
		err  *Error
	}{
		{name: "typed tls handshake", err: resultErrorFromError(windowsCodexTLSHandshakeError())},
		{name: "connection reset message", err: &Error{Message: "connection reset"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			auth := &Auth{ID: "auth-transport-" + uuid.NewString(), Provider: "codex"}
			if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}
			m.MarkResult(context.Background(), Result{
				AuthID:   auth.ID,
				Provider: auth.Provider,
				Model:    model,
				Success:  false,
				Error:    tc.err,
			})
			assertNoCooldown(t, m, auth.ID, model)
		})
	}
}

func TestExecuteRetriesPreHTTPTransportFailureWithoutCooling(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(1, 0, 0)
	executor := &transportThenSuccessExecutor{
		identifier: "codex",
		fail:       windowsCodexTLSHandshakeError(),
	}
	manager.RegisterExecutor(executor)

	model := "gpt-6-astra-" + uuid.NewString()
	authID := "codex-transport-" + uuid.NewString()
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: "codex"}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	resp, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v, want success after transport retry", errExecute)
	}
	if string(resp.Payload) != "ok" {
		t.Fatalf("Execute() payload = %q, want %q", resp.Payload, "ok")
	}
	if calls := executor.callCount(); calls != 2 {
		t.Fatalf("executor calls = %d, want 2", calls)
	}
	assertNoCooldown(t, manager, authID, model)
}

func TestManager_HTTP2ClosedConnectionRespectsRequestRetry(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	for _, message := range []string{
		"http2: client connection force closed via ClientConn.Close",
		"http2: client conn is closed",
	} {
		for _, kind := range []string{"execute", "stream", "count"} {
			for _, retries := range []int{0, 1} {
				t.Run(fmt.Sprintf("%s/%s/retries=%d", message, kind, retries), func(t *testing.T) {
					m := NewManager(nil, nil, nil)
					m.SetRetryConfig(retries, 0, 0)
					executor := &transportThenSuccessExecutor{identifier: "openai-compatibility", fail: errors.New(message)}
					m.RegisterExecutor(executor)
					model := "http2-retry-" + uuid.NewString()
					authID := "http2-auth-" + uuid.NewString()
					registry.GetGlobalRegistry().RegisterClient(authID, executor.Identifier(), []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
					if _, errRegister := m.Register(context.Background(), &Auth{ID: authID, Provider: executor.Identifier()}); errRegister != nil {
						t.Fatalf("register auth: %v", errRegister)
					}
					call := func() error {
						providers := []string{executor.Identifier()}
						req := cliproxyexecutor.Request{Model: model}
						switch kind {
						case "execute":
							_, errExecute := m.Execute(context.Background(), providers, req, cliproxyexecutor.Options{})
							return errExecute
						case "stream":
							stream, errStream := m.ExecuteStream(context.Background(), providers, req, cliproxyexecutor.Options{})
							if errStream != nil {
								return errStream
							}
							for chunk := range stream.Chunks {
								if chunk.Err != nil {
									return chunk.Err
								}
							}
							return nil
						default:
							_, errCount := m.ExecuteCount(context.Background(), providers, req, cliproxyexecutor.Options{})
							return errCount
						}
					}
					errCall := call()
					assertNoCooldown(t, m, authID, model)
					if retries == 0 {
						if !errors.Is(errCall, executor.fail) || executor.callCount() != 1 {
							t.Fatalf("retry disabled: error = %v, calls = %d; want original error and one call", errCall, executor.callCount())
						}
						if errNext := call(); errNext != nil {
							t.Fatalf("next request on the same credential: %v", errNext)
						}
					} else if errCall != nil {
						t.Fatalf("request failed instead of using its retry round: %v", errCall)
					}
					if executor.callCount() != 2 {
						t.Fatalf("executor calls = %d, want 2", executor.callCount())
					}
					assertNoCooldown(t, m, authID, model)
				})
			}
		}
	}
}

func TestExecuteDoesNotPoisonCredentialOnPreHTTPTransportFailure(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	executor := &transportThenSuccessExecutor{
		identifier: "codex",
		fail:       windowsCodexTLSHandshakeError(),
	}
	manager.RegisterExecutor(executor)

	model := "gpt-6-astra-" + uuid.NewString()
	authID := "codex-transport-poison-" + uuid.NewString()
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: "codex"}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	req := cliproxyexecutor.Request{Model: model}
	if _, errExecute := manager.Execute(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{}); errExecute == nil {
		t.Fatal("first Execute() error = nil, want transport failure")
	} else if shouldRetrySchedulerPick(errExecute) {
		t.Fatalf("first Execute() = %v, want raw transport error rather than auth_unavailable", errExecute)
	}
	assertNoCooldown(t, manager, authID, model)

	resp, errExecute := manager.Execute(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("second Execute() error = %v, want success on the still-available credential", errExecute)
	}
	if string(resp.Payload) != "ok" {
		t.Fatalf("second Execute() payload = %q, want %q", resp.Payload, "ok")
	}
	if calls := executor.callCount(); calls != 2 {
		t.Fatalf("executor calls = %d, want 2", calls)
	}
}

// alwaysFailExecutor is local to the configured-handshake retry-round test
// because transportThenSuccessExecutor intentionally fails only once.
type alwaysFailExecutor struct {
	identifier string
	fail       error
	calls      int
}

func (e *alwaysFailExecutor) Identifier() string { return e.identifier }

func (e *alwaysFailExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls++
	return cliproxyexecutor.Response{}, e.fail
}

func (e *alwaysFailExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.calls++
	return nil, e.fail
}

func (*alwaysFailExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *alwaysFailExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls++
	return cliproxyexecutor.Response{}, e.fail
}

func (*alwaysFailExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *alwaysFailExecutor) callCount() int { return e.calls }

// TestExecuteRetriesConfiguredClaudeHandshakeTimeoutToConfiguredRounds
// verifies the complete request-retry round behavior with one credential:
// request-retry 3 means the initial attempt plus three additional rounds,
// and a caller deadline with the same retry setting still runs only once.
func TestExecuteRetriesConfiguredClaudeHandshakeTimeoutToConfiguredRounds(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	configuredErr := &url.Error{
		Op:  "Post",
		URL: "https://api.anthropic.com/v1/messages",
		Err: fmt.Errorf("claude tls: handshake upstream: %w", cliproxyexecutor.NewConfiguredTLSHandshakeTimeoutError(context.DeadlineExceeded)),
	}
	tests := []struct {
		name      string
		err       error
		wantCalls int
	}{
		{name: "configured handshake timeout", err: configuredErr, wantCalls: 4},
		{name: "caller deadline", err: context.DeadlineExceeded, wantCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.SetRetryConfig(3, 0, 0)
			executor := &alwaysFailExecutor{identifier: "claude", fail: tt.err}
			manager.RegisterExecutor(executor)

			model := "claude-handshake-retry-" + uuid.NewString()
			authID := "claude-handshake-auth-" + uuid.NewString()
			registry.GetGlobalRegistry().RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
			if _, errRegister := manager.Register(context.Background(), &Auth{ID: authID, Provider: "claude"}); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}

			_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			if errExecute == nil {
				t.Fatal("Execute() error = nil, want final handshake failure")
			}
			if calls := executor.callCount(); calls != tt.wantCalls {
				t.Fatalf("executor calls = %d, want %d", calls, tt.wantCalls)
			}
			assertNoCooldown(t, manager, authID, model)
		})
	}
}

func TestHomeExecuteRetriesPreHTTPTransportFailure(t *testing.T) {
	dispatcher := &retryContractHomeDispatcher{authIDs: []string{"home-retry-a"}}
	executor := &transportThenSuccessExecutor{
		identifier: "home-retry-contract",
		fail:       windowsCodexTLSHandshakeError(),
	}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetRetryConfig(1, 0, 0)
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	manager.RegisterExecutor(executor)

	resp, errExecute := manager.Execute(context.Background(), []string{"home-retry-contract"}, cliproxyexecutor.Request{Model: "gpt"}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v, want success after Home transport retry", errExecute)
	}
	if string(resp.Payload) != "ok" {
		t.Fatalf("Execute() payload = %q, want %q", resp.Payload, "ok")
	}
	if calls := executor.callCount(); calls != 2 {
		t.Fatalf("executor calls = %d, want 2", calls)
	}
}

type transportThenSuccessExecutor struct {
	identifier string
	fail       error

	mu    sync.Mutex
	calls int
}

func (e *transportThenSuccessExecutor) Identifier() string { return e.identifier }

func (e *transportThenSuccessExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.recordCall() == 1 {
		return cliproxyexecutor.Response{}, e.fail
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *transportThenSuccessExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.recordCall() == 1 {
		return nil, e.fail
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (*transportThenSuccessExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *transportThenSuccessExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.recordCall() == 1 {
		return cliproxyexecutor.Response{}, e.fail
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (*transportThenSuccessExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *transportThenSuccessExecutor) recordCall() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	return e.calls
}

func (e *transportThenSuccessExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}
