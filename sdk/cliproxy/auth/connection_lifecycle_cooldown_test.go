package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestManager_MarkResult_ConnectionLifecycleDoesNotCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	cases := []struct {
		name string
		err  *Error
	}{
		{name: "websocket 1000", err: &Error{Message: "websocket: close 1000 (normal)"}},
		{name: "websocket 1001", err: &Error{Message: "websocket: close 1001 (going away)"}},
		{name: "websocket 1006", err: &Error{Message: "websocket: close 1006 (abnormal closure): unexpected EOF"}},
		{name: "context canceled", err: &Error{Message: "context canceled"}},
		{name: "context deadline exceeded", err: &Error{Message: "context deadline exceeded"}},
		{name: "unexpected EOF", err: &Error{Message: "unexpected EOF"}},
		{name: "plain EOF", err: &Error{Message: "EOF"}},
		{name: "wrapped unexpected EOF", err: &Error{Message: "read tcp 127.0.0.1:1->127.0.0.1:2: unexpected EOF"}},
		{name: "typed canceled", err: resultErrorFromError(context.Canceled)},
		{name: "typed deadline", err: resultErrorFromError(context.DeadlineExceeded)},
		{name: "url canceled", err: resultErrorFromError(&url.Error{Op: "Post", URL: "https://example.com", Err: context.Canceled})},
		{name: "url deadline", err: resultErrorFromError(&url.Error{Op: "Post", URL: "https://example.com", Err: context.DeadlineExceeded})},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			auth := &Auth{ID: "auth-lifecycle-" + tc.name, Provider: "codex"}
			if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}

			model := "gpt-5.6-sol"
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

func TestManager_MarkResult_ConnectionLifecycleAuthLevelDoesNotCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-lifecycle-auth-level", Provider: "codex"}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		// Empty model exercises the auth-level failure path.
		Success: false,
		Error:   &Error{Message: "websocket: close 1006 (abnormal closure): unexpected EOF"},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if updated.Unavailable {
		t.Fatalf("expected auth-level lifecycle error to keep auth available")
	}
	if !updated.NextRetryAfter.IsZero() {
		t.Fatalf("expected auth-level lifecycle error to keep auth cooldown unset, got %v", updated.NextRetryAfter)
	}
}

func TestManager_MarkResult_HTTPStatusWithLifecycleTextStillCooldowns(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	cases := []struct {
		name       string
		httpStatus int
		message    string
		wantAuth   bool // true => long auth-style suspension reason expected via model state
	}{
		{name: "401 unexpected EOF", httpStatus: http.StatusUnauthorized, message: "unexpected EOF", wantAuth: true},
		{name: "429 context canceled", httpStatus: http.StatusTooManyRequests, message: "context canceled", wantAuth: true},
		{name: "500 unexpected EOF", httpStatus: http.StatusInternalServerError, message: "unexpected EOF"},
		{name: "500 websocket 1006 text", httpStatus: http.StatusInternalServerError, message: "websocket: close 1006 (abnormal closure): unexpected EOF"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			auth := &Auth{ID: "auth-status-" + tc.name, Provider: "codex"}
			if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}

			model := "gpt-5.6-sol"
			before := time.Now()
			m.MarkResult(context.Background(), Result{
				AuthID:   auth.ID,
				Provider: auth.Provider,
				Model:    model,
				Success:  false,
				Error: &Error{
					HTTPStatus: tc.httpStatus,
					Message:    tc.message,
				},
			})

			updated, ok := m.GetByID(auth.ID)
			if !ok || updated == nil {
				t.Fatalf("expected auth to be present")
			}
			state := updated.ModelStates[model]
			if state == nil {
				t.Fatal("expected model cooldown state")
			}
			if state.NextRetryAfter.IsZero() {
				t.Fatalf("expected HTTP status %d with lifecycle text to still cool, got zero NextRetryAfter", tc.httpStatus)
			}
			if tc.httpStatus == http.StatusInternalServerError && state.NextRetryAfter.Before(before.Add(4*time.Second)) {
				t.Fatalf("expected ~5s transient cooldown, got next_retry_after=%v", state.NextRetryAfter)
			}
			if tc.wantAuth && !state.Unavailable {
				t.Fatalf("expected auth-class status to mark model unavailable")
			}
		})
	}
}

func TestManager_MarkResult_NonLifecycleStillCooldowns(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-still-cools", Provider: "codex"}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "gpt-5.6-sol"
	before := time.Now()
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error: &Error{
			HTTPStatus: http.StatusInternalServerError,
			Message:    "upstream internal failure",
			Retryable:  true,
		},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatal("expected model cooldown state")
	}
	if !state.Unavailable {
		t.Fatal("expected non-lifecycle 500 to mark model unavailable")
	}
	if state.NextRetryAfter.Before(before.Add(4 * time.Second)) {
		t.Fatalf("expected ~5s transient cooldown, got next_retry_after=%v", state.NextRetryAfter)
	}
}

func TestResultErrorFromError_ConnectionLifecycleDoesNotBecomeRequestScoped(t *testing.T) {
	cases := []error{
		context.Canceled,
		context.DeadlineExceeded,
		io.EOF,
		io.ErrUnexpectedEOF,
		&url.Error{Op: "Post", URL: "https://example.com", Err: context.Canceled},
		&url.Error{Op: "Post", URL: "https://example.com", Err: context.DeadlineExceeded},
		&websocket.CloseError{Code: websocket.CloseNormalClosure, Text: "normal"},
		&websocket.CloseError{Code: websocket.CloseGoingAway, Text: "bye"},
		&websocket.CloseError{Code: websocket.CloseAbnormalClosure, Text: "unexpected EOF"},
		fmt.Errorf("upstream read: %w", &websocket.CloseError{Code: websocket.CloseAbnormalClosure, Text: "unexpected EOF"}),
		fmt.Errorf("wrap: %w", io.ErrUnexpectedEOF),
		errors.New("websocket: close 1000 (normal)"),
		errors.New("websocket: close 1006 (abnormal closure): unexpected EOF"),
		errors.New("context deadline exceeded"),
		errors.New("unexpected EOF"),
	}
	for _, err := range cases {
		if !isConnectionLifecycleError(err) {
			t.Fatalf("isConnectionLifecycleError(%v) = false, want true", err)
		}
		got := resultErrorFromError(err)
		if got == nil {
			t.Fatalf("resultErrorFromError(%v) = nil", err)
		}
		if got.IsRequestScoped() {
			t.Fatalf("resultErrorFromError(%v) code=%q, want non-request-scoped lifecycle error", err, got.Code)
		}
		if got.Code != connectionLifecycleErrorCode {
			t.Fatalf("resultErrorFromError(%v) code=%q, want %q", err, got.Code, connectionLifecycleErrorCode)
		}
		if isRequestInvalidError(err) {
			t.Fatalf("isRequestInvalidError(%v) = true, lifecycle must not stop credential fallback", err)
		}
		if !shouldSkipCredentialCooldown(got) {
			t.Fatalf("shouldSkipCredentialCooldown(%#v) = false, want true", got)
		}
	}
}

