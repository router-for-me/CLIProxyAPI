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
	transportErr := resultErrorFromError(fmt.Errorf("dial tcp: lookup europe-west4-aiplatform.googleapis.com: no such host"))

	model := "gemini-2.5-pro"
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
