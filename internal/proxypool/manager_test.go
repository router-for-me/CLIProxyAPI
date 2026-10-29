package proxypool

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

const (
	nodeA = "http://proxy-a.example.com:8080"
	nodeB = "http://proxy-b.example.com:8080"
	nodeC = "socks5://proxy-c.example.com:1080"
)

type fakeTicker struct {
	ch chan time.Time
}

func (t *fakeTicker) Chan() <-chan time.Time { return t.ch }
func (t *fakeTicker) Stop()                  {}

func newFakeTicker() *fakeTicker {
	return &fakeTicker{ch: make(chan time.Time, 4)}
}

type stubProbe struct {
	mu  sync.Mutex
	err map[string]error
}

func newStubProbe() *stubProbe {
	return &stubProbe{err: make(map[string]error)}
}

func (p *stubProbe) fn(_ context.Context, nodeURL, _ string, _ time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err[nodeURL]
}

func (p *stubProbe) set(nodeURL string, err error) {
	p.mu.Lock()
	p.err[nodeURL] = err
	p.mu.Unlock()
}

func newTestManager(t *testing.T, strategy Strategy, nodes []string, opts ...Option) *Manager {
	t.Helper()
	manager, errNew := NewManager([]Config{{
		ID:       "pool",
		Strategy: strategy,
		Nodes:    nodes,
		HealthCheck: HealthCheck{
			Enabled:  true,
			TestURL:  "https://example.com",
			Interval: time.Minute,
			Timeout:  time.Second,
		},
	}}, opts...)
	if errNew != nil {
		t.Fatalf("NewManager() error = %v", errNew)
	}
	t.Cleanup(manager.Close)
	return manager
}

func TestStickyResolutionIsStable(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, StrategySticky, []string{nodeA, nodeB, nodeC})
	first, errResolve := manager.Resolve("claude", "account-1", "pool")
	if errResolve != nil {
		t.Fatalf("Resolve() error = %v", errResolve)
	}
	for i := 0; i < 10; i++ {
		next, errNext := manager.Resolve("claude", "account-1", "pool")
		if errNext != nil {
			t.Fatalf("Resolve() error = %v", errNext)
		}
		if next != first {
			t.Fatalf("Resolve() = %q, want stable %q", next, first)
		}
	}
}

func TestStickyReassignsFromUnhealthyBindingAndDoesNotFlapBack(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, StrategySticky, []string{nodeA, nodeB, nodeC}, WithFailureThreshold(1))
	first, errResolve := manager.Resolve("claude", "account-1", "pool")
	if errResolve != nil {
		t.Fatalf("Resolve() error = %v", errResolve)
	}

	manager.ReportDialResult("pool", first, errors.New("dial failed"))
	reassigned, errReassigned := manager.ResolveNode("claude", "account-1", "pool", first)
	if errReassigned != nil {
		t.Fatalf("ResolveNode() error = %v", errReassigned)
	}
	if reassigned == first {
		t.Fatalf("ResolveNode() reused unhealthy node %q", first)
	}

	// The failed node recovers, but the account stays on its new binding.
	manager.ReportDialResult("pool", first, nil)
	stable, errStable := manager.ResolveNode("claude", "account-1", "pool", reassigned)
	if errStable != nil {
		t.Fatalf("ResolveNode() error = %v", errStable)
	}
	if stable != reassigned {
		t.Fatalf("ResolveNode() = %q, want reassigned binding %q", stable, reassigned)
	}
}

func TestStickyRejectsUnhealthyBindingWhenAllNodesDown(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, StrategySticky, []string{nodeA}, WithFailureThreshold(1))
	manager.ReportDialResult("pool", nodeA, errors.New("dial failed"))
	_, errResolve := manager.Resolve("claude", "account-1", "pool")
	if errResolve == nil {
		t.Fatal("Resolve() = nil error, want no healthy nodes")
	}
	if !strings.Contains(errResolve.Error(), "no healthy nodes") {
		t.Fatalf("Resolve() error = %v, want no healthy nodes", errResolve)
	}
}

func TestRoundRobinDistributesAcrossHealthyNodes(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, StrategyRoundRobin, []string{nodeA, nodeB, nodeC})
	counts := make(map[string]int, 3)
	for i := 0; i < 6; i++ {
		node, errResolve := manager.Resolve("claude", "account-1", "pool")
		if errResolve != nil {
			t.Fatalf("Resolve() error = %v", errResolve)
		}
		counts[node]++
	}
	for _, node := range []string{nodeA, nodeB, nodeC} {
		if counts[node] != 2 {
			t.Fatalf("round-robin distribution = %v, want each node twice", counts)
		}
	}
}

func TestRoundRobinSkipsUnhealthyNodes(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, StrategyRoundRobin, []string{nodeA, nodeB, nodeC}, WithFailureThreshold(1))
	manager.ReportDialResult("pool", nodeB, errors.New("dial failed"))
	counts := make(map[string]int, 2)
	for i := 0; i < 4; i++ {
		node, errResolve := manager.Resolve("claude", "account-1", "pool")
		if errResolve != nil {
			t.Fatalf("Resolve() error = %v", errResolve)
		}
		counts[node]++
	}
	if counts[nodeB] != 0 {
		t.Fatalf("unhealthy node %q was assigned: %v", nodeB, counts)
	}
	if counts[nodeA] != 2 || counts[nodeC] != 2 {
		t.Fatalf("healthy distribution = %v, want two each", counts)
	}
}