func TestIsConnectionLifecycleError_StatusBearingErrorsStayCoolable(t *testing.T) {
	cases := []error{
		&statusBearingError{status: http.StatusUnauthorized, msg: "unexpected EOF"},
		&statusBearingError{status: http.StatusTooManyRequests, msg: "context canceled"},
		&statusBearingError{status: http.StatusInternalServerError, msg: "unexpected EOF"},
		&statusBearingError{status: http.StatusBadGateway, msg: "websocket: close 1006 (abnormal closure): unexpected EOF"},
	}
	for _, err := range cases {
		if isConnectionLifecycleError(err) {
			t.Fatalf("isConnectionLifecycleError(%v) = true, want false for status-bearing errors", err)
		}
		got := resultErrorFromError(err)
		if shouldSkipCredentialCooldown(got) {
			t.Fatalf("shouldSkipCredentialCooldown(%#v) = true, want false", got)
		}
	}
}

func TestIsConnectionLifecycleError_TypedCloseWins(t *testing.T) {
	// Typed websocket close is unambiguous even when an outer status is attached.
	err := &statusBearingCloseError{
		status: http.StatusBadGateway,
		close:  &websocket.CloseError{Code: websocket.CloseAbnormalClosure, Text: "unexpected EOF"},
	}
	if !isConnectionLifecycleError(err) {
		t.Fatalf("typed CloseError should be lifecycle even with outer status")
	}
	got := resultErrorFromError(err)
	if got.Code != connectionLifecycleErrorCode {
		t.Fatalf("code = %q, want %q", got.Code, connectionLifecycleErrorCode)
	}
	if !shouldSkipCredentialCooldown(got) {
		t.Fatalf("shouldSkipCredentialCooldown(%#v) = false, want true", got)
	}

	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-typed-close", Provider: "codex"}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	model := "gpt-5.6-sol"
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error:    got,
	})
	assertNoCooldown(t, m, auth.ID, model)
}

type statusBearingError struct {
	status int
	msg    string
}

func (e *statusBearingError) Error() string   { return e.msg }
func (e *statusBearingError) StatusCode() int { return e.status }

type statusBearingCloseError struct {
	status int
	close  *websocket.CloseError
}

func (e *statusBearingCloseError) Error() string {
	if e.close == nil {
		return "status-bearing close"
	}
	return e.close.Error()
}
func (e *statusBearingCloseError) StatusCode() int { return e.status }
func (e *statusBearingCloseError) Unwrap() error   { return e.close }

func assertNoCooldown(t *testing.T, m *Manager, authID, model string) {
	t.Helper()
	updated, ok := m.GetByID(authID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if updated.Unavailable {
		t.Fatalf("expected connection lifecycle error to keep auth available")
	}
	if !updated.NextRetryAfter.IsZero() {
		t.Fatalf("expected connection lifecycle error to keep auth cooldown unset, got %v", updated.NextRetryAfter)
	}
	if state := updated.ModelStates[model]; state != nil {
		if state.Unavailable || !state.NextRetryAfter.IsZero() {
			t.Fatalf("expected no model cooldown, got %#v", state)
		}
	}
}

func TestManager_MarkResult_TransportFailureDoesNotCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	cases := []struct {
		name string
		err  error
	}{
		{name: "tls handshake timeout", err: fmt.Errorf(`Post "https://example.com/v1/chat/completions": net/http: TLS handshake timeout`)},
		{name: "connection refused", err: fmt.Errorf("dial tcp 1.2.3.4:443: connect: connection refused")},
		{name: "connection reset", err: fmt.Errorf("read tcp 127.0.0.1:1->127.0.0.1:2: read: connection reset by peer")},
		{name: "dns failure", err: fmt.Errorf(`dial tcp: lookup example.com: no such host`)},
		{name: "io timeout", err: fmt.Errorf("dial tcp 1.2.3.4:443: i/o timeout")},
		{name: "proxyconnect", err: fmt.Errorf("proxyconnect tcp: dial tcp 127.0.0.1:7890: connect: connection refused")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !isConnectionLifecycleError(tc.err) {
				t.Fatalf("isConnectionLifecycleError(%v) = false, want true", tc.err)
			}
			got := resultErrorFromError(tc.err)
			if !shouldSkipCredentialCooldown(got) {
				t.Fatalf("shouldSkipCredentialCooldown(%#v) = false, want true", got)
			}

			m := NewManager(nil, nil, nil)
			auth := &Auth{ID: "auth-transport-" + tc.name, Provider: "codex"}
			if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}
			model := "gpt-5.6-sol"
			m.MarkResult(context.Background(), Result{
				AuthID:   auth.ID,
				Provider: auth.Provider,
				Model:    model,
				Success:  false,
				Error:    got,
			})
			assertNoCooldown(t, m, auth.ID, model)
		})
	}
}

func TestIsConnectionLifecycleError_TransportTextWithStatusStillCooldowns(t *testing.T) {
	// An upstream HTTP error body mentioning transport text must stay coolable.
	err := &statusBearingError{status: http.StatusBadGateway, msg: "upstream reported: connection refused"}
	if isConnectionLifecycleError(err) {
		t.Fatalf("status-bearing transport text must not be classified as lifecycle")
	}
	if shouldSkipCredentialCooldown(resultErrorFromError(err)) {
		t.Fatalf("shouldSkipCredentialCooldown = true, want false")
	}
}

