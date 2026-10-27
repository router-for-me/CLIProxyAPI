package cliproxy

import (
	"context"
	"sync/atomic"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type proxyPoolSaveCounter struct {
	saves int32
}

func (s *proxyPoolSaveCounter) List(context.Context) ([]*coreauth.Auth, error) { return nil, nil }
func (s *proxyPoolSaveCounter) Save(context.Context, *coreauth.Auth) (string, error) {
	atomic.AddInt32(&s.saves, 1)
	return "", nil
}
func (s *proxyPoolSaveCounter) Delete(context.Context, string) error { return nil }

const (
	servicePoolNodeA = "http://proxy-a.example.com:8080"
	servicePoolNodeB = "http://proxy-b.example.com:8080"
	servicePoolNodeC = "socks5://proxy-c.example.com:1080"
)

func newProxyPoolTestService(t *testing.T, cfg *config.Config) *Service {
	t.Helper()
	service := &Service{coreManager: coreauth.NewManager(nil, nil, nil)}
	service.applyProxyPools(cfg)
	t.Cleanup(service.closeProxyPools)
	return service
}

func TestServiceProxyPoolAppliesOnRegistration(t *testing.T) {
	t.Parallel()

	service := newProxyPoolTestService(t, &config.Config{ProxyPools: []config.ProxyPoolConfig{{
		ID:    "pool-us",
		Nodes: []string{servicePoolNodeA, servicePoolNodeB},
	}}})
	registered, errRegister := service.coreManager.Register(context.Background(), &coreauth.Auth{
		ID:         "account-1",
		Provider:   "claude",
		Attributes: map[string]string{coreauth.AttributeProxyPoolID: "pool-us"},
		Metadata:   map[string]any{"type": "claude"},
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	node := coreauth.ProxyPoolNode(registered)
	if node == "" {
		t.Fatal("sticky proxy pool node was not written")
	}
	if registered.ProxyURL != node {
		t.Fatalf("ProxyURL = %q, want node %q", registered.ProxyURL, node)
	}
}

func TestServiceProxyPoolExplicitProxyWins(t *testing.T) {
	t.Parallel()

	service := newProxyPoolTestService(t, &config.Config{ProxyPools: []config.ProxyPoolConfig{{
		ID:    "pool-us",
		Nodes: []string{servicePoolNodeA},
	}}})
	const explicitProxy = "socks5://explicit.example.com:1080"
	registered, errRegister := service.coreManager.Register(context.Background(), &coreauth.Auth{
		ID:         "account-1",
		Provider:   "claude",
		ProxyURL:   explicitProxy,
		Attributes: map[string]string{coreauth.AttributeProxyPoolID: "pool-us"},
		Metadata:   map[string]any{"type": "claude"},
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	if registered.ProxyURL != explicitProxy {
		t.Fatalf("ProxyURL = %q, want explicit %q", registered.ProxyURL, explicitProxy)
	}
	if got := coreauth.ProxyPoolNode(registered); got != "" {
		t.Fatalf("ProxyPoolNode() = %q, want empty", got)
	}
}

func TestServiceRoundRobinAssignsWithoutWritingStickyMetadata(t *testing.T) {
	t.Parallel()

	service := newProxyPoolTestService(t, &config.Config{ProxyPools: []config.ProxyPoolConfig{{
		ID:       "pool-us",
		Strategy: "round-robin",
		Nodes:    []string{servicePoolNodeA, servicePoolNodeB, servicePoolNodeC},
	}}})
	assigned := make(map[string]struct{}, 3)
	for _, id := range []string{"account-1", "account-2", "account-3"} {
		registered, errRegister := service.coreManager.Register(context.Background(), &coreauth.Auth{
			ID:         id,
			Provider:   "claude",
			Attributes: map[string]string{coreauth.AttributeProxyPoolID: "pool-us"},
			Metadata:   map[string]any{"type": "claude"},
		})
		if errRegister != nil {
			t.Fatalf("Register(%s) error = %v", id, errRegister)
		}
		if registered.ProxyURL == "" {
			t.Fatalf("Register(%s) left ProxyURL empty", id)
		}
		if got := coreauth.ProxyPoolNode(registered); got != registered.ProxyURL {
			t.Fatalf("Register(%s) marker = %q, want assigned node %q", id, got, registered.ProxyURL)
		}
		assigned[registered.ProxyURL] = struct{}{}
	}
	if len(assigned) != 3 {
		t.Fatalf("round-robin assigned %d distinct nodes, want 3", len(assigned))
	}
}

func TestServiceRoundRobinPersistsOnlyFirstAssignment(t *testing.T) {
	t.Parallel()

	store := &proxyPoolSaveCounter{}
	service := &Service{coreManager: coreauth.NewManager(store, nil, nil)}
	service.applyProxyPools(&config.Config{ProxyPools: []config.ProxyPoolConfig{{
		ID:       "pool-us",
		Strategy: "round-robin",
		Nodes:    []string{servicePoolNodeA, servicePoolNodeB},
	}}})
	t.Cleanup(service.closeProxyPools)

	registered, errRegister := service.coreManager.Register(context.Background(), &coreauth.Auth{
		ID:         "account-1",
		Provider:   "claude",
		Attributes: map[string]string{coreauth.AttributeProxyPoolID: "pool-us"},
		Metadata:   map[string]any{"type": "claude"},
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	savesAfterRegister := atomic.LoadInt32(&store.saves)
	if savesAfterRegister == 0 {
		t.Fatal("first round-robin assignment was not persisted")
	}

	rotated := registered.Clone()
	rotated.Metadata[coreauth.MetadataProxyPoolNode] = "http://rotated.example.com:8080"
	service.persistProxyPoolBinding(context.Background(), rotated, registered.ProxyURL)
	if got := atomic.LoadInt32(&store.saves); got != savesAfterRegister {
		t.Fatalf("round-robin rotation persisted again: saves %d -> %d", savesAfterRegister, got)
	}

	service.persistProxyPoolBinding(context.Background(), rotated, "")
	if got := atomic.LoadInt32(&store.saves); got != savesAfterRegister+1 {
		t.Fatalf("first round-robin assignment save count = %d, want %d", got, savesAfterRegister+1)
	}
}

func TestServiceStickyPersistsChangedBinding(t *testing.T) {
	t.Parallel()

	store := &proxyPoolSaveCounter{}
	service := &Service{coreManager: coreauth.NewManager(store, nil, nil)}
	service.applyProxyPools(&config.Config{ProxyPools: []config.ProxyPoolConfig{{
		ID:    "pool-us",
		Nodes: []string{servicePoolNodeA, servicePoolNodeB},
	}}})
	t.Cleanup(service.closeProxyPools)

	registered, errRegister := service.coreManager.Register(context.Background(), &coreauth.Auth{
		ID:         "account-1",
		Provider:   "claude",
		Attributes: map[string]string{coreauth.AttributeProxyPoolID: "pool-us"},
		Metadata:   map[string]any{"type": "claude"},
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	savesAfterRegister := atomic.LoadInt32(&store.saves)

	reassignedNode := servicePoolNodeA
	if registered.ProxyURL == servicePoolNodeA {
		reassignedNode = servicePoolNodeB
	}
	reassigned := registered.Clone()
	reassigned.Metadata[coreauth.MetadataProxyPoolNode] = reassignedNode
	service.persistProxyPoolBinding(context.Background(), reassigned, registered.ProxyURL)
	if got := atomic.LoadInt32(&store.saves); got != savesAfterRegister+1 {
		t.Fatalf("sticky reassignment save count = %d, want %d", got, savesAfterRegister+1)
	}
}

func TestServiceProxyPoolHotReloadKeepsBinding(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{ProxyPools: []config.ProxyPoolConfig{{
		ID:    "pool-us",
		Nodes: []string{servicePoolNodeA, servicePoolNodeB},
	}}}
	service := newProxyPoolTestService(t, cfg)
	registered, errRegister := service.coreManager.Register(context.Background(), &coreauth.Auth{
		ID:         "account-1",
		Provider:   "claude",
		Attributes: map[string]string{coreauth.AttributeProxyPoolID: "pool-us"},
		Metadata:   map[string]any{"type": "claude"},
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	node := coreauth.ProxyPoolNode(registered)

	service.proxyPoolMu.Lock()
	before := service.proxyPoolManager
	service.proxyPoolMu.Unlock()
	service.applyProxyPools(cfg)
	service.proxyPoolMu.Lock()
	after := service.proxyPoolManager
	service.proxyPoolMu.Unlock()
	if before != after {
		t.Fatal("proxy pool manager was rebuilt for an unchanged configuration")
	}

	// Simulate a backing auth file reload that no longer carries proxy_url.
	incoming := registered.Clone()
	incoming.ProxyURL = ""
	updated, errUpdate := service.coreManager.Update(context.Background(), incoming)
	if errUpdate != nil {
		t.Fatalf("Update() error = %v", errUpdate)
	}
	if updated.ProxyURL != node {
		t.Fatalf("ProxyURL after reload = %q, want stable binding %q", updated.ProxyURL, node)
	}
	if got := coreauth.ProxyPoolNode(updated); got != node {
		t.Fatalf("ProxyPoolNode() after reload = %q, want %q", got, node)
	}
}

func TestServiceProxyPoolConfigChangeRebindsAccounts(t *testing.T) {
	t.Parallel()

	service := newProxyPoolTestService(t, &config.Config{ProxyPools: []config.ProxyPoolConfig{{
		ID:    "pool-us",
		Nodes: []string{servicePoolNodeA},
	}}})
	registered, errRegister := service.coreManager.Register(context.Background(), &coreauth.Auth{
		ID:         "account-1",
		Provider:   "claude",
		Attributes: map[string]string{coreauth.AttributeProxyPoolID: "pool-us"},
		Metadata:   map[string]any{"type": "claude"},
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	if registered.ProxyURL != servicePoolNodeA {
		t.Fatalf("ProxyURL = %q, want %q", registered.ProxyURL, servicePoolNodeA)
	}

	service.applyProxyPools(&config.Config{ProxyPools: []config.ProxyPoolConfig{{
		ID:    "pool-us",
		Nodes: []string{servicePoolNodeB},
	}}})
	current, ok := service.coreManager.GetByID("account-1")
	if !ok || current == nil {
		t.Fatal("auth disappeared after pool config change")
	}
	if current.ProxyURL != servicePoolNodeB {
		t.Fatalf("ProxyURL after pool change = %q, want %q", current.ProxyURL, servicePoolNodeB)
	}
	if got := coreauth.ProxyPoolNode(current); got != servicePoolNodeB {
		t.Fatalf("ProxyPoolNode() after pool change = %q, want %q", got, servicePoolNodeB)
	}
}

func TestServiceProxyPoolConfigChangeKeepsExplicitProxy(t *testing.T) {
	t.Parallel()

	service := newProxyPoolTestService(t, &config.Config{ProxyPools: []config.ProxyPoolConfig{{
		ID:    "pool-us",
		Nodes: []string{servicePoolNodeA},
	}}})
	const explicitProxy = "http://explicit.example.com:8080"
	if _, errRegister := service.coreManager.Register(context.Background(), &coreauth.Auth{
		ID:         "account-1",
		Provider:   "claude",
		ProxyURL:   explicitProxy,
		Attributes: map[string]string{coreauth.AttributeProxyPoolID: "pool-us"},
		Metadata:   map[string]any{"type": "claude"},
	}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	service.applyProxyPools(&config.Config{ProxyPools: []config.ProxyPoolConfig{{
		ID:    "pool-us",
		Nodes: []string{servicePoolNodeB},
	}}})
	current, ok := service.coreManager.GetByID("account-1")
	if !ok || current == nil {
		t.Fatal("auth disappeared after pool config change")
	}
	if current.ProxyURL != explicitProxy {
		t.Fatalf("ProxyURL = %q, want explicit %q", current.ProxyURL, explicitProxy)
	}
}

func TestServiceResolvePoolNodeForOAuthSession(t *testing.T) {
	t.Parallel()

	service := newProxyPoolTestService(t, &config.Config{ProxyPools: []config.ProxyPoolConfig{{
		ID:    "pool-us",
		Nodes: []string{servicePoolNodeA, servicePoolNodeB, servicePoolNodeC},
	}}})
	node, errResolve := service.coreManager.ResolveProxyPoolNode(context.Background(), "claude", "", "pool-us")
	if errResolve != nil {
		t.Fatalf("ResolveProxyPoolNode() error = %v", errResolve)
	}
	if node == "" {
		t.Fatal("ResolveProxyPoolNode() returned an empty node")
	}
	if _, errMissing := service.coreManager.ResolveProxyPoolNode(context.Background(), "claude", "", "missing"); errMissing == nil {
		t.Fatal("ResolveProxyPoolNode() = nil error for an unknown pool")
	}
}
