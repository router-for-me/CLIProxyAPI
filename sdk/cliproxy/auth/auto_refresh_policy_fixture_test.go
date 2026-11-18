package auth

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type policyRefreshExecutor struct {
	authUseExecutor
	refreshes atomic.Int64
}

func (e *policyRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	e.refreshes.Add(1)
	updated := auth.Clone()
	updated.Metadata["expired"] = time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	return updated, nil
}

type policyRefreshFixture struct {
	manager              *Manager
	exec                 *policyRefreshExecutor
	hook                 *authUseResultHook
	auth                 *Auth
	loop                 *authAutoRefreshLoop
	ctx                  context.Context
	cancel               context.CancelFunc
	loopDone, workerDone chan struct{}
}

func newPolicyRefreshFixture(t *testing.T) *policyRefreshFixture {
	t.Helper()
	hook := &authUseResultHook{}
	exec := &policyRefreshExecutor{authUseExecutor: authUseExecutor{provider: "policy-refresh-fixture"}}
	m := NewManager(nil, nil, hook)
	m.RegisterExecutor(exec)
	auth, err := m.Register(context.Background(), &Auth{
		ID: "policy-refresh-fixture", Provider: exec.provider, Status: StatusActive,
		Metadata: map[string]any{
			"access_token": "synthetic-access", "refresh_token": "synthetic-grant",
			"expired":                  time.Now().Add(-time.Hour).Format(time.RFC3339),
			"refresh_interval_seconds": 3600,
		},
	})
	if err != nil || auth == nil {
		t.Fatal("policy refresh fixture registration failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	loop := newAuthAutoRefreshLoop(m, time.Hour, 1)
	m.refreshLoop = loop
	return &policyRefreshFixture{manager: m, exec: exec, hook: hook, auth: auth, loop: loop, ctx: ctx, cancel: cancel}
}

func (f *policyRefreshFixture) start() {
	f.loopDone, f.workerDone = make(chan struct{}), make(chan struct{})
	f.loop.rebuild(time.Now())
	// Run the production dispatcher and worker separately so both have explicit
	// joins. No scheduler behavior is replaced by the fixture.
	go func() { defer close(f.loopDone); f.loop.loop(f.ctx) }()
	go func() { defer close(f.workerDone); f.loop.worker(f.ctx) }()
}

func (f *policyRefreshFixture) stop(t *testing.T) {
	t.Helper()
	f.cancel()
	synctest.Wait()
	for _, done := range []chan struct{}{f.loopDone, f.workerDone} {
		if done != nil {
			select {
			case <-done:
			default:
				t.Error("policy refresh worker cleanup did not finish")
			}
		}
	}
	f.loop.releaseQueuedJobs()
}

func (f *policyRefreshFixture) current(t *testing.T) *Auth {
	t.Helper()
	auth, ok := f.manager.GetByID(f.auth.ID)
	if !ok || auth == nil {
		t.Fatal("policy refresh credential missing")
	}
	return auth
}

func (f *policyRefreshFixture) requireHealthy(t *testing.T) {
	t.Helper()
	auth := f.current(t)
	if auth.Status != StatusActive || auth.Disabled || auth.Unavailable || auth.Failed != 0 ||
		auth.LastError != nil || auth.RefreshError != nil || auth.RefreshFailures != 0 ||
		auth.RequiresSignIn || !auth.NextRetryAfter.IsZero() || f.hook.results.Load() != 0 {
		t.Fatal("policy refresh refusal changed upstream health")
	}
}

func (f *policyRefreshFixture) refusedJob(t *testing.T) *authRefreshJob {
	t.Helper()
	f.manager.SetAuthExecutionPolicy(authUsePolicyFunc(func(context.Context, AuthUse) error { return context.Canceled }))
	f.loop.rebuild(time.Now())
	f.loop.handleDue(f.ctx, time.Now())
	var job *authRefreshJob
	select {
	case job = <-f.loop.jobs:
	default:
		t.Fatal("policy refresh job was not queued")
	}
	if !f.manager.beginRefreshJob(f.ctx, job) {
		t.Fatal("policy refresh job did not start")
	}
	_, err := f.manager.refreshAuthForRequestAtEpoch(f.ctx, job.id, "", job.registrationEpoch)
	requireAuthUseRefusal(t, err)
	return job
}