func TestManager_MarkResult_TransportFailureWithAuthProxyStillCooldowns(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-transport-proxy", Provider: "codex", ProxyURL: "http://127.0.0.1:7890"}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	transportErr := resultErrorFromError(fmt.Errorf("proxyconnect tcp: dial tcp 127.0.0.1:7890: connect: connection refused"))

	model := "gpt-5.6-sol"
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error:    transportErr,
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil || state.NextRetryAfter.IsZero() {
		t.Fatalf("expected per-auth proxy transport failure to keep cooldown")
	}

	// Client cancellation must still skip cooldown even with a per-auth proxy.
	authCancel := &Auth{ID: "auth-transport-proxy-cancel", Provider: "codex", ProxyURL: "http://127.0.0.1:7890"}
	if _, errRegister := m.Register(context.Background(), authCancel); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	m.MarkResult(context.Background(), Result{
		AuthID:   authCancel.ID,
		Provider: authCancel.Provider,
		Model:    model,
		Success:  false,
		Error:    resultErrorFromError(context.Canceled),
	})
	assertNoCooldown(t, m, authCancel.ID, model)

	// Auth-level path (empty model) must also keep cooldown for transport
	// failures through a per-auth proxy.
	authLevel := &Auth{ID: "auth-transport-proxy-auth-level", Provider: "codex", ProxyURL: "http://127.0.0.1:7890"}
	if _, errRegister := m.Register(context.Background(), authLevel); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	m.MarkResult(context.Background(), Result{
		AuthID:   authLevel.ID,
		Provider: authLevel.Provider,
		Success:  false,
		Error:    transportErr,
	})
	updatedAuthLevel, okAuthLevel := m.GetByID(authLevel.ID)
	if !okAuthLevel || updatedAuthLevel == nil {
		t.Fatalf("expected auth-level auth to be present")
	}
	if updatedAuthLevel.NextRetryAfter.IsZero() {
		t.Fatalf("expected auth-level transport failure via per-auth proxy to keep cooldown")
	}
}

func TestManager_MarkResult_RequestScopedTransportTextWithAuthProxySkipsCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-request-scoped-proxy", Provider: "claude", ProxyURL: "http://127.0.0.1:7890"}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	model := "claude-sonnet-4-5"

	// request_scoped code must win over the per-auth proxy transport override.
	scopedErr := &Error{Code: "request_scoped", Message: "Post \"https://example.com/v1/messages\": net/http: TLS handshake timeout"}
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error:    scopedErr,
	})
	assertNoCooldown(t, m, auth.ID, model)
}

func newVertexSATestAuth(id, location string) *Auth {
	metadata := map[string]any{
		"service_account": map[string]any{
			"project_id":   "test-project",
			"client_email": "sa@test-project.iam.gserviceaccount.com",
		},
		"project_id": "test-project",
	}
	if location != "" {
		metadata["location"] = location
	}
	return &Auth{ID: id, Provider: "vertex", Metadata: metadata}
}

