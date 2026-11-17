package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func newTOSViolationError() *Error {
	return &Error{
		HTTPStatus: http.StatusForbidden,
		Code:       "tos_violation",
		Message:    "This service has been disabled in this account for violation of Terms of Service.",
	}
}

func registerTOSRotationAuths(t *testing.T, m *Manager, prefix string) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	for _, id := range []string{prefix + "-1", prefix + "-2", prefix + "-3"} {
		a := &Auth{ID: id, Provider: "antigravity", Status: StatusActive}
		reg.RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: "gemini-3-pro"}})
		idCopy := id
		t.Cleanup(func() { reg.UnregisterClient(idCopy) })
		if _, err := m.Register(context.Background(), a); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
}

func assertTOSStopError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var authErr *Error
	if !errors.As(err, &authErr) || authErr == nil || authErr.Code != "tos_violation" || authErr.HTTPStatus != http.StatusForbidden {
		t.Fatalf("error = %T/%v, want the original 403 tos_violation error", err, err)
	}
}

// A TOS_VIOLATION must end the request on the first credential: replaying the
// same payload on the next credential would get that credential suspended too.
func TestTOSViolation_Execute_DoesNotRotateCredentials(t *testing.T) {
	m := NewManager(nil, nil, nil)
	registerTOSRotationAuths(t, m, "tos-exec")

	calls := 0
	m.RegisterExecutor(&mockCustomErrorExecutor{
		identifier: "antigravity",
		executeFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			calls++
			cliproxyexecutor.MarkUpstreamAttempt(ctx)
			return cliproxyexecutor.Response{}, newTOSViolationError()
		},
	})

	_, err := m.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: "gemini-3-pro"}, cliproxyexecutor.Options{})
	assertTOSStopError(t, err)
	if calls != 1 {
		t.Fatalf("executor calls = %d, want 1 (no rotation after TOS_VIOLATION)", calls)
	}
}

func TestTOSViolation_ExecuteStream_DoesNotRotateCredentials(t *testing.T) {
	m := NewManager(nil, nil, nil)
	registerTOSRotationAuths(t, m, "tos-stream")

	calls := 0
	m.RegisterExecutor(&customStreamMockExecutor{
		identifier: "antigravity",
		streamFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			calls++
			cliproxyexecutor.MarkUpstreamAttempt(ctx)
			return nil, newTOSViolationError()
		},
	})

	_, err := m.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: "gemini-3-pro"}, cliproxyexecutor.Options{})
	assertTOSStopError(t, err)
	if calls != 1 {
		t.Fatalf("stream executor calls = %d, want 1 (no rotation after TOS_VIOLATION)", calls)
	}
}

func TestTOSViolation_ExecuteCount_DoesNotRotateCredentials(t *testing.T) {
	m := NewManager(nil, nil, nil)
	registerTOSRotationAuths(t, m, "tos-count")

	calls := 0
	m.RegisterExecutor(&mockCustomErrorExecutor{
		identifier: "antigravity",
		countFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			calls++
			cliproxyexecutor.MarkUpstreamAttempt(ctx)
			return cliproxyexecutor.Response{}, newTOSViolationError()
		},
	})

	_, err := m.ExecuteCount(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: "gemini-3-pro"}, cliproxyexecutor.Options{})
	assertTOSStopError(t, err)
	if calls != 1 {
		t.Fatalf("count executor calls = %d, want 1 (no rotation after TOS_VIOLATION)", calls)
	}
}

// Non-TOS 403s keep the existing rotation behaviour.
func TestNonTOS403_Execute_StillRotates(t *testing.T) {
	m := NewManager(nil, nil, nil)
	registerTOSRotationAuths(t, m, "plain403")

	calls := 0
	m.RegisterExecutor(&mockCustomErrorExecutor{
		identifier: "antigravity",
		executeFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			calls++
			cliproxyexecutor.MarkUpstreamAttempt(ctx)
			return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusForbidden, Message: "forbidden"}
		},
	})

	_, _ = m.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: "gemini-3-pro"}, cliproxyexecutor.Options{})
	if calls < 2 {
		t.Fatalf("executor calls = %d, want rotation to continue for a non-TOS 403", calls)
	}
}
