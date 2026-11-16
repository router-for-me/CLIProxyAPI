package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type continuityTestScheduler struct {
	decide func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error)
}

func (s *continuityTestScheduler) PickAuth(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	if len(req.Candidates) == 0 {
		return pluginapi.SchedulerPickResponse{}, false, nil
	}
	return pluginapi.SchedulerPickResponse{Handled: true, AuthID: req.Candidates[0].ID}, true, nil
}
func (s *continuityTestScheduler) SchedulerContinuity(ctx context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
	return s.decide(ctx, req)
}

type continuity429Executor struct {
	schedulerTestExecutor
	mu     sync.Mutex
	calls  int
	status int
}

func (e *continuity429Executor) Identifier() string { return "codex" }
func (e *continuity429Executor) Execute(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	if e.calls == 1 {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: e.status, Message: "fixture"}
	}
	return cliproxyexecutor.Response{Payload: []byte("recovered")}, nil
}
func (e *continuity429Executor) ExecuteStream(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	if e.calls == 1 {
		return nil, &Error{HTTPStatus: e.status, Message: "fixture"}
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: recovered\n\n")}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func TestContinuityGateRechecksAndBoundsTotalWait(t *testing.T) {
	now := time.Unix(1000, 0)
	budget := &continuityBudget{}
	calls := 0
	decide := func() (pluginapi.SchedulerContinuityResponse, bool, error) {
		calls++
		if calls == 2 {
			return pluginapi.SchedulerContinuityResponse{Action: "ready"}, true, nil
		}
		return pluginapi.SchedulerContinuityResponse{Action: "wait", RetryAfterMS: 1000, MaxWaitMS: 3000}, true, nil
	}
	action, waited, err := runContinuityGate(context.Background(), budget, decide, func() time.Time { return now }, func(_ context.Context, d time.Duration) error { now = now.Add(d); return nil })
	if err != nil || !waited || action != "ready" || calls != 2 {
		t.Fatalf("first gate: %q %v %v calls=%d", action, waited, err, calls)
	}
	deadline := budget.deadline
	calls = 0
	_, _, err = runContinuityGate(context.Background(), budget, func() (pluginapi.SchedulerContinuityResponse, bool, error) {
		calls++
		return pluginapi.SchedulerContinuityResponse{Action: "wait", RetryAfterMS: 1000, MaxWaitMS: 240000}, true, nil
	}, func() time.Time { return now }, func(_ context.Context, d time.Duration) error { now = now.Add(d); return nil })
	if err == nil || !budget.deadline.Equal(deadline) || calls != 2 {
		t.Fatalf("shared deadline extended: %v %v calls=%d", budget.deadline, err, calls)
	}
	calls = 0
	_, _, err = runContinuityGate(context.Background(), budget, func() (pluginapi.SchedulerContinuityResponse, bool, error) {
		calls++
		return pluginapi.SchedulerContinuityResponse{Action: "ready"}, true, nil
	}, func() time.Time { return now }, func(context.Context, time.Duration) error { return nil })
	if err == nil || calls != 0 {
		t.Fatalf("ready after deadline accepted: %v calls=%d", err, calls)
	}
}

func TestContinuityGateHonorsCancellationAfterDecision(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, _, err := runContinuityGate(ctx, &continuityBudget{}, func() (pluginapi.SchedulerContinuityResponse, bool, error) {
		cancel()
		return pluginapi.SchedulerContinuityResponse{Action: "ready"}, true, nil
	}, time.Now, waitContinuityTimer)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestContinuityCandidatesPreserveQuotaAndFilterOtherNativeBlocks(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(schedulerTestExecutor{provider: "codex"})
	now := time.Now()
	future := now.Add(time.Hour)
	model := "continuity-model"
	auths := []*Auth{
		{ID: "quota", Index: "quota-index", Provider: "codex", ModelStates: map[string]*ModelState{model: {Status: StatusError, Unavailable: true, NextRetryAfter: future, Quota: QuotaState{Exceeded: true, NextRecoverAt: future}, LastError: &Error{HTTPStatus: 429}}}},
		{ID: "overload", Provider: "codex", ModelStates: map[string]*ModelState{model: {Status: StatusError, Unavailable: true, NextRetryAfter: future, LastError: &Error{HTTPStatus: 503}}}},
		{ID: "unauthorized", Provider: "codex", Status: StatusError, Unavailable: true, LastError: &Error{HTTPStatus: 401}},
		{ID: "disabled", Provider: "codex", Disabled: true},
	}
	for _, a := range auths {
		if _, err := m.Register(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	registerSchedulerModels(t, "codex", model, "quota", "overload", "unauthorized", "disabled")
	var got pluginapi.SchedulerContinuityRequest
	m.SetPluginScheduler(&continuityTestScheduler{decide: func(_ context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
		got = req
		return pluginapi.SchedulerContinuityResponse{Action: "ready"}, true, nil
	}})
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.RequestLifecycleIDMetadataKey: "execution-123"}}
	_, handled, err := m.schedulerContinuityDecision(context.Background(), []string{"codex"}, model, opts, false)
	if err != nil || !handled || got.RequestID != "execution-123" || len(got.Pick.Candidates) != 1 || got.Pick.Candidates[0].ID != "quota" || got.Pick.Candidates[0].Metadata["auth_index"] != "quota-index" {
		t.Fatalf("bad candidates: %#v %v", got, err)
	}
	opts.Metadata[cliproxyexecutor.PinnedAuthMetadataKey] = "overload"
	_, handled, err = m.schedulerContinuityDecision(context.Background(), []string{"codex"}, model, opts, false)
	if err != nil || handled {
		t.Fatalf("blocked pinned account reached gate: %v %v", handled, err)
	}
}

func TestContinuityConfirmed429WaitsThenRetriesOneRejectedAttempt(t *testing.T) {
	m := NewManager(nil, nil, nil)
	ex := &continuity429Executor{status: http.StatusTooManyRequests}
	m.RegisterExecutor(ex)
	if _, err := m.Register(context.Background(), &Auth{ID: "quota-auth", Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	var rejected int
	m.SetPluginScheduler(&continuityTestScheduler{decide: func(_ context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
		if req.PreviousAttemptRejected429 {
			rejected++
			return pluginapi.SchedulerContinuityResponse{Action: "wait", Reason: "five_hour_exhausted", RetryAfterMS: 1000, MaxWaitMS: 5000}, true, nil
		}
		if rejected > 0 {
			m.mu.Lock()
			a := m.auths["quota-auth"]
			a.Unavailable = false
			a.NextRetryAfter = time.Time{}
			a.Quota = QuotaState{}
			a.LastError = nil
			a.ModelStates = nil
			a.Status = StatusActive
			m.mu.Unlock()
		}
		return pluginapi.SchedulerContinuityResponse{Action: "ready"}, true, nil
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	result, err := m.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{Stream: true, Metadata: map[string]any{cliproxyexecutor.RequestLifecycleIDMetadataKey: "request-1"}})
	if err != nil || result == nil || rejected != 1 || ex.calls != 2 {
		t.Fatalf("err=%v result=%v rejected=%d calls=%d", err, result, rejected, ex.calls)
	}
	chunk := <-result.Chunks
	if string(chunk.Payload) != "data: recovered\n\n" {
		t.Fatalf("payload=%q", chunk.Payload)
	}
}

func TestContinuityDoesNotReplayNon429(t *testing.T) {
	m := NewManager(nil, nil, nil)
	ex := &continuity429Executor{status: http.StatusInternalServerError}
	m.RegisterExecutor(ex)
	if _, err := m.Register(context.Background(), &Auth{ID: "error-auth", Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	var rejected int
	m.SetPluginScheduler(&continuityTestScheduler{decide: func(_ context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
		if req.PreviousAttemptRejected429 {
			rejected++
		}
		return pluginapi.SchedulerContinuityResponse{Action: "ready"}, true, nil
	}})
	_, err := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{Stream: true})
	if err == nil || rejected != 0 || ex.calls != 1 {
		t.Fatalf("err=%v rejected=%d calls=%d", err, rejected, ex.calls)
	}
}

func TestContinuityNonStreamConfirmed429WaitsThenRetries(t *testing.T) {
	m := NewManager(nil, nil, nil)
	ex := &continuity429Executor{status: http.StatusTooManyRequests}
	m.RegisterExecutor(ex)
	if _, err := m.Register(context.Background(), &Auth{ID: "nonstream-auth", Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	var rejected int
	m.SetPluginScheduler(&continuityTestScheduler{decide: func(_ context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
		if req.PreviousAttemptRejected429 {
			rejected++
			return pluginapi.SchedulerContinuityResponse{Action: "wait", Reason: "five_hour_exhausted", RetryAfterMS: 1000, MaxWaitMS: 5000}, true, nil
		}
		if rejected > 0 {
			m.mu.Lock()
			a := m.auths["nonstream-auth"]
			a.Unavailable = false
			a.NextRetryAfter = time.Time{}
			a.Quota = QuotaState{}
			a.LastError = nil
			a.ModelStates = nil
			a.Status = StatusActive
			m.mu.Unlock()
		}
		return pluginapi.SchedulerContinuityResponse{Action: "ready"}, true, nil
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	result, err := m.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.RequestLifecycleIDMetadataKey: "request-nonstream"}})
	if err != nil || string(result.Payload) != "recovered" || rejected != 1 || ex.calls != 2 {
		t.Fatalf("err=%v payload=%q rejected=%d calls=%d", err, result.Payload, rejected, ex.calls)
	}
}

func TestContinuityGateObservesDisabledAfterReload(t *testing.T) {
	now := time.Unix(2000, 0)
	calls := 0
	action, waited, err := runContinuityGate(context.Background(), &continuityBudget{}, func() (pluginapi.SchedulerContinuityResponse, bool, error) {
		calls++
		if calls == 1 {
			return pluginapi.SchedulerContinuityResponse{Action: "wait", RetryAfterMS: 1000, MaxWaitMS: 6000}, true, nil
		}
		return pluginapi.SchedulerContinuityResponse{Action: "disabled"}, true, nil
	}, func() time.Time { return now }, func(_ context.Context, d time.Duration) error { now = now.Add(d); return nil })
	if err != nil || action != "disabled" || !waited || calls != 2 {
		t.Fatalf("reload gate action=%q waited=%v calls=%d err=%v", action, waited, calls, err)
	}
}