// registerClientModelForTest registers a route model for an auth in the global
// registry, mirroring production where client models are registered at the
// service layer. Only auths that register the model count as pollable peers
// for request selection (see Manager.authSupportsRouteModel). The registration
// is removed on test cleanup so the global registry is not polluted.
func registerClientModelForTest(t *testing.T, authID, model string) {
	registry.GetGlobalRegistry().RegisterClient(authID, "vertex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
}

func TestManager_MarkResult_TransportFailureWithUniqueVertexLocationStillCooldowns(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	m := NewManager(nil, nil, nil)
	authA := newVertexSATestAuth("auth-vertex-us", "us-central1")
	authB := newVertexSATestAuth("auth-vertex-eu", "europe-west4")
	for _, a := range []*Auth{authA, authB} {
		if _, errRegister := m.Register(context.Background(), a); errRegister != nil {
			t.Fatalf("register auth: %v", errRegister)
		}
	}
	// The US peer must register the route model to count as a pollable
	// alternative endpoint for the model-level attribution below.
	transportErr := resultErrorFromError(fmt.Errorf("dial tcp: lookup europe-west4-aiplatform.googleapis.com: no such host"))

	model := "gemini-2.5-pro"
	registerClientModelForTest(t, authA.ID, model)
	registerClientModelForTest(t, authB.ID, model)
	m.MarkResult(context.Background(), Result{
		AuthID:   authB.ID,
		Provider: authB.Provider,
		Model:    model,
		Success:  false,
		Error:    transportErr,
	})

	updated, ok := m.GetByID(authB.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil || state.NextRetryAfter.IsZero() {
		t.Fatalf("expected unique-location vertex transport failure to keep cooldown")
	}

	// Auth-level path (empty model) must also keep cooldown. It needs a
	// location not used by other pool members to count as auth-specific.
	authLevel := newVertexSATestAuth("auth-vertex-eu-level", "asia-south1")
	if _, errRegister := m.Register(context.Background(), authLevel); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	m.MarkResult(context.Background(), Result{
		AuthID:   authLevel.ID,
		Provider: authLevel.Provider,
		Success:  false,
		Error:    transportErr,
	})
	updatedAuthLevel, okAuthLevel := m.GetByID(authLevel.ID)
	if !okAuthLevel || updatedAuthLevel == nil {
		t.Fatalf("expected auth-level auth to be present")
	}
	if updatedAuthLevel.NextRetryAfter.IsZero() {
		t.Fatalf("expected auth-level unique-location vertex transport failure to keep cooldown")
	}

	// Non-transport lifecycle failures (client cancellation) still skip cooldown
	// even with a unique vertex location.
	authCancel := newVertexSATestAuth("auth-vertex-ap-cancel", "asia-east1")
	if _, errRegister := m.Register(context.Background(), authCancel); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	m.MarkResult(context.Background(), Result{
		AuthID:   authCancel.ID,
		Provider: authCancel.Provider,
		Model:    model,
		Success:  false,
		Error:    resultErrorFromError(context.Canceled),
	})
	assertNoCooldown(t, m, authCancel.ID, model)
}

func TestManager_MarkResult_TransportFailureWithProxyDialSkipsVertexAttribution(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	m := NewManager(nil, nil, nil)
	authUS := newVertexSATestAuth("auth-vertex-proxy-us", "us-central1")
	authEU := newVertexSATestAuth("auth-vertex-proxy-eu", "europe-west4")
	for _, a := range []*Auth{authUS, authEU} {
		if _, errRegister := m.Register(context.Background(), a); errRegister != nil {
			t.Fatalf("register auth: %v", errRegister)
		}
	}
	model := "gemini-2.5-pro"

	// A proxy dial failure (shared proxy layer, before any regional endpoint is
	// reached) must not be attributed to a Vertex location, even though the
	// failing auth's location is unique in the pool.
	proxyErr := resultErrorFromError(fmt.Errorf("proxyconnect tcp: dial tcp 127.0.0.1:7890: connect: connection refused"))
	if !isTransportFailureResultError(proxyErr) {
		t.Fatalf("isTransportFailureResultError(%#v) = false, want true", proxyErr)
	}
	m.MarkResult(context.Background(), Result{
		AuthID:   authEU.ID,
		Provider: authEU.Provider,
		Model:    model,
		Success:  false,
		Error:    proxyErr,
	})
	assertNoCooldown(t, m, authEU.ID, model)

	// Auth-level path (empty model) must behave identically.
	m.MarkResult(context.Background(), Result{
		AuthID:   authUS.ID,
		Provider: authUS.Provider,
		Success:  false,
		Error:    proxyErr,
	})
	updatedUS, okUS := m.GetByID(authUS.ID)
	if !okUS || updatedUS == nil {
		t.Fatalf("expected auth to be present")
	}
	if updatedUS.Unavailable || !updatedUS.NextRetryAfter.IsZero() {
		t.Fatalf("expected proxy dial failure to skip auth-level cooldown, got unavailable=%v next=%v", updatedUS.Unavailable, updatedUS.NextRetryAfter)
	}

	// Regression guard: a direct regional-endpoint failure (no proxy involved)
	// must still keep cooldown when the location is unique in the pool.
	endpointErr := resultErrorFromError(fmt.Errorf("dial tcp: lookup asia-south1-aiplatform.googleapis.com: no such host"))
	authLevel := newVertexSATestAuth("auth-vertex-proxy-level", "asia-south1")
	if _, errRegister := m.Register(context.Background(), authLevel); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	m.MarkResult(context.Background(), Result{
		AuthID:   authLevel.ID,
		Provider: authLevel.Provider,
		Success:  false,
		Error:    endpointErr,
	})
	updatedLevel, okLevel := m.GetByID(authLevel.ID)
	if !okLevel || updatedLevel == nil {
		t.Fatalf("expected auth to be present")
	}
	if updatedLevel.NextRetryAfter.IsZero() {
		t.Fatalf("expected unique-location vertex endpoint failure to keep cooldown")
	}
}

func TestManager_MarkResult_TransportFailureNewPatternsSkipCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	model := "gpt-5.6-sol"
	cases := []struct {
		name string
		err  error
	}{
		{name: "server misbehaving", err: fmt.Errorf(`dial tcp: lookup example.com on 8.8.8.8:53: server misbehaving`)},
		{name: "connection timed out", err: fmt.Errorf(`dial tcp 1.2.3.4:443: connect: connection timed out`)},
		{name: "operation timed out", err: fmt.Errorf(`Post "https://example.com/v1/chat/completions": dial tcp 1.2.3.4:443: connect: operation timed out`)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !isConnectionLifecycleError(tc.err) {
				t.Fatalf("isConnectionLifecycleError(%v) = false, want true", tc.err)
			}
			got := resultErrorFromError(tc.err)
			if !isTransportFailureResultError(got) {
				t.Fatalf("isTransportFailureResultError(%#v) = false, want true", got)
			}
			if !shouldSkipCredentialCooldown(got) {
				t.Fatalf("shouldSkipCredentialCooldown(%#v) = false, want true", got)
			}

			// Plain auth: the new transport texts must skip cooldown.
			m := NewManager(nil, nil, nil)
			auth := &Auth{ID: "auth-transport-new-" + tc.name, Provider: "codex"}
			if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}
			m.MarkResult(context.Background(), Result{
				AuthID:   auth.ID,
				Provider: auth.Provider,
				Model:    model,
				Success:  false,
				Error:    got,
			})
			assertNoCooldown(t, m, auth.ID, model)

			// Per-auth proxy: the same texts must flow through the
			// isTransportFailureResultError override and keep cooldown.
			mProxy := NewManager(nil, nil, nil)
			authProxy := &Auth{ID: "auth-transport-new-proxy-" + tc.name, Provider: "codex", ProxyURL: "http://127.0.0.1:7890"}
			if _, errRegister := mProxy.Register(context.Background(), authProxy); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}
			mProxy.MarkResult(context.Background(), Result{
				AuthID:   authProxy.ID,
				Provider: authProxy.Provider,
				Model:    model,
				Success:  false,
				Error:    got,
			})
			updated, ok := mProxy.GetByID(authProxy.ID)
			if !ok || updated == nil {
				t.Fatalf("expected auth to be present")
			}
			state := updated.ModelStates[model]
			if state == nil || state.NextRetryAfter.IsZero() {
				t.Fatalf("expected per-auth proxy transport failure to keep cooldown")
			}
		})
	}
}

func TestManager_MarkResult_TransportFailureWithSharedVertexLocationSkipsCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	transportErr := resultErrorFromError(fmt.Errorf("dial tcp: lookup us-central1-aiplatform.googleapis.com: no such host"))
	model := "gemini-2.5-pro"

	// Shared location: the fault may be regional and affect both auths, so
	// cooldown must stay skipped.
	mShared := NewManager(nil, nil, nil)
	sharedA := newVertexSATestAuth("auth-vertex-shared-a", "us-central1")
	sharedB := newVertexSATestAuth("auth-vertex-shared-b", "us-central1")
	for _, a := range []*Auth{sharedA, sharedB} {
		if _, errRegister := mShared.Register(context.Background(), a); errRegister != nil {
			t.Fatalf("register auth: %v", errRegister)
		}
	}
	// Both auths register the route model, so the shared-location peer is a
	// pollable alternative and the endpoint is not unique.
	registerClientModelForTest(t, sharedA.ID, model)
	registerClientModelForTest(t, sharedB.ID, model)
	mShared.MarkResult(context.Background(), Result{
		AuthID:   sharedA.ID,
		Provider: sharedA.Provider,
		Model:    model,
		Success:  false,
		Error:    transportErr,
	})
	assertNoCooldown(t, mShared, sharedA.ID, model)

	// Single vertex auth: no peer offers an alternative endpoint, so skipping
	// cooldown must stay in effect (original transport-failure fix).
	mSingle := NewManager(nil, nil, nil)
	single := newVertexSATestAuth("auth-vertex-single", "us-central1")
	if _, errRegister := mSingle.Register(context.Background(), single); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	mSingle.MarkResult(context.Background(), Result{
		AuthID:   single.ID,
		Provider: single.Provider,
		Model:    model,
		Success:  false,
		Error:    transportErr,
	})
	assertNoCooldown(t, mSingle, single.ID, model)
}

