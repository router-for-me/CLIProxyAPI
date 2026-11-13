package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
)

type authExecutionRefusal struct{}

func (authExecutionRefusal) Error() string         { return "selected account execution is unavailable" }
func (authExecutionRefusal) StatusCode() int       { return http.StatusServiceUnavailable }
func (authExecutionRefusal) IsRequestStop() bool   { return true }
func (authExecutionRefusal) IsRequestScoped() bool { return true }

func isAuthExecutionRefusal(err error) bool {
	var refusal authExecutionRefusal
	return errors.As(err, &refusal)
}

func (m *Manager) executionPolicy() AuthExecutionPolicy {
	if m == nil {
		return nil
	}
	if holder := m.authExecutionPolicy.Load(); holder != nil {
		return holder.policy
	}
	return nil
}

func (m *Manager) checkAuthExecutionHome() error {
	if m.executionPolicy() != nil && m.HomeEnabled() {
		return authExecutionRefusal{}
	}
	return nil
}

func (m *Manager) checkAuthExecution(ctx context.Context, kind AuthUseKind, auth *Auth, requestedModel, model string) error {
	policy := m.executionPolicy()
	if policy == nil {
		return nil
	}
	if err := m.checkAuthExecutionHome(); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if auth == nil {
		return authExecutionRefusal{}
	}
	if id := strings.TrimSpace(auth.ID); id != "" {
		release, err := m.lockAuthExecutionRead(ctx, id)
		if err != nil {
			return requestStopError{err}
		}
		defer release()
	}
	snapshot, err := snapshotAuthForPolicy(auth)
	if err != nil {
		return authExecutionRefusal{}
	}
	if err := ctx.Err(); err != nil {
		return requestStopError{err}
	}
	if policy.Check(ctx, AuthUse{Kind: kind, Auth: snapshot, RequestedModel: requestedModel, Model: model}) != nil {
		return authExecutionRefusal{}
	}
	if err := ctx.Err(); err != nil {
		return requestStopError{err}
	}
	return nil
}

// Share the publication barrier without the writer path's metadata-save
// preemption. The callback completes before this permit is released; provider IO
// always happens after release. No manager mutex is held during the callback.
func (m *Manager) lockAuthExecutionRead(ctx context.Context, id string) (func(), error) {
	if err := m.authLoadGate.Acquire(ctx, 1); err != nil {
		return nil, err
	}
	value, _ := m.authMutationLocks.LoadOrStore(id, make(chan struct{}, 1))
	gate := value.(chan struct{})
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-gate
			m.authLoadGate.Release(1)
			return nil, err
		}
	case <-ctx.Done():
		m.authLoadGate.Release(1)
		return nil, ctx.Err()
	}
	var once sync.Once
	return func() { once.Do(func() { <-gate; m.authLoadGate.Release(1) }) }, nil
}
