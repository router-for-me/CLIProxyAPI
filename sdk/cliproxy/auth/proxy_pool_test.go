package auth

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

type fakeProxyPoolResolver struct {
	authCalls int32
	poolCalls int32
	node      string
	err       error
}

func (r *fakeProxyPoolResolver) ResolveAuthProxy(_ context.Context, auth *Auth) (string, string, error) {
	atomic.AddInt32(&r.authCalls, 1)
	if r.err != nil {
		return "", "", r.err
	}
	if auth == nil {
		return "", "", nil
	}
	node := r.node
	return node, node, nil
}

func (r *fakeProxyPoolResolver) ResolvePoolNode(_ context.Context, _, _, _ string) (string, string, error) {
	atomic.AddInt32(&r.poolCalls, 1)
	if r.err != nil {
		return "", "", r.err
	}
	return r.node, r.node, nil
}

type proxyPoolListStore struct {
	auths []*Auth
}

func (s *proxyPoolListStore) List(context.Context) ([]*Auth, error) {
	out := make([]*Auth, 0, len(s.auths))
	for _, auth := range s.auths {
		out = append(out, auth.Clone())
	}
	return out, nil
}

func (s *proxyPoolListStore) Save(context.Context, *Auth) (string, error) { return "", nil }
func (s *proxyPoolListStore) Delete(context.Context, string) error        { return nil }