func TestManager_MarkResult_VertexSharedHostNetworkFailureSkipsCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	model := "gemini-2.5-pro"
	cases := []struct {
		name string
		err  error
	}{
		{name: "network unreachable", err: fmt.Errorf("dial tcp 142.250.4.1:443: connect: network is unreachable")},
		{name: "dns server misbehaving", err: fmt.Errorf("dial tcp: lookup europe-west4-aiplatform.googleapis.com: server misbehaving")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !isTransportFailureResultError(resultErrorFromError(tc.err)) {
				t.Fatalf("isTransportFailureResultError(%v) = false, want true", tc.err)
			}

			// Unique-location vertex auths: shared-host faults cannot prove the
			// regional endpoint failed, so cooldown must stay skipped.
			m := NewManager(nil, nil, nil)
			authUS := newVertexSATestAuth("auth-vertex-shared-fault-us", "us-central1")
			authEU := newVertexSATestAuth("auth-vertex-shared-fault-eu", "europe-west4")
			for _, a := range []*Auth{authUS, authEU} {
				if _, errRegister := m.Register(context.Background(), a); errRegister != nil {
					t.Fatalf("register auth: %v", errRegister)
				}
			}
			m.MarkResult(context.Background(), Result{
				AuthID:   authEU.ID,
				Provider: authEU.Provider,
				Model:    model,
				Success:  false,
				Error:    resultErrorFromError(tc.err),
			})
			assertNoCooldown(t, m, authEU.ID, model)

			// Auth-level path (empty model) must behave identically.
			m.MarkResult(context.Background(), Result{
				AuthID:   authUS.ID,
				Provider: authUS.Provider,
				Success:  false,
				Error:    resultErrorFromError(tc.err),
			})
			updatedUS, okUS := m.GetByID(authUS.ID)
			if !okUS || updatedUS == nil {
				t.Fatalf("expected auth to be present")
			}
			if updatedUS.Unavailable || !updatedUS.NextRetryAfter.IsZero() {
				t.Fatalf("expected shared-host fault to skip auth-level cooldown, got unavailable=%v next=%v", updatedUS.Unavailable, updatedUS.NextRetryAfter)
			}
		})
	}
}

