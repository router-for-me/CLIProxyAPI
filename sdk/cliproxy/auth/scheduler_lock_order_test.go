package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// lockProbeScheduler records whether Manager.mu was free for writers while
// SchedulerWantsAcrossPriorities ran. A real plugin host takes its own mutex
// there, and the same mutex is held across Manager.RegisterExecutor (which
// needs Manager.mu for writing), so the call must not run under Manager.mu.RLock.
type lockProbeScheduler struct {
	fakePluginScheduler
	manager *Manager
	probed  int
	blocked int
}

func (s *lockProbeScheduler) SchedulerWantsAcrossPriorities() bool {
	s.probed++
	if s.manager.mu.TryLock() {
		s.manager.mu.Unlock()
	} else {
		s.blocked++
	}
	return false
}

func newLockProbeManager(t *testing.T) (*Manager, *lockProbeScheduler) {
	t.Helper()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["gemini"] = schedulerTestExecutor{}
	if _, err := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "gemini"}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	scheduler := &lockProbeScheduler{manager: manager}
	manager.SetPluginScheduler(scheduler)
	return manager, scheduler
}

func TestPickNextDoesNotCallSchedulerAcrossPrioritiesUnderManagerLock(t *testing.T) {
	manager, scheduler := newLockProbeManager(t)
	if _, _, err := manager.pickNextLegacy(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil); err != nil {
		t.Fatalf("pickNextLegacy() error = %v", err)
	}
	if scheduler.probed == 0 {
		t.Fatal("SchedulerWantsAcrossPriorities was never consulted")
	}
	if scheduler.blocked != 0 {
		t.Fatalf("SchedulerWantsAcrossPriorities ran under Manager.mu %d time(s)", scheduler.blocked)
	}
}

func TestPickNextMixedDoesNotCallSchedulerAcrossPrioritiesUnderManagerLock(t *testing.T) {
	manager, scheduler := newLockProbeManager(t)
	if _, _, _, err := manager.pickNextMixedLegacy(context.Background(), []string{"gemini"}, "", cliproxyexecutor.Options{}, nil); err != nil {
		t.Fatalf("pickNextMixedLegacy() error = %v", err)
	}
	if scheduler.probed == 0 {
		t.Fatal("SchedulerWantsAcrossPriorities was never consulted")
	}
	if scheduler.blocked != 0 {
		t.Fatalf("SchedulerWantsAcrossPriorities ran under Manager.mu %d time(s)", scheduler.blocked)
	}
}

func TestPluginSchedulerWantsAcrossPrioritiesDoesNotHoldManagerLock(t *testing.T) {
	manager, scheduler := newLockProbeManager(t)
	manager.PluginSchedulerWantsAcrossPriorities()
	if scheduler.probed != 1 || scheduler.blocked != 0 {
		t.Fatalf("probed=%d blocked=%d, want 1 and 0", scheduler.probed, scheduler.blocked)
	}
}
