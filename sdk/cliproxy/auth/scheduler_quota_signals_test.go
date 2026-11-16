package auth

import (
	"context"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type quotaSignalsPluginScheduler struct {
	*fakePluginScheduler
	quotaSignals bool
}

func (s quotaSignalsPluginScheduler) SchedulerWantsQuotaSignals() bool { return s.quotaSignals }

func newQuotaSignalsTestManager(t *testing.T) (*Manager, time.Time) {
	t.Helper()
	observedAt := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	observed := &Auth{ID: "auth-observed", Provider: "codex", Quota: QuotaState{
		ObservedAt: observedAt,
		Signals: map[string]string{
			"X-Codex-Secondary-Used-Percent": "42",
			"X-Codex-Secondary-Reset-At":     "1794130700",
		},
	}}
	unobserved := &Auth{ID: "auth-unobserved", Provider: "codex"}
	for _, auth := range []*Auth{observed, unobserved} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
	}
	return manager, observedAt
}

func candidateByID(t *testing.T, req pluginapi.SchedulerPickRequest, id string) pluginapi.SchedulerAuthCandidate {
	t.Helper()
	for _, candidate := range req.Candidates {
		if candidate.ID == id {
			return candidate
		}
	}
	t.Fatalf("candidate %q missing from %+v", id, req.Candidates)
	return pluginapi.SchedulerAuthCandidate{}
}

func TestManagerPluginSchedulerQuotaSignalsOptIn(t *testing.T) {
	manager, observedAt := newQuotaSignalsTestManager(t)
	fake := &fakePluginScheduler{
		handled: true,
		pick: func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
			// Mutating the snapshot must not leak back into the host's auth state.
			for _, candidate := range req.Candidates {
				if candidate.Quota != nil {
					candidate.Quota.Signals["X-Codex-Secondary-Used-Percent"] = "999"
				}
			}
			return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-observed"}, true, nil
		},
	}
	manager.SetPluginScheduler(quotaSignalsPluginScheduler{fakePluginScheduler: fake, quotaSignals: true})

	got, _, errPick := manager.pickNext(context.Background(), "codex", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}
	if got == nil || got.ID != "auth-observed" {
		t.Fatalf("picked auth = %v, want auth-observed", got)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("len(requests) = %d, want 1", len(fake.requests))
	}

	observed := candidateByID(t, fake.requests[0], "auth-observed")
	if observed.Quota == nil {
		t.Fatal("observed candidate Quota = nil, want snapshot")
	}
	if !observed.Quota.ObservedAt.Equal(observedAt) {
		t.Fatalf("Quota.ObservedAt = %v, want %v", observed.Quota.ObservedAt, observedAt)
	}
	if observed.Quota.Signals["X-Codex-Secondary-Reset-At"] != "1794130700" {
		t.Fatalf("Quota.Signals = %v, want the observed reset", observed.Quota.Signals)
	}
	if unobserved := candidateByID(t, fake.requests[0], "auth-unobserved"); unobserved.Quota != nil {
		t.Fatalf("unobserved candidate Quota = %+v, want nil", unobserved.Quota)
	}

	stored, ok := manager.GetByID("auth-observed")
	if !ok || stored.Quota.Signals["X-Codex-Secondary-Used-Percent"] != "42" {
		t.Fatalf("host auth signals = %v, want unchanged after plugin mutation", stored.Quota.Signals)
	}
}

func TestManagerPluginSchedulerQuotaSignalsDefaultOmitted(t *testing.T) {
	manager, _ := newQuotaSignalsTestManager(t)
	fake := &fakePluginScheduler{
		handled: true,
		resp:    pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-observed"},
	}
	manager.SetPluginScheduler(fake)

	if _, _, errPick := manager.pickNext(context.Background(), "codex", "", cliproxyexecutor.Options{}, nil); errPick != nil {
		t.Fatalf("pickNext() error = %v", errPick)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("len(requests) = %d, want 1", len(fake.requests))
	}
	for _, candidate := range fake.requests[0].Candidates {
		if candidate.Quota != nil {
			t.Fatalf("candidate %s Quota = %+v, want nil without opt-in", candidate.ID, candidate.Quota)
		}
	}
}