func TestManager_MarkResult_VertexEndpointConnectionRefusedStillCooldowns(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	m := NewManager(nil, nil, nil)
	authUS := newVertexSATestAuth("auth-vertex-refused-us", "us-central1")
	authEU := newVertexSATestAuth("auth-vertex-refused-eu", "europe-west4")
	for _, a := range []*Auth{authUS, authEU} {
		if _, errRegister := m.Register(context.Background(), a); errRegister != nil {
			t.Fatalf("register auth: %v", errRegister)
		}
	}
	endpointErr := resultErrorFromError(fmt.Errorf("dial tcp 142.250.4.1:443: connect: connection refused"))
	if !isTransportFailureResultError(endpointErr) {
		t.Fatalf("isTransportFailureResultError(%#v) = false, want true", endpointErr)
	}

	// A direct endpoint failure (connection refused) must keep cooldown when
	// the location is unique in the pool.
	model := "gemini-2.5-pro"
	// The US peer must register the route model to count as a pollable
	// alternative endpoint for the model-level attribution below.
	registerClientModelForTest(t, authUS.ID, model)
	registerClientModelForTest(t, authEU.ID, model)
	m.MarkResult(context.Background(), Result{
		AuthID:   authEU.ID,
		Provider: authEU.Provider,
		Model:    model,
		Success:  false,
		Error:    endpointErr,
	})
	updated, ok := m.GetByID(authEU.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil || state.NextRetryAfter.IsZero() {
		t.Fatalf("expected unique-location vertex endpoint failure (connection refused) to keep cooldown")
	}

	// Auth-level path (empty model): the only other vertex SA peer (authEU)
	// is already cooling from the model-level step above, so it cannot serve
	// requests and the failing auth is the only pollable credential. It must
	// not cool down (same principle as the disabled-peer case).
	m.MarkResult(context.Background(), Result{
		AuthID:   authUS.ID,
		Provider: authUS.Provider,
		Success:  false,
		Error:    endpointErr,
	})
	updatedUS, okUS := m.GetByID(authUS.ID)
	if !okUS || updatedUS == nil {
		t.Fatalf("expected auth to be present")
	}
	if updatedUS.Unavailable || !updatedUS.NextRetryAfter.IsZero() {
		t.Fatalf("expected cooling-peer failure to skip auth-level cooldown, got unavailable=%v next=%v", updatedUS.Unavailable, updatedUS.NextRetryAfter)
	}
}

func TestManager_MarkResult_VertexDisabledPeerSkipsCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	m := NewManager(nil, nil, nil)
	authUS := newVertexSATestAuth("auth-vertex-disabled-peer-us", "us-central1")
	authUS.Disabled = true
	authEU := newVertexSATestAuth("auth-vertex-disabled-peer-eu", "europe-west4")
	for _, a := range []*Auth{authUS, authEU} {
		if _, errRegister := m.Register(context.Background(), a); errRegister != nil {
			t.Fatalf("register auth: %v", errRegister)
		}
	}
	endpointErr := resultErrorFromError(fmt.Errorf("dial tcp 142.250.4.1:443: connect: connection refused"))

	model := "gemini-2.5-pro"
	m.MarkResult(context.Background(), Result{
		AuthID:   authEU.ID,
		Provider: authEU.Provider,
		Model:    model,
		Success:  false,
		Error:    endpointErr,
	})
	// The only other vertex SA auth is disabled, so the failing auth is the
	// only pollable credential and must not be cooled down.
	assertNoCooldown(t, m, authEU.ID, model)

	// Auth-level path (empty model) must behave identically.
	m.MarkResult(context.Background(), Result{
		AuthID:   authEU.ID,
		Provider: authEU.Provider,
		Success:  false,
		Error:    endpointErr,
	})
	updated, ok := m.GetByID(authEU.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if updated.Unavailable || !updated.NextRetryAfter.IsZero() {
		t.Fatalf("expected disabled-peer failure to skip auth-level cooldown, got unavailable=%v next=%v", updated.Unavailable, updated.NextRetryAfter)
	}
}

func TestManager_MarkResult_VertexCoolingPeerStillCooldowns(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	now := time.Now()
	model := "gemini-2.5-pro"
	endpointErr := resultErrorFromError(fmt.Errorf("dial tcp 142.250.4.1:443: connect: connection refused"))

	// Model-level path: the failing EU auth shares its location only with a
	// peer that is itself cooling down (quota cooldown for the same model),
	// so the scheduler would never rotate to that peer. The healthy US auth
	// offers the only reachable endpoint, so the failing auth must cool down
	// to let rotation switch locations.
	mModel := NewManager(nil, nil, nil)
	authEU := newVertexSATestAuth("auth-vertex-cool-peer-eu", "europe-west4")
	authEUPeer := newVertexSATestAuth("auth-vertex-cool-peer-eu-peer", "europe-west4")
	authEUPeer.ModelStates = map[string]*ModelState{
		model: {
			Status:         StatusError,
			Unavailable:    true,
			NextRetryAfter: now.Add(10 * time.Minute),
			Quota: QuotaState{
				Exceeded:      true,
				Reason:        "quota",
				NextRecoverAt: now.Add(10 * time.Minute),
			},
		},
	}
	authUS := newVertexSATestAuth("auth-vertex-cool-peer-us", "us-central1")
	for _, a := range []*Auth{authEU, authEUPeer, authUS} {
		if _, errRegister := mModel.Register(context.Background(), a); errRegister != nil {
			t.Fatalf("register auth: %v", errRegister)
		}
	}
	// The US auth must register the route model to count as the only reachable
	// alternative endpoint for the model-level attribution below.
	registerClientModelForTest(t, authUS.ID, model)
	registerClientModelForTest(t, authEU.ID, model)
	registerClientModelForTest(t, authEUPeer.ID, model)
	if blocked, _, _ := isAuthBlockedForModel(authEUPeer, model, now); !blocked {
		t.Fatalf("expected cooling peer to be blocked for model")
	}
	mModel.MarkResult(context.Background(), Result{
		AuthID:   authEU.ID,
		Provider: authEU.Provider,
		Model:    model,
		Success:  false,
		Error:    endpointErr,
	})
	updated, ok := mModel.GetByID(authEU.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil || state.NextRetryAfter.IsZero() {
		t.Fatalf("expected cooling same-location peer to keep model cooldown")
	}

	// Auth-level path (empty model): same scenario with an auth-level cooling
	// peer.
	mAuth := NewManager(nil, nil, nil)
	authEULevel := newVertexSATestAuth("auth-vertex-cool-peer-level-eu", "europe-west4")
	authPeerLevel := newVertexSATestAuth("auth-vertex-cool-peer-level-eu-peer", "europe-west4")
	authPeerLevel.Unavailable = true
	authPeerLevel.NextRetryAfter = now.Add(10 * time.Minute)
	authPeerLevel.Quota = QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(10 * time.Minute)}
	authUSLevel := newVertexSATestAuth("auth-vertex-cool-peer-level-us", "us-central1")
	for _, a := range []*Auth{authEULevel, authPeerLevel, authUSLevel} {
		if _, errRegister := mAuth.Register(context.Background(), a); errRegister != nil {
			t.Fatalf("register auth: %v", errRegister)
		}
	}
	if blocked, _, _ := isAuthBlockedForModel(authPeerLevel, "", now); !blocked {
		t.Fatalf("expected auth-level cooling peer to be blocked")
	}
	mAuth.MarkResult(context.Background(), Result{
		AuthID:   authEULevel.ID,
		Provider: authEULevel.Provider,
		Success:  false,
		Error:    endpointErr,
	})
	updatedLevel, okLevel := mAuth.GetByID(authEULevel.ID)
	if !okLevel || updatedLevel == nil {
		t.Fatalf("expected auth-level auth to be present")
	}
	if updatedLevel.NextRetryAfter.IsZero() {
		t.Fatalf("expected cooling same-location peer to keep auth-level cooldown")
	}
}

func TestManager_MarkResult_VertexExpiredCooldownPeerSkipsCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	now := time.Now()
	model := "gemini-2.5-pro"
	endpointErr := resultErrorFromError(fmt.Errorf("dial tcp 142.250.4.1:443: connect: connection refused"))

	m := NewManager(nil, nil, nil)
	authEU := newVertexSATestAuth("auth-vertex-expired-peer-eu", "europe-west4")
	authPeer := newVertexSATestAuth("auth-vertex-expired-peer-eu-peer", "europe-west4")
	authPeer.ModelStates = map[string]*ModelState{
		model: {
			Status:         StatusError,
			Unavailable:    true,
			NextRetryAfter: now.Add(-time.Minute),
			Quota: QuotaState{
				Exceeded:      true,
				Reason:        "quota",
				NextRecoverAt: now.Add(-time.Minute),
			},
		},
	}
	for _, a := range []*Auth{authEU, authPeer} {
		if _, errRegister := m.Register(context.Background(), a); errRegister != nil {
			t.Fatalf("register auth: %v", errRegister)
		}
	}
	// The same-location peer must also register the route model: its cooldown
	// expired and it can serve the model, so the endpoint stays shared.
	registerClientModelForTest(t, authEU.ID, model)
	registerClientModelForTest(t, authPeer.ID, model)
	if blocked, _, _ := isAuthBlockedForModel(authPeer, model, now); blocked {
		t.Fatalf("expected expired-cooldown peer to be pollable")
	}
	m.MarkResult(context.Background(), Result{
		AuthID:   authEU.ID,
		Provider: authEU.Provider,
		Model:    model,
		Success:  false,
		Error:    endpointErr,
	})
	// The same-location peer's cooldown has expired, so it is pollable again
	// and the endpoint is shared: cooldown must stay skipped.
	assertNoCooldown(t, m, authEU.ID, model)
}

