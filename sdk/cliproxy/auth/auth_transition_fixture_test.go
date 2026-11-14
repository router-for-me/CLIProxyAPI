package auth

import (
	"context"
	"sync/atomic"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type authTransitionPolicy struct {
	check   func(context.Context, AuthUse) error
	observe func(context.Context, AuthTransition)
}

func (p *authTransitionPolicy) Check(ctx context.Context, use AuthUse) error {
	if p.check != nil {
		return p.check(ctx, use)
	}
	return nil
}
func (p *authTransitionPolicy) ObserveTransition(ctx context.Context, transition AuthTransition) {
	if p.observe != nil {
		p.observe(ctx, transition)
	}
}

type authTransitionExecutor struct {
	*authUseExecutor
	refresh      func(context.Context, *Auth) (*Auth, error)
	refreshCalls atomic.Int64
	execute      func(context.Context, *Auth) error
}

func (e *authTransitionExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	e.refreshCalls.Add(1)
	return e.refresh(ctx, auth)
}
func (e *authTransitionExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.execute != nil {
		if err := e.execute(ctx, auth); err != nil {
			return cliproxyexecutor.Response{}, err
		}
	}
	return e.authUseExecutor.Execute(ctx, auth, req, opts)
}

type authTransitionStore struct{ save func(*Auth) }

func (s *authTransitionStore) Save(_ context.Context, auth *Auth) (string, error) {
	if s.save != nil {
		s.save(auth)
	}
	return "", nil
}
func (*authTransitionStore) List(context.Context) ([]*Auth, error) { return nil, nil }
func (*authTransitionStore) Delete(context.Context, string) error  { return nil }

func rotatedTransitionAuth(_ context.Context, a *Auth) (*Auth, error) {
	a.Metadata["access_token"] = "synthetic-rotated"
	return a, nil
}
