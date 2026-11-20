package pluginhost

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestExternalUsageRejectsVolatileHandoff(t *testing.T) {
	for _, phase := range []string{"quiescing", "replaying", "async-draining", "pending", "closed"} {
		t.Run(phase, func(t *testing.T) {
			var calls atomic.Int32
			host := newHostWithRecords(capabilityRecord{id: "usage", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{UsagePlugin: usagePluginFunc(func(context.Context, pluginapi.UsageRecord) { calls.Add(1) })}}})
			state := host.usageStateForPlugin("usage")
			state.mu.Lock()
			switch phase {
			case "quiescing":
				state.quiescing = true
			case "replaying":
				state.replaying = true
			case "async-draining":
				state.asyncDraining = true
			case "pending":
				state.pending = append(state.pending, pendingPluginUsageRecord{})
			case "closed":
				state.closed = true
			}
			before := len(state.pending)
			state.mu.Unlock()
			if errDelivery := host.HandleUsage(t.Context(), pluginapi.UsageRecord{RequestID: "external"}); !errors.Is(errDelivery, ErrUsageDeliveryPending) {
				t.Fatalf("pending delivery error=%v", errDelivery)
			}
			state.mu.Lock()
			pending, inFlight := len(state.pending), state.inFlight
			state.mu.Unlock()
			if pending != before || inFlight != 0 || calls.Load() != 0 {
				t.Fatalf("external event was adopted: pending=%d inFlight=%d callbacks=%d", pending, inFlight, calls.Load())
			}
		})
	}
}

func TestExternalUsageStaleSnapshotIsNotSuccessfulDelivery(t *testing.T) {
	var calls atomic.Int32
	host := newHostWithRecords(capabilityRecord{id: "usage", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{UsagePlugin: usagePluginFunc(func(context.Context, pluginapi.UsageRecord) { calls.Add(1) })}}})
	// Model a reader holding the old snapshot after identity changed.
	host.mu.Lock()
	host.activePluginVersions["usage"] = "replacement"
	host.mu.Unlock()
	if !host.HasUsagePlugins() {
		t.Fatal("stale advertised consumer disappeared from the delivery contract")
	}
	if errDelivery := host.HandleUsage(t.Context(), pluginapi.UsageRecord{}); !errors.Is(errDelivery, ErrUsageDeliveryUnavailable) {
		t.Fatalf("stale consumer was acknowledged without a callback: %v", errDelivery)
	}
	if calls.Load() != 0 {
		t.Fatal("stale consumer was invoked")
	}
}

func TestExternalUsageInterruptedReplayRetainsSenderOwnership(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	host := newHostWithRecords(capabilityRecord{id: "usage", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{UsagePlugin: usagePluginFunc(func(_ context.Context, event pluginapi.UsageRecord) {
		if event.RequestID != "local" {
			t.Errorf("external event reached a replaying host: %q", event.RequestID)
		}
		close(entered)
		<-release
	})}}})
	if !host.beginUsageReplacement(t.Context(), "usage") {
		t.Fatal("replacement did not start")
	}
	// Local standalone delivery keeps its existing asynchronous replay policy.
	(&usageAdapter{host: host, pluginID: "usage"}).HandleUsage(t.Context(), coreusage.Record{RequestID: "local"})
	finished := make(chan struct{})
	go func() { defer close(finished); host.finishUsageReplacement("usage") }()
	defer func() { releaseOnce.Do(func() { close(release) }); <-finished }()
	waitForHostTestSignal(t, entered, "local replay callback")
	record := pluginapi.UsageRecord{RequestID: "external"}
	if errDelivery := host.HandleUsage(t.Context(), record); !errors.Is(errDelivery, ErrUsageDeliveryPending) {
		t.Fatalf("pending external delivery error=%v", errDelivery)
	}
	state := host.usageStateForPlugin("usage")
	state.mu.Lock()
	pending := len(state.pending)
	state.mu.Unlock()
	if pending != 0 {
		t.Fatalf("external event was adopted into volatile memory: %d", pending)
	}
	// Discard the old host's volatile state, as during process interruption.
	host.clearAllUsageReplacements()
	var received []pluginapi.UsageRecord
	restarted := newHostWithRecords(capabilityRecord{id: "usage", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{UsagePlugin: usagePluginFunc(func(_ context.Context, event pluginapi.UsageRecord) { received = append(received, event) })}}})
	if errDelivery := restarted.HandleUsage(t.Context(), record); errDelivery != nil {
		t.Fatal(errDelivery)
	}
	if len(received) != 1 || received[0].RequestID != record.RequestID {
		t.Fatalf("retried records=%+v", received)
	}
}

