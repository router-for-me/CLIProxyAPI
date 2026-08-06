package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
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
		{name: "unexpected EOF", err: &Error{Message: "unexpected EOF"}},
		{name: "plain EOF", err: &Error{Message: "EOF"}},
		{name: "wrapped unexpected EOF", err: &Error{Message: "read tcp 127.0.0.1:1->127.0.0.1:2: unexpected EOF"}},
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
		io.EOF,
		io.ErrUnexpectedEOF,
		&websocket.CloseError{Code: websocket.CloseNormalClosure, Text: "normal"},
		&websocket.CloseError{Code: websocket.CloseGoingAway, Text: "bye"},
		&websocket.CloseError{Code: websocket.CloseAbnormalClosure, Text: "unexpected EOF"},
		fmt.Errorf("upstream read: %w", &websocket.CloseError{Code: websocket.CloseAbnormalClosure, Text: "unexpected EOF"}),
		fmt.Errorf("wrap: %w", io.ErrUnexpectedEOF),
		errors.New("websocket: close 1000 (normal)"),
		errors.New("websocket: close 1006 (abnormal closure): unexpected EOF"),
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