func TestProbeFailureThresholdAndRecovery(t *testing.T) {
	t.Parallel()

	probe := newStubProbe()
	manager := newTestManager(t, StrategySticky, []string{nodeA}, WithProbeFunc(probe.fn), WithFailureThreshold(2))
	ctx := context.Background()

	probe.set(nodeA, errors.New("probe failed"))
	manager.ProbePool(ctx, "pool")
	status := mustStatus(t, manager, "pool")
	if !status[0].Healthy {
		t.Fatal("node removed after one failure, want debounce threshold of two")
	}

	manager.ProbePool(ctx, "pool")
	status = mustStatus(t, manager, "pool")
	if status[0].Healthy {
		t.Fatal("node still healthy after two consecutive failures")
	}

	probe.set(nodeA, nil)
	manager.ProbePool(ctx, "pool")
	status = mustStatus(t, manager, "pool")
	if !status[0].Healthy {
		t.Fatal("node did not recover after a successful probe")
	}
}

func TestProbeLoopDrivenByInjectedTicker(t *testing.T) {
	t.Parallel()

	ticker := newFakeTicker()
	probed := make(chan string, 4)
	manager, errNew := NewManager([]Config{{
		ID:       "pool",
		Strategy: StrategySticky,
		Nodes:    []string{nodeA},
		HealthCheck: HealthCheck{
			Enabled:  true,
			TestURL:  "https://example.com",
			Interval: time.Minute,
			Timeout:  time.Second,
		},
	}},
		WithIntervalFunc(func(time.Duration) Ticker { return ticker }),
		WithProbeFunc(func(context.Context, string, string, time.Duration) error {
			probed <- nodeA
			return errors.New("probe failed")
		}),
		WithFailureThreshold(1),
	)
	if errNew != nil {
		t.Fatalf("NewManager() error = %v", errNew)
	}
	manager.Start(context.Background())
	t.Cleanup(manager.Close)

	ticker.ch <- time.Now()
	select {
	case <-probed:
	case <-time.After(5 * time.Second):
		t.Fatal("probe loop did not run")
	}
	status := mustStatus(t, manager, "pool")
	if status[0].Healthy {
		t.Fatal("node remained healthy after an injected failing probe")
	}
}

func TestResolveUnknownPool(t *testing.T) {
	t.Parallel()

	manager, errNew := NewManager([]Config{{ID: "pool", Strategy: StrategySticky, Nodes: []string{nodeA}}})
	if errNew != nil {
		t.Fatalf("NewManager() error = %v", errNew)
	}
	t.Cleanup(manager.Close)
	if _, errResolve := manager.Resolve("claude", "account", "missing"); errResolve == nil {
		t.Fatal("Resolve() = nil error for unknown pool")
	}
}

func TestNewManagerRejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		configs []Config
	}{
		{name: "empty id", configs: []Config{{Nodes: []string{nodeA}}}},
		{name: "duplicate id", configs: []Config{{ID: "pool", Nodes: []string{nodeA}}, {ID: "pool", Nodes: []string{nodeB}}}},
		{name: "empty nodes", configs: []Config{{ID: "pool"}}},
		{name: "invalid strategy", configs: []Config{{ID: "pool", Strategy: "random", Nodes: []string{nodeA}}}},
		{name: "invalid node", configs: []Config{{ID: "pool", Nodes: []string{"ftp://proxy.example.com:21"}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, errNew := NewManager(tc.configs); errNew == nil {
				t.Fatal("NewManager() = nil error, want rejection")
			}
		})
	}
}

func TestConfigsFromConfigDefaults(t *testing.T) {
	t.Parallel()

	configs, errConvert := ConfigsFromConfig([]internalconfig.ProxyPoolConfig{{
		ID:    "pool-us",
		Nodes: []string{nodeA},
	}})
	if errConvert != nil {
		t.Fatalf("ConfigsFromConfig() error = %v", errConvert)
	}
	if len(configs) != 1 {
		t.Fatalf("len(configs) = %d, want 1", len(configs))
	}
	cfg := configs[0]
	if cfg.Strategy != StrategySticky {
		t.Fatalf("Strategy = %q, want sticky", cfg.Strategy)
	}
	if cfg.HealthCheck.Interval != DefaultHealthCheckInterval || cfg.HealthCheck.Timeout != DefaultHealthCheckTimeout {
		t.Fatalf("HealthCheck = %+v, want default interval/timeout", cfg.HealthCheck)
	}
	if cfg.HealthCheck.TestURL != DefaultHealthCheckTestURL {
		t.Fatalf("TestURL = %q, want default", cfg.HealthCheck.TestURL)
	}
}

func TestManagerConcurrentResolve(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, StrategyRoundRobin, []string{nodeA, nodeB, nodeC})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, errResolve := manager.Resolve("claude", "account", "pool"); errResolve != nil {
				t.Errorf("Resolve() error = %v", errResolve)
			}
		}()
	}
	wg.Wait()
}

func mustStatus(t *testing.T, manager *Manager, poolID string) []NodeStatus {
	t.Helper()
	status, errStatus := manager.Status(poolID)
	if errStatus != nil {
		t.Fatalf("Status() error = %v", errStatus)
	}
	return status
}