func TestExternalUsageWaitsForCallbackBeforeReplacement(t *testing.T) {
	entered, release, drained := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	host := newHostWithRecords(capabilityRecord{id: "usage", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{UsagePlugin: usagePluginFunc(func(ctx context.Context, _ pluginapi.UsageRecord) {
		if ctx.Err() != nil {
			t.Error("callback inherited caller cancellation")
		}
		close(entered)
		<-release
	})}}})
	host.usageDrainWaitHook = func(string) { close(drained) }
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	delivered := make(chan error, 1)
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		delivered <- host.HandleUsage(ctx, pluginapi.UsageRecord{RequestID: "blocked"})
	}()
	defer func() { releaseOnce.Do(func() { close(release) }); workers.Wait() }()
	waitForHostTestSignal(t, entered, "external callback start")
	cancel()
	replaced := make(chan bool, 1)
	workers.Add(1)
	go func() { defer workers.Done(); replaced <- host.beginUsageReplacement(t.Context(), "usage") }()
	waitForHostTestSignal(t, drained, "replacement waiting for actual delivery")
	select {
	case errDelivery := <-delivered:
		t.Fatalf("callback acknowledged before completion: %v", errDelivery)
	default:
	}
	select {
	case <-replaced:
		t.Fatal("replacement bypassed the active callback")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	if errDelivery := <-delivered; errDelivery != nil {
		t.Fatal(errDelivery)
	}
	if !<-replaced {
		t.Fatal("replacement did not resume after completion")
	}
	host.finishUsageReplacement("usage")
}

func TestExternalUsageRetriesFusedConsumer(t *testing.T) {
	for _, gated := range []bool{false, true} {
		name := "direct"
		if gated {
			name = "HasUsagePlugins"
		}
		t.Run(name, func(t *testing.T) {
			calls := 0
			host := newHostWithRecords(capabilityRecord{id: "usage", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{UsagePlugin: usagePluginFunc(func(context.Context, pluginapi.UsageRecord) {
				calls++
				panic("plugin failed before processing usage")
			})}}})
			record := pluginapi.UsageRecord{RequestID: "external"}
			if errDelivery := host.HandleUsage(t.Context(), record); !errors.Is(errDelivery, ErrUsageDeliveryUnavailable) {
				t.Fatalf("panic delivery error=%v", errDelivery)
			}
			if !host.isPluginFused("usage") {
				t.Fatal("panicking consumer was not fused")
			}
			for attempt := 0; attempt < 2; attempt++ {
				var errDelivery error
				// Match Home's consumer check before selecting the delivery sink.
				if !gated || host.HasUsagePlugins() {
					errDelivery = host.HandleUsage(t.Context(), record)
				}
				if !errors.Is(errDelivery, ErrUsageDeliveryUnavailable) {
					t.Fatalf("retry %d acknowledged a fused consumer: %v", attempt, errDelivery)
				}
			}
			if !host.HasUsagePlugins() {
				t.Fatal("registered fused consumer disappeared from the delivery contract")
			}
			if calls != 1 {
				t.Fatalf("fused consumer was invoked again: callbacks=%d", calls)
			}
		})
	}
}

func TestExternalUsageWithoutConsumerCompletes(t *testing.T) {
	for name, host := range map[string]*Host{
		"nil":          nil,
		"empty":        newHostWithRecords(),
		"non-consumer": newHostWithRecords(capabilityRecord{id: "other"}),
	} {
		t.Run(name, func(t *testing.T) {
			if host.HasUsagePlugins() {
				t.Fatal("host advertised an absent usage consumer")
			}
			if errDelivery := host.HandleUsage(t.Context(), pluginapi.UsageRecord{RequestID: "external"}); errDelivery != nil {
				t.Fatalf("delivery without consumers failed: %v", errDelivery)
			}
		})
	}
}

func TestExternalUsageReportsRPCFailureAndPanic(t *testing.T) {
	failure := errors.New("usage RPC failure")
	fail := true
	client := &lifecycleTestClient{call: func(context.Context, string, []byte) ([]byte, error) {
		if fail {
			return nil, failure
		}
		return marshalRPCResult(rpcEmptyResponse{})
	}}
	host := newHostWithRecords(capabilityRecord{id: "usage", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{UsagePlugin: &rpcPluginAdapter{id: "usage", client: client}}}})
	if errDelivery := host.HandleUsage(t.Context(), pluginapi.UsageRecord{}); !errors.Is(errDelivery, failure) {
		t.Fatalf("RPC failure=%v", errDelivery)
	}
	state := host.usageStateForPlugin("usage")
	state.mu.Lock()
	pending := len(state.pending)
	state.mu.Unlock()
	if pending != 0 {
		t.Fatal("failed external RPC was silently requeued")
	}
	fail = false
	if errDelivery := host.HandleUsage(t.Context(), pluginapi.UsageRecord{}); errDelivery != nil {
		t.Fatal(errDelivery)
	}
	panicking := newHostWithRecords(capabilityRecord{id: "usage", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{UsagePlugin: usagePluginFunc(func(context.Context, pluginapi.UsageRecord) { panic("plugin failed") })}}})
	if errDelivery := panicking.HandleUsage(t.Context(), pluginapi.UsageRecord{}); !errors.Is(errDelivery, ErrUsageDeliveryUnavailable) || !panicking.isPluginFused("usage") {
		t.Fatalf("panic was not returned as a failed delivery: %v", errDelivery)
	}
}
