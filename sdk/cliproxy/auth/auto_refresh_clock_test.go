package auth

import (
	"context"
	"testing"
	"time"
)

func TestAuthAutoRefreshLoop_DeadlinesUseWallClock(t *testing.T) {
	now := time.Now()
	deadline := now.Add(30 * time.Minute)
	for _, operation := range []string{"rebuild", "insert", "update"} {
		t.Run(operation, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			if _, errRegister := manager.Register(context.Background(), &Auth{
				ID: "retry", Provider: "test", NextRefreshAfter: deadline,
			}); errRegister != nil {
				t.Fatal(errRegister)
			}
			loop := newAuthAutoRefreshLoop(manager, time.Second, 1)
			switch operation {
			case "rebuild":
				loop.rebuild(now)
			case "insert":
				loop.upsert("retry", deadline)
			case "update":
				loop.upsert("retry", deadline.Add(time.Hour))
				loop.upsert("retry", deadline)
			}
			got, ok := loop.peek()
			if !ok || got != deadline.Round(0) {
				t.Fatalf("queued deadline = %v, want wall-clock-only %v", got, deadline.Round(0))
			}
			if due := loop.popDue(deadline.Add(-time.Nanosecond)); len(due) != 0 {
				t.Fatalf("refresh scheduled before its deadline: %v", due)
			}
			if due := loop.popDue(deadline); len(due) != 1 || due[0] != "retry" {
				t.Fatalf("due at deadline = %v, want [retry]", due)
			}
		})
	}
}

type refreshClockObserver struct{ observed time.Time }

func (e *refreshClockObserver) ShouldRefresh(now time.Time, _ *Auth) bool {
	e.observed = now
	return true
}

func TestAuthAutoRefreshLoop_DueEvaluationUsesWallClock(t *testing.T) {
	now := time.Now()
	manager := NewManager(nil, nil, nil)
	evaluator := &refreshClockObserver{}
	manager.RegisterExecutor(&mockOAuthErrorExecutor{id: "refresh-clock"})
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID: "due", Provider: "refresh-clock", Runtime: evaluator,
	}); errRegister != nil {
		t.Fatal(errRegister)
	}
	loop := newAuthAutoRefreshLoop(manager, time.Second, 1)
	loop.handleDueAuth(context.Background(), now, "due")
	if evaluator.observed != now.Round(0) {
		t.Fatalf("refresh evaluation time = %v, want wall-clock-only %v", evaluator.observed, now.Round(0))
	}
	select {
	case job := <-loop.jobs:
		if job.pendingUntil != now.Add(refreshPendingBackoff).Round(0) {
			t.Fatalf("pending deadline retains a monotonic clock: %v", job.pendingUntil)
		}
		manager.finishRefreshJob(job, time.Time{}, false)
	default:
		t.Fatal("due credential was not queued for refresh")
	}
}
