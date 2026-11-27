package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

const saveWaitRefreshProvider = "save-wait-refresh"

// failingHeldRefreshExecutor holds its first refresh until released, then fails
// it, so the job ends without committing anything.
type failingHeldRefreshExecutor struct {
	countingRefreshExecutor
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func (e *failingHeldRefreshExecutor) Refresh(context.Context, *Auth) (*Auth, error) {
	e.once.Do(func() { close(e.started) })
	<-e.release
	return nil, errors.New("refresh failed")
}

// A job whose refresh fails never reaches a commit or a hook; it must still stop
// counting as saving when it returns, or WaitAutoRefreshSaves would wait forever.
func TestWaitAutoRefreshSaves_WaitsForJobThatFails(t *testing.T) {
	executor := &failingHeldRefreshExecutor{
		countingRefreshExecutor: countingRefreshExecutor{id: saveWaitRefreshProvider},
		started:                 make(chan struct{}),
		release:                 make(chan struct{}),
	}
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(executor)
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID:       "save-wait-failing",
		Provider: saveWaitRefreshProvider,
		Runtime:  codexRenewalEvaluator{},
	}); errRegister != nil {
		t.Fatalf("Register error: %v", errRegister)
	}
	released := false
	releaseRefresh := func() {
		if !released {
			released = true
			close(executor.release)
		}
	}
	t.Cleanup(releaseRefresh)
	manager.StartAutoRefresh(context.Background(), 10*time.Millisecond)

	// The timer only guards against a hang.
	hangGuard := time.NewTimer(30 * time.Second)
	defer hangGuard.Stop()
	select {
	case <-executor.started:
	case <-hangGuard.C:
		t.Fatal("auto-refresh never started the refresh")
	}
	manager.StopAutoRefresh()

	expired, cancel := context.WithCancel(context.Background())
	cancel()
	if errWait := manager.WaitAutoRefreshSaves(expired); !errors.Is(errWait, context.Canceled) {
		t.Fatalf("WaitAutoRefreshSaves with the refresh still held = %v, want context.Canceled", errWait)
	}

	releaseRefresh()
	waitDone := make(chan error, 1)
	go func() { waitDone <- manager.WaitAutoRefreshSaves(context.Background()) }()
	select {
	case errWait := <-waitDone:
		if errWait != nil {
			t.Fatalf("WaitAutoRefreshSaves after the refresh failed = %v, want nil", errWait)
		}
	case <-hangGuard.C:
		t.Fatal("WaitAutoRefreshSaves still waiting after the failed refresh returned")
	}
}

// With no auto-refresh job running, the wait returns at once.
func TestWaitAutoRefreshSaves_IdleReturnsAtOnce(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	if errWait := manager.WaitAutoRefreshSaves(expired); errWait != nil {
		t.Fatalf("WaitAutoRefreshSaves on an idle manager = %v, want nil", errWait)
	}
	var nilManager *Manager
	if errWait := nilManager.WaitAutoRefreshSaves(context.Background()); errWait != nil {
		t.Fatalf("WaitAutoRefreshSaves on a nil manager = %v, want nil", errWait)
	}
}
