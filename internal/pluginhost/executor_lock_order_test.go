package pluginhost

import (
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// hostLockProbeManager reports whether Host.mu is held by the caller while the
// executor manager is entered. coreauth.Manager takes its own lock in these
// methods, and its credential selection calls back into the Host, so holding
// Host.mu here produces an ABBA deadlock.
type hostLockProbeManager struct {
	*fakeExecutorManager
	host *Host
	held []string
}

func (m *hostLockProbeManager) probe(op string) {
	if m.host.mu.TryLock() {
		m.host.mu.Unlock()
		return
	}
	m.held = append(m.held, op)
}

func (m *hostLockProbeManager) Executor(provider string) (coreauth.ProviderExecutor, bool) {
	m.probe("Executor")
	return m.fakeExecutorManager.Executor(provider)
}

func (m *hostLockProbeManager) RegisterExecutor(executor coreauth.ProviderExecutor) {
	m.probe("RegisterExecutor")
	m.fakeExecutorManager.RegisterExecutor(executor)
}

func (m *hostLockProbeManager) UnregisterExecutor(provider string) {
	m.probe("UnregisterExecutor")
	m.fakeExecutorManager.UnregisterExecutor(provider)
}

func TestRegisterExecutorsDoesNotHoldHostLockWhileCallingManager(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "alpha",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			Executor: &fakeExecutor{identifier: "provider-a"},
		}},
	})
	manager := &hostLockProbeManager{fakeExecutorManager: newFakeExecutorManager(), host: host}

	host.RegisterExecutors(manager, nil)
	if manager.registerCalls != 1 {
		t.Fatalf("RegisterExecutor calls = %d, want 1", manager.registerCalls)
	}

	// Replace the snapshot so provider-a becomes stale and is unregistered.
	setHostSnapshotForTest(host, true, capabilityRecord{
		id: "bravo",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			Executor: &fakeExecutor{identifier: "provider-b"},
		}},
	})
	host.RegisterExecutors(manager, nil)
	if len(manager.unregisters) != 1 {
		t.Fatalf("UnregisterExecutor calls = %v, want provider-a", manager.unregisters)
	}

	if len(manager.held) != 0 {
		t.Fatalf("Host.mu was held during manager calls: %v", manager.held)
	}
}
