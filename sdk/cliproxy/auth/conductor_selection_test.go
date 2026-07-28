package auth

import (
	"context"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type mockStoppableSelector struct {
	stopped bool
}

func (m *mockStoppableSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	if len(auths) > 0 {
		return auths[0], nil
	}
	return nil, nil
}

func (m *mockStoppableSelector) Stop() {
	m.stopped = true
}

func TestManager_SetSelector_StopsOldSessionAffinitySelector(t *testing.T) {
	m := NewManager(nil, nil, nil)
	oldSel := NewSessionAffinitySelector(&RoundRobinSelector{})
	m.SetSelector(oldSel)

	newSel := &RoundRobinSelector{}
	m.SetSelector(newSel)

	select {
	case <-oldSel.cache.stopCh:
		// Success: old SessionAffinitySelector was stopped
	default:
		t.Fatal("expected old SessionAffinitySelector to be stopped")
	}

	// Test with mockStoppableSelector as well
	mockOld := &mockStoppableSelector{}
	m.SetSelector(mockOld)

	mockNew := &mockStoppableSelector{}
	m.SetSelector(mockNew)

	if !mockOld.stopped {
		t.Fatal("expected mockOld selector to be stopped")
	}
	if mockNew.stopped {
		t.Fatal("expected mockNew selector to not be stopped")
	}
}

func TestManager_SetSelector_SameSelectorNotStopped(t *testing.T) {
	m := NewManager(nil, nil, nil)
	sel := NewSessionAffinitySelector(&RoundRobinSelector{})
	m.SetSelector(sel)

	// Setting the same selector instance again
	m.SetSelector(sel)

	select {
	case <-sel.cache.stopCh:
		t.Fatal("expected selector to NOT be stopped when re-set with the same instance")
	default:
		// Success: still open
	}

	// Test with mockStoppableSelector
	mockSel := &mockStoppableSelector{}
	m.SetSelector(mockSel)
	m.SetSelector(mockSel)

	if mockSel.stopped {
		t.Fatal("expected mockSel to NOT be stopped when re-set with the same instance")
	}
}