func TestManager_MarkResult_VertexModelIneligiblePeerStillCooldowns(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	model := "gemini-2.5-pro"
	endpointErr := resultErrorFromError(fmt.Errorf("dial tcp 142.250.4.1:443: connect: connection refused"))

	m := NewManager(nil, nil, nil)
	authEU := newVertexSATestAuth("auth-vertex-model-ineligible-eu", "europe-west4")
	// Same-location peer that is ready but does not register the route model.
	// Request selection would never pick it for this model, so it must not
	// hide endpoint uniqueness either.
	authEUPeer := newVertexSATestAuth("auth-vertex-model-ineligible-eu-peer", "europe-west4")
	authUS := newVertexSATestAuth("auth-vertex-model-ineligible-us", "us-central1")
	for _, a := range []*Auth{authEU, authEUPeer, authUS} {
		if _, errRegister := m.Register(context.Background(), a); errRegister != nil {
			t.Fatalf("register auth: %v", errRegister)
		}
	}
	// Only the US credential registers the route model, so it is the only
	// pollable alternative endpoint for this model.
	registerClientModelForTest(t, authUS.ID, model)

	now := time.Now()
	if blocked, _, _ := isAuthBlockedForModel(authEUPeer, model, now); blocked {
		t.Fatalf("expected same-location peer to be ready")
	}
	registryRef := registry.GetGlobalRegistry()
	if m.authSupportsRouteModel(registryRef, authEUPeer, model) {
		t.Fatalf("expected model-ineligible peer to be excluded from selection")
	}
	if !m.authSupportsRouteModel(registryRef, authUS, model) {
		t.Fatalf("expected US auth to support the route model")
	}

	m.MarkResult(context.Background(), Result{
		AuthID:   authEU.ID,
		Provider: authEU.Provider,
		Model:    model,
		Success:  false,
		Error:    endpointErr,
	})
	updated, ok := m.GetByID(authEU.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil || state.NextRetryAfter.IsZero() {
		t.Fatalf("expected model-ineligible same-location peer to keep model cooldown")
	}
}

func TestManager_MarkResult_TransportFailureWithSocksProxyDialSkipsVertexAttribution(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	m := NewManager(nil, nil, nil)
	authUS := newVertexSATestAuth("auth-vertex-socks-us", "us-central1")
	authEU := newVertexSATestAuth("auth-vertex-socks-eu", "europe-west4")
	for _, a := range []*Auth{authUS, authEU} {
		if _, errRegister := m.Register(context.Background(), a); errRegister != nil {
			t.Fatalf("register auth: %v", errRegister)
		}
	}
	model := "gemini-2.5-pro"

	// x/net/proxy SOCKS5 dialer failures surface as
	// "socks connect tcp <proxy>-><target>: <err>" (net.OpError with
	// Op "socks connect", see golang.org/x/net/internal/socks). The message
	// carries no "proxyconnect tcp", so it previously matched both the
	// transport and vertex endpoint pattern tables and drained the pool by
	// attributing a shared socks5 proxy outage to each unique-location
	// Vertex credential (cfg-level proxy, no per-auth ProxyURL override).
	socksErr := resultErrorFromError(fmt.Errorf("socks connect tcp 127.0.0.1:1080->europe-west4-aiplatform.googleapis.com:443: connection refused"))
	if !isTransportFailureResultError(socksErr) {
		t.Fatalf("isTransportFailureResultError(%#v) = false, want true", socksErr)
	}
	if isVertexEndpointFailureMessage(socksErr.Message) {
		t.Fatalf("isVertexEndpointFailureMessage(%q) = true, want false", socksErr.Message)
	}

	// Model-level path: the socks dial failure must skip cooldown even though
	// the failing auth's location is unique in the pool.
	m.MarkResult(context.Background(), Result{
		AuthID:   authEU.ID,
		Provider: authEU.Provider,
		Model:    model,
		Success:  false,
		Error:    socksErr,
	})
	assertNoCooldown(t, m, authEU.ID, model)

	// Auth-level path (empty model) must behave identically.
	m.MarkResult(context.Background(), Result{
		AuthID:   authUS.ID,
		Provider: authUS.Provider,
		Success:  false,
		Error:    socksErr,
	})
	updatedUS, okUS := m.GetByID(authUS.ID)
	if !okUS || updatedUS == nil {
		t.Fatalf("expected auth to be present")
	}
	if updatedUS.Unavailable || !updatedUS.NextRetryAfter.IsZero() {
		t.Fatalf("expected socks dial failure to skip auth-level cooldown, got unavailable=%v next=%v", updatedUS.Unavailable, updatedUS.NextRetryAfter)
	}
}

func TestManager_MarkResult_VertexEndpointConnectionTimedOutStillCooldowns(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	m := NewManager(nil, nil, nil)
	authUS := newVertexSATestAuth("auth-vertex-timedout-us", "us-central1")
	authEU := newVertexSATestAuth("auth-vertex-timedout-eu", "europe-west4")
	for _, a := range []*Auth{authUS, authEU} {
		if _, errRegister := m.Register(context.Background(), a); errRegister != nil {
			t.Fatalf("register auth: %v", errRegister)
		}
	}
	// A direct dial timeout against the regional endpoint is a per-endpoint
	// fault (unlike "operation timed out", which is shared-host) and must
	// keep cooldown when the location is unique in the pool. This guards the
	// "connection timed out" pattern in vertexEndpointFailureMessagePatterns
	// from being dropped as shared-host.
	endpointErr := resultErrorFromError(fmt.Errorf("dial tcp 142.250.4.1:443: connect: connection timed out"))
	if !isTransportFailureResultError(endpointErr) {
		t.Fatalf("isTransportFailureResultError(%#v) = false, want true", endpointErr)
	}
	if !isVertexEndpointFailureMessage(endpointErr.Message) {
		t.Fatalf("isVertexEndpointFailureMessage(%q) = false, want true", endpointErr.Message)
	}

	model := "gemini-2.5-pro"
	// The US peer must register the route model to count as a pollable
	// alternative endpoint for the model-level attribution below.
	registerClientModelForTest(t, authUS.ID, model)
	registerClientModelForTest(t, authEU.ID, model)
	m.MarkResult(context.Background(), Result{
		AuthID:   authEU.ID,
		Provider: authEU.Provider,
		Model:    model,
		Success:  false,
		Error:    endpointErr,
	})
	updated, ok := m.GetByID(authEU.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil || state.NextRetryAfter.IsZero() {
		t.Fatalf("expected unique-location vertex connection timed out to keep cooldown")
	}
}

func TestManager_MarkResult_TransportFailureWithDirectOrNoneProxySkipsCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	transportErr := resultErrorFromError(fmt.Errorf(`Post "https://example.com/v1/chat/completions": net/http: TLS handshake timeout`))
	if !isTransportFailureResultError(transportErr) {
		t.Fatalf("isTransportFailureResultError(%#v) = false, want true", transportErr)
	}

	// proxyutil.Parse resolves "direct" and "none" to ModeDirect (explicit
	// bypass, no proxy endpoint), so neither is a per-auth proxy override.
	// The real-proxy counterpart (http://127.0.0.1:7890 keeping cooldown) is
	// covered by TestManager_MarkResult_TransportFailureWithAuthProxyStillCooldowns.
	for _, proxyValue := range []string{"direct", "none"} {
		t.Run("proxy="+proxyValue, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			auth := &Auth{ID: "auth-proxy-" + proxyValue, Provider: "codex", ProxyURL: proxyValue}
			if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}
			model := "gpt-5.6-sol"

			// Model-level path must skip cooldown.
			m.MarkResult(context.Background(), Result{
				AuthID:   auth.ID,
				Provider: auth.Provider,
				Model:    model,
				Success:  false,
				Error:    transportErr,
			})
			assertNoCooldown(t, m, auth.ID, model)

			// Auth-level path must skip cooldown as well.
			m.MarkResult(context.Background(), Result{
				AuthID:   auth.ID,
				Provider: auth.Provider,
				Success:  false,
				Error:    transportErr,
			})
			updated, ok := m.GetByID(auth.ID)
			if !ok || updated == nil {
				t.Fatalf("expected auth to be present")
			}
			if updated.Unavailable || !updated.NextRetryAfter.IsZero() {
				t.Fatalf("expected direct/none proxy transport failure to skip auth-level cooldown, got unavailable=%v next=%v", updated.Unavailable, updated.NextRetryAfter)
			}
		})
	}
}

