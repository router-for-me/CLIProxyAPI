package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type issue7571ZeroOutputStatusErr struct {
	msg string
}

func (e issue7571ZeroOutputStatusErr) Error() string     { return e.msg }
func (e issue7571ZeroOutputStatusErr) StatusCode() int   { return http.StatusBadGateway }
func (e issue7571ZeroOutputStatusErr) ErrorCode() string { return ErrorCodeZeroOutput }

type issue7571MockExecutor struct {
	provider      string
	mu            sync.Mutex
	executeAuths  []string
	streamAuths   []string
	succeedAuthID string
}

func (e *issue7571MockExecutor) Identifier() string { return e.provider }

func (e *issue7571MockExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.executeAuths = append(e.executeAuths, auth.ID)
	succeed := e.succeedAuthID != "" && auth.ID == e.succeedAuthID
	e.mu.Unlock()
	if succeed {
		return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
	}
	return cliproxyexecutor.Response{}, issue7571ZeroOutputStatusErr{msg: "upstream returned zero output with finishReason MALFORMED_FUNCTION_CALL"}
}

func (e *issue7571MockExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.streamAuths = append(e.streamAuths, auth.ID)
	succeed := e.succeedAuthID != "" && auth.ID == e.succeedAuthID
	e.mu.Unlock()
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	if succeed {
		ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {\"ok\":true}\n\n")}
		close(ch)
		return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
	}
	ch <- cliproxyexecutor.StreamChunk{Err: issue7571ZeroOutputStatusErr{msg: "upstream returned zero output with finishReason MALFORMED_FUNCTION_CALL"}}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *issue7571MockExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *issue7571MockExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *issue7571MockExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func TestIssue7571_ZeroOutputSkipsCooldownAndStopsRequestRetryAmplification(t *testing.T) {
	const model = "gemini-4-hydrogen-auth-7571"

	t.Run("exhausted_round_stops_without_extra_request_retry_sweeps", func(t *testing.T) {
		mockExec := &issue7571MockExecutor{provider: "antigravity"}
		manager := NewManager(nil, &RoundRobinSelector{}, nil)
		manager.SetConfig(&internalconfig.Config{DisableCooling: false})
		manager.SetRetryConfig(3, 30*time.Second, 0)
		manager.RegisterExecutor(mockExec)

		for _, authID := range []string{"ag-7571-a", "ag-7571-b"} {
			if _, errReg := manager.Register(context.Background(), &Auth{
				ID:       authID,
				Provider: "antigravity",
				Status:   StatusActive,
			}); errReg != nil {
				t.Fatalf("Register(%s) error = %v", authID, errReg)
			}
			id := authID
			registry.GetGlobalRegistry().RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		}

		_, errExec := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
		if statusCodeFromError(errExec) != http.StatusBadGateway {
			t.Fatalf("Execute() status = %d (err=%v), want 502", statusCodeFromError(errExec), errExec)
		}
		if len(mockExec.executeAuths) != 2 {
			t.Fatalf("Execute() attempts with request-retry=3 = %d (%v), want 2 (1 initial round across 2 credentials)", len(mockExec.executeAuths), mockExec.executeAuths)
		}

		streamRes, errStream := manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
		streamErr := errStream
		if streamErr == nil && streamRes != nil {
			for chunk := range streamRes.Chunks {
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
			}
		}
		if statusCodeFromError(streamErr) != http.StatusBadGateway {
			t.Fatalf("ExecuteStream() status = %d (err=%v), want 502", statusCodeFromError(streamErr), streamErr)
		}
		if len(mockExec.streamAuths) != 2 {
			t.Fatalf("ExecuteStream() attempts with request-retry=3 = %d (%v), want 2 (1 initial round across 2 credentials)", len(mockExec.streamAuths), mockExec.streamAuths)
		}

		for _, authID := range []string{"ag-7571-a", "ag-7571-b"} {
			stored, okAuth := manager.GetByID(authID)
			if !okAuth || stored == nil {
				t.Fatalf("GetByID(%s) missing", authID)
			}
			if stored.Unavailable || !stored.NextRetryAfter.IsZero() {
				t.Fatalf("credential %s cooled down: Unavailable=%v NextRetryAfter=%v", authID, stored.Unavailable, stored.NextRetryAfter)
			}
		}
	})

	t.Run("initial_round_still_fails_over_to_healthy_sibling_or_fallback_provider", func(t *testing.T) {
		agExec := &issue7571MockExecutor{provider: "antigravity"}
		vtxExec := &issue7571MockExecutor{provider: "vertex", succeedAuthID: "vtx-7571-ok"}
		manager := NewManager(nil, &RoundRobinSelector{}, nil)
		manager.SetConfig(&internalconfig.Config{DisableCooling: false})
		manager.SetRetryConfig(3, 30*time.Second, 0)
		manager.RegisterExecutor(agExec)
		manager.RegisterExecutor(vtxExec)

		for _, entry := range []struct {
			id       string
			provider string
		}{
			{id: "ag-7571-zero", provider: "antigravity"},
			{id: "vtx-7571-ok", provider: "vertex"},
		} {
			if _, errReg := manager.Register(context.Background(), &Auth{
				ID:       entry.id,
				Provider: entry.provider,
				Status:   StatusActive,
			}); errReg != nil {
				t.Fatalf("Register(%s) error = %v", entry.id, errReg)
			}
			id, prov := entry.id, entry.provider
			registry.GetGlobalRegistry().RegisterClient(id, prov, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		}

		resp, errExec := manager.Execute(context.Background(), []string{"antigravity", "vertex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
		if errExec != nil {
			t.Fatalf("Execute() fallback error = %v, want nil", errExec)
		}
		if string(resp.Payload) != `{"ok":true}` {
			t.Fatalf("Execute() payload = %q, want ok JSON", string(resp.Payload))
		}
		storedAG, _ := manager.GetByID("ag-7571-zero")
		if storedAG == nil || storedAG.Unavailable || !storedAG.NextRetryAfter.IsZero() {
			t.Fatalf("ag-7571-zero should remain uncooled after zero-output fallback: %+v", storedAG)
		}
	})
}

func TestIssue7571_ZeroOutputWrappedClassificationAndSessionAffinityNever(t *testing.T) {
	zeroErr := issue7571ZeroOutputStatusErr{msg: "upstream returned zero output with finishReason MALFORMED_FUNCTION_CALL"}
	wrappedBootstrap := newStreamBootstrapError(markUpstreamExecutionAttempt(zeroErr), http.Header{"Content-Type": {"text/event-stream"}})
	wrappedHome := markHomeRetryRoundExhausted(markUpstreamExecutionAttempt(zeroErr), nil, true)

	for name, candidate := range map[string]error{
		"raw":       zeroErr,
		"bootstrap": wrappedBootstrap,
		"home":      wrappedHome,
	} {
		if !isZeroOutputError(candidate) {
			t.Fatalf("isZeroOutputError(%s) = false, want true", name)
		}
		if isRequestRetryRoundError(candidate) {
			t.Fatalf("isRequestRetryRoundError(%s) = true, want false", name)
		}
		rerr := ResultErrorFromError(candidate)
		if rerr == nil || rerr.Code != ErrorCodeZeroOutput || !shouldSkipCredentialCooldown(rerr) {
			t.Fatalf("ResultErrorFromError(%s) = %+v, want code=%q skipCooldown=true", name, rerr, ErrorCodeZeroOutput)
		}
	}

	const model = "gemini-4-hydrogen-affinity-7571"
	affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
		RebindOn: SessionAffinityRebindOnNever,
	})
	mockExec := &issue7571MockExecutor{provider: "antigravity", succeedAuthID: "ag-aff-1"}
	manager := NewManager(nil, affinity, nil)
	manager.SetConfig(&internalconfig.Config{DisableCooling: false})
	manager.SetRetryConfig(3, 30*time.Second, 0)
	manager.RegisterExecutor(mockExec)

	for _, authID := range []string{"ag-aff-1", "ag-aff-2"} {
		if _, errReg := manager.Register(context.Background(), &Auth{
			ID:       authID,
			Provider: "antigravity",
			Status:   StatusActive,
		}); errReg != nil {
			t.Fatalf("Register(%s) error = %v", authID, errReg)
		}
		id := authID
		registry.GetGlobalRegistry().RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}

	headers := http.Header{"X-Session-ID": {"sess-7571-never"}}
	if _, errBind := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Headers: headers}); errBind != nil {
		t.Fatalf("initial bind Execute() error = %v", errBind)
	}

	// Switch bound credential to return zero-output 502: under rebind-on=never and request-retry=3,
	// it must execute once (no 4x retry loop) and preserve the 502 error (not rewrite to 503 session_auth_retry).
	mockExec.mu.Lock()
	mockExec.succeedAuthID = ""
	mockExec.executeAuths = nil
	mockExec.mu.Unlock()

	_, errNever := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Headers: headers})
	if statusCodeFromError(errNever) != http.StatusBadGateway {
		t.Fatalf("rebind-on=never Execute() status = %d (err=%v), want 502 (must not rewrite zero-output to 503 session_auth_retry)", statusCodeFromError(errNever), errNever)
	}
	if len(mockExec.executeAuths) != 1 || mockExec.executeAuths[0] != "ag-aff-1" {
		t.Fatalf("rebind-on=never Execute() attempts = %v, want [ag-aff-1] once", mockExec.executeAuths)
	}

	streamResNever, errStreamNever := manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Headers: headers, Stream: true})
	if errStreamNever == nil && streamResNever != nil {
		for chunk := range streamResNever.Chunks {
			if chunk.Err != nil {
				errStreamNever = chunk.Err
			}
		}
	}
	if statusCodeFromError(errStreamNever) != http.StatusBadGateway {
		t.Fatalf("rebind-on=never ExecuteStream() status = %d (err=%v), want 502", statusCodeFromError(errStreamNever), errStreamNever)
	}
	if len(mockExec.streamAuths) != 1 || mockExec.streamAuths[0] != "ag-aff-1" {
		t.Fatalf("rebind-on=never ExecuteStream() attempts = %v, want [ag-aff-1] once", mockExec.streamAuths)
	}
}
