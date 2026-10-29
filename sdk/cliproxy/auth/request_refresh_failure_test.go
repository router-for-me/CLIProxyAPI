package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type demandUnauthorizedExecutor struct{ *demandTestExecutor }

func (e *demandUnauthorizedExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls.Add(1)
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: 401, Message: "synthetic rejected inference bearer"}
}

func TestRequestRefreshUnauthorizedRefreshPreservesActualFailure(t *testing.T) {
	m, a, _, e := demandManager(t, time.Now().Add(time.Hour))
	failure := errors.New("synthetic actual refresh failure after 401")
	e.refresh = func(context.Context, *Auth) (*Auth, error) { return nil, failure }
	m.RegisterExecutor(&demandUnauthorizedExecutor{e})
	_, err := m.Execute(context.Background(), []string{a.Provider}, cliproxyexecutor.Request{Model: "demand-model"}, cliproxyexecutor.Options{})
	if !errors.Is(err, failure) || e.calls.Load() != 1 || e.refreshes.Load() != 1 {
		t.Fatalf("401 lost actual refresh failure: %v calls=%d refreshes=%d", err, e.calls.Load(), e.refreshes.Load())
	}
}

func TestRequestRefreshQuotaWithUnknownRecoveryStaysBlocked(t *testing.T) {
	m, a, _, e := demandManager(t, time.Now().Add(-time.Hour))
	a.Quota = QuotaState{Exceeded: true, Reason: "credential_quota"}
	a.ModelStates = map[string]*ModelState{"other": {}}
	_, _ = m.Update(context.Background(), a)
	_, err := m.Execute(context.Background(), []string{a.Provider}, cliproxyexecutor.Request{Model: "demand-model"}, cliproxyexecutor.Options{})
	if err == nil || e.refreshes.Load() != 0 || e.calls.Load() != 0 {
		t.Fatalf("unknown quota recovery admitted: %v", err)
	}
}
