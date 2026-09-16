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
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
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
	chunks := make(chan cliproxyexecutor.StreamChunk)
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