func TestRegisterResolvesProxyPoolBinding(t *testing.T) {
	t.Parallel()

	resolver := &fakeProxyPoolResolver{node: "http://pool-node.example.com:8080"}
	manager := NewManager(nil, nil, nil)
	manager.SetProxyPoolResolver(resolver)

	registered, errRegister := manager.Register(context.Background(), &Auth{
		ID:         "account-1",
		Provider:   "claude",
		Attributes: map[string]string{AttributeProxyPoolID: "pool-us"},
		Metadata:   map[string]any{"type": "claude"},
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	if registered.ProxyURL != resolver.node {
		t.Fatalf("ProxyURL = %q, want %q", registered.ProxyURL, resolver.node)
	}
	if got := ProxyPoolNode(registered); got != resolver.node {
		t.Fatalf("ProxyPoolNode() = %q, want %q", got, resolver.node)
	}
	if atomic.LoadInt32(&resolver.authCalls) != 1 {
		t.Fatalf("ResolveAuthProxy calls = %d, want 1", resolver.authCalls)
	}
}

func TestRegisterExplicitProxyURLWinsOverPool(t *testing.T) {
	t.Parallel()

	resolver := &fakeProxyPoolResolver{node: "http://pool-node.example.com:8080"}
	manager := NewManager(nil, nil, nil)
	manager.SetProxyPoolResolver(resolver)

	const explicitProxy = "socks5://explicit.example.com:1080"
	registered, errRegister := manager.Register(context.Background(), &Auth{
		ID:         "account-1",
		Provider:   "claude",
		ProxyURL:   explicitProxy,
		Attributes: map[string]string{AttributeProxyPoolID: "pool-us"},
		Metadata:   map[string]any{"type": "claude"},
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	if registered.ProxyURL != explicitProxy {
		t.Fatalf("ProxyURL = %q, want explicit %q", registered.ProxyURL, explicitProxy)
	}
	if got := ProxyPoolNode(registered); got != "" {
		t.Fatalf("ProxyPoolNode() = %q, want empty", got)
	}
	if atomic.LoadInt32(&resolver.authCalls) != 0 {
		t.Fatalf("ResolveAuthProxy calls = %d, want 0", resolver.authCalls)
	}
}

func TestRegisterResolvesPoolIDFromMetadataFallback(t *testing.T) {
	t.Parallel()

	resolver := &fakeProxyPoolResolver{node: "http://pool-node.example.com:8080"}
	manager := NewManager(nil, nil, nil)
	manager.SetProxyPoolResolver(resolver)

	registered, errRegister := manager.Register(context.Background(), &Auth{
		ID:       "account-1",
		Provider: "claude",
		Metadata: map[string]any{
			"type":               "claude",
			AttributeProxyPoolID: "pool-us",
		},
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	if registered.ProxyURL != resolver.node {
		t.Fatalf("ProxyURL = %q, want %q", registered.ProxyURL, resolver.node)
	}
}

func TestUpdateResolvesProxyPoolWhenProxyCleared(t *testing.T) {
	t.Parallel()

	resolver := &fakeProxyPoolResolver{node: "http://pool-node.example.com:8080"}
	manager := NewManager(nil, nil, nil)
	manager.SetProxyPoolResolver(resolver)

	base, errRegister := manager.Register(context.Background(), &Auth{
		ID:         "account-1",
		Provider:   "claude",
		ProxyURL:   "http://explicit.example.com:8080",
		Attributes: map[string]string{AttributeProxyPoolID: "pool-us"},
		Metadata:   map[string]any{"type": "claude"},
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	incoming := base.Clone()
	incoming.ProxyURL = ""
	updated, errUpdate := manager.Update(context.Background(), incoming)
	if errUpdate != nil {
		t.Fatalf("Update() error = %v", errUpdate)
	}
	if updated.ProxyURL != resolver.node {
		t.Fatalf("ProxyURL = %q, want %q", updated.ProxyURL, resolver.node)
	}
}

func TestLoadResolvesProxyPoolBindings(t *testing.T) {
	t.Parallel()

	resolver := &fakeProxyPoolResolver{node: "http://pool-node.example.com:8080"}
	manager := NewManager(&proxyPoolListStore{auths: []*Auth{{
		ID:         "loaded",
		Provider:   "claude",
		Attributes: map[string]string{AttributeProxyPoolID: "pool-us"},
		Metadata:   map[string]any{"type": "claude"},
	}}}, nil, nil)
	manager.SetProxyPoolResolver(resolver)

	if errLoad := manager.Load(context.Background()); errLoad != nil {
		t.Fatalf("Load() error = %v", errLoad)
	}
	loaded, ok := manager.GetByID("loaded")
	if !ok || loaded == nil {
		t.Fatal("loaded auth not found")
	}
	if loaded.ProxyURL != resolver.node {
		t.Fatalf("ProxyURL = %q, want %q", loaded.ProxyURL, resolver.node)
	}
	if got := ProxyPoolNode(loaded); got != resolver.node {
		t.Fatalf("ProxyPoolNode() = %q, want %q", got, resolver.node)
	}
}

func TestResolverFailureLeavesProxyURLEmpty(t *testing.T) {
	t.Parallel()

	resolver := &fakeProxyPoolResolver{err: errors.New("no healthy nodes")}
	manager := NewManager(nil, nil, nil)
	manager.SetProxyPoolResolver(resolver)

	registered, errRegister := manager.Register(context.Background(), &Auth{
		ID:         "account-1",
		Provider:   "claude",
		Attributes: map[string]string{AttributeProxyPoolID: "pool-us"},
		Metadata:   map[string]any{"type": "claude"},
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	if registered.ProxyURL != "" {
		t.Fatalf("ProxyURL = %q, want empty after resolver failure", registered.ProxyURL)
	}
}

func TestResolveProxyPoolNodeDelegates(t *testing.T) {
	t.Parallel()

	resolver := &fakeProxyPoolResolver{node: "http://pool-node.example.com:8080"}
	manager := NewManager(nil, nil, nil)
	manager.SetProxyPoolResolver(resolver)

	node, errResolve := manager.ResolveProxyPoolNode(context.Background(), "claude", "", "pool-us")
	if errResolve != nil {
		t.Fatalf("ResolveProxyPoolNode() error = %v", errResolve)
	}
	if node != resolver.node {
		t.Fatalf("ResolveProxyPoolNode() = %q, want %q", node, resolver.node)
	}
	if atomic.LoadInt32(&resolver.poolCalls) != 1 {
		t.Fatalf("ResolvePoolNode calls = %d, want 1", resolver.poolCalls)
	}
}

func TestApplyProxyPoolMetadataCopiesAttribute(t *testing.T) {
	t.Parallel()

	auth := &Auth{}
	ApplyProxyPoolMetadata(auth, map[string]any{AttributeProxyPoolID: " pool-us "})
	if got := auth.Attributes[AttributeProxyPoolID]; got != "pool-us" {
		t.Fatalf("AttributeProxyPoolID = %q, want pool-us", got)
	}
}

func TestProxyPoolConstantsCanonicalAlias(t *testing.T) {
	t.Parallel()

	if got := CanonicalCredentialMetadataKey("proxy-pool-id"); got != AttributeProxyPoolID {
		t.Fatalf("CanonicalCredentialMetadataKey(proxy-pool-id) = %q, want %q", got, AttributeProxyPoolID)
	}
}