func TestManager_MarkResult_VertexProviderCaseNormalizedStillCooldowns(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	m := NewManager(nil, nil, nil)
	authUS := newVertexSATestAuth("auth-vertex-case-us", "us-central1")
	authEU := newVertexSATestAuth("auth-vertex-case-eu", "europe-west4")
	// Provider "Vertex" with a capital first letter must still be recognized
	// as a Vertex service account, mirroring how executorKeyFromAuth trims and
	// lowercases the provider for selection.
	authUS.Provider = "Vertex"
	authEU.Provider = "Vertex"
	for _, a := range []*Auth{authUS, authEU} {
		if _, errRegister := m.Register(context.Background(), a); errRegister != nil {
			t.Fatalf("register auth: %v", errRegister)
		}
	}
	endpointErr := resultErrorFromError(fmt.Errorf("dial tcp 142.250.4.1:443: connect: connection refused"))
	if !isVertexEndpointFailureMessage(endpointErr.Message) {
		t.Fatalf("isVertexEndpointFailureMessage(%q) = false, want true", endpointErr.Message)
	}

	model := "gemini-2.5-pro"
	registerClientModelForTest(t, authUS.ID, model)
	registerClientModelForTest(t, authEU.ID, model)
	m.MarkResult(context.Background(), Result{
		AuthID:   authEU.ID,
		Provider: authEU.Provider,
		Model:    model,
		Success:  false,
		Error:    endpointErr,
	})
	updated, ok := m.GetByID(authEU.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil || state.NextRetryAfter.IsZero() {
		t.Fatalf("expected normalized Vertex provider to keep cooldown for unique location")
	}
}

func TestManager_MarkResult_VertexWeightedPeerZeroWeightStillCooldowns(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	// Weighted selector: the same-location EU peer carries weight 0 and is
	// never picked for this model, while the healthy US peer (weight 1) is the
	// only pollable alternative endpoint, so the EU failure is auth-specific.
	m := NewManager(nil, &WeightedRoundRobinSelector{}, nil)
	authEU := newVertexSATestAuth("auth-vertex-weight-eu", "europe-west4")
	authEU2 := newVertexSATestAuth("auth-vertex-weight-eu2", "europe-west4")
	authUS := newVertexSATestAuth("auth-vertex-weight-us", "us-central1")
	authEU2.Attributes = map[string]string{AttributeWeight: "0"}
	authUS.Attributes = map[string]string{AttributeWeight: "1"}
	for _, a := range []*Auth{authEU, authEU2, authUS} {
		if _, errRegister := m.Register(context.Background(), a); errRegister != nil {
			t.Fatalf("register auth: %v", errRegister)
		}
	}
	endpointErr := resultErrorFromError(fmt.Errorf("dial tcp 142.250.4.1:443: connect: connection refused"))

	model := "gemini-2.5-pro"
	for _, a := range []*Auth{authEU, authEU2, authUS} {
		registerClientModelForTest(t, a.ID, model)
	}
	m.MarkResult(context.Background(), Result{
		AuthID:   authEU.ID,
		Provider: authEU.Provider,
		Model:    model,
		Success:  false,
		Error:    endpointErr,
	})
	updated, ok := m.GetByID(authEU.ID)
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil || state.NextRetryAfter.IsZero() {
		t.Fatalf("expected weighted zero-weight same-location peer to keep cooldown")
	}
}

func TestManager_MarkResult_VertexRoundRobinIgnoresWeightSkipsCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	prevTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(5)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(prevTransient) })

	// Default round-robin selector ignores weights entirely, so the
	// same-location EU peer (weight 0) still counts as a pollable alternative
	// endpoint and the failure is not auth-specific: cooldown is skipped.
	m := NewManager(nil, nil, nil)
	authEU := newVertexSATestAuth("auth-vertex-rr-eu", "europe-west4")
	authEU2 := newVertexSATestAuth("auth-vertex-rr-eu2", "europe-west4")
	authUS := newVertexSATestAuth("auth-vertex-rr-us", "us-central1")
	authEU2.Attributes = map[string]string{AttributeWeight: "0"}
	authUS.Attributes = map[string]string{AttributeWeight: "1"}
	for _, a := range []*Auth{authEU, authEU2, authUS} {
		if _, errRegister := m.Register(context.Background(), a); errRegister != nil {
			t.Fatalf("register auth: %v", errRegister)
		}
	}
	endpointErr := resultErrorFromError(fmt.Errorf("dial tcp 142.250.4.1:443: connect: connection refused"))

	model := "gemini-2.5-pro"
	for _, a := range []*Auth{authEU, authEU2, authUS} {
		registerClientModelForTest(t, a.ID, model)
	}
	m.MarkResult(context.Background(), Result{
		AuthID:   authEU.ID,
		Provider: authEU.Provider,
		Model:    model,
		Success:  false,
		Error:    endpointErr,
	})
	assertNoCooldown(t, m, authEU.ID, model)
}
