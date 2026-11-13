package auth

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type authPolicyUnauthorizedExecutor struct {
	*authUseExecutor
	bootstrap bool
	refreshes atomic.Int64
}

func (e *authPolicyUnauthorizedExecutor) result(kind AuthUseKind, a *Auth) error {
	e.record(kind, a)
	if authAccessToken(a) == "synthetic-a" {
		return &Error{HTTPStatus: http.StatusUnauthorized, Message: "fixture credential rejected"}
	}
	return nil
}
func (e *authPolicyUnauthorizedExecutor) Execute(_ context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, e.result(AuthUseExecute, a)
}
func (e *authPolicyUnauthorizedExecutor) CountTokens(_ context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(`{"input_tokens":1}`)}, e.result(AuthUseCount, a)
}
func (e *authPolicyUnauthorizedExecutor) ExecuteStream(_ context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	err := e.result(AuthUseStream, a)
	if err != nil && !e.bootstrap {
		return nil, err
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	if err != nil {
		chunks <- cliproxyexecutor.StreamChunk{Err: err}
	} else {
		chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: {\"ok\":true}\n\n")}
	}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}
func (e *authPolicyUnauthorizedExecutor) Refresh(ctx context.Context, a *Auth) (*Auth, error) {
	e.refreshes.Add(1)
	return rotatedTransitionAuth(ctx, a)
}

func TestAuthExecutionPolicyRefreshRefusalStopsUnauthorizedRetry(t *testing.T) {
	for _, path := range []string{"execute", "count", "stream", "stream-bootstrap"} {
		t.Run(path, func(t *testing.T) {
			f := newAuthUseFixture(t)
			_, _, err := f.manager.Modify(f.ctx, f.auth.ID, 0, func(a *Auth) error {
				a.Attributes["priority"] = "10"
				a.Metadata["refresh_token"] = "synthetic-grant"
				return nil
			})
			if err != nil {
				t.Fatal("refresh refusal fixture setup failed")
			}
			other := f.auth.Clone()
			other.ID += "-alternate"
			other.Metadata["access_token"] = "synthetic-alternate"
			if _, err := f.manager.Register(f.ctx, other); err != nil {
				t.Fatal("refresh alternate registration failed")
			}
			registry.GetGlobalRegistry().RegisterClient(other.ID, other.Provider, []*registry.ModelInfo{{ID: f.model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(other.ID) })
			e := &authPolicyUnauthorizedExecutor{authUseExecutor: f.exec, bootstrap: path == "stream-bootstrap"}
			f.manager.RegisterExecutor(e)
			refreshChecks := 0
			f.manager.SetAuthExecutionPolicy(authUsePolicyFunc(func(_ context.Context, use AuthUse) error {
				if use.Kind == AuthUseRefresh {
					refreshChecks++
					return errors.New("private fixture refresh policy failure")
				}
				return nil
			}))
			kind := AuthUseKind(path)
			if e.bootstrap {
				kind = AuthUseStream
			}
			requireAuthUseRefusal(t, f.invoke(kind, cliproxyexecutor.Options{}))
			_, inputs := f.exec.observed()
			if refreshChecks != 1 || e.refreshes.Load() != 0 || len(inputs) != 1 || inputs[0].ID != f.auth.ID {
				t.Fatal("refresh policy refusal retried or used an alternate credential")
			}
			current, ok := f.manager.GetByID(f.auth.ID)
			if !ok || current.RejectedAccessToken != "synthetic-a" {
				t.Fatal("original unauthorized response was not recorded as rejection")
			}
			// The original 401 can reject its token; the local policy refusal
			// must add no result, cooldown, or credential failure.
			f.requireNoHealthResult(t)
		})
	}
}
