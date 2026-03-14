package cliproxy

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/cluster"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

type clusterBindingStore struct {
	saveCount atomic.Int32
}

func (s *clusterBindingStore) List(context.Context) ([]*coreauth.Auth, error) { return nil, nil }

func (s *clusterBindingStore) Save(context.Context, *coreauth.Auth) (string, error) {
	s.saveCount.Add(1)
	return "", nil
}

func (s *clusterBindingStore) Delete(context.Context, string) error { return nil }

func newClusterBindingTestService(t *testing.T) (*Service, *coreauth.Manager, *clusterBindingStore) {
	t.Helper()

	cfg := &internalconfig.Config{
		Cluster: internalconfig.ClusterConfig{
			Enabled: true,
			NodeID:  "node-a",
		},
	}
	store := &clusterBindingStore{}
	manager := coreauth.NewManager(store, nil, nil)
	manager.SetConfig(cfg)

	return &Service{
		cfg:         cfg,
		coreManager: manager,
		cluster:     cluster.NewService(cfg),
	}, manager, store
}

func containsProvider(providers []string, provider string) bool {
	for _, item := range providers {
		if strings.TrimSpace(item) == strings.TrimSpace(provider) {
			return true
		}
	}
	return false
}

func TestServiceReconcileClusterBindingsRegistersRemoteModelsAndAliases(t *testing.T) {
	service, _, store := newClusterBindingTestService(t)

	model := "cluster-service-joined-model"
	localID := "local-auth"
	remoteBinding := cluster.PeerBinding{
		ConfiguredID:            "node-b",
		NodeID:                  "node-b",
		AuthID:                  cluster.RuntimeAuthID("node-b"),
		Provider:                cluster.ProviderKey("node-b"),
		AdvertiseURL:            "http://node-b.example.com",
		Models:                  []string{model},
		RegisterNodePrefixAlias: true,
	}

	registry.GetGlobalRegistry().RegisterClient(localID, "gemini", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(localID)
		registry.GetGlobalRegistry().UnregisterClient(remoteBinding.AuthID)
	})

	service.reconcileClusterBindings(context.Background(), []cluster.PeerBinding{remoteBinding})

	rawProviders := registry.GetGlobalRegistry().GetModelProviders(model)
	if !containsProvider(rawProviders, "gemini") {
		t.Fatalf("raw providers missing local provider: %v", rawProviders)
	}
	if !containsProvider(rawProviders, remoteBinding.Provider) {
		t.Fatalf("raw providers missing remote provider: %v", rawProviders)
	}

	aliasProviders := registry.GetGlobalRegistry().GetModelProviders("node-b/" + model)
	if !containsProvider(aliasProviders, remoteBinding.Provider) {
		t.Fatalf("alias providers = %v, want %q", aliasProviders, remoteBinding.Provider)
	}

	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("runtime-only cluster auth should not persist, saveCount=%d", got)
	}
}

func TestServiceReconcileClusterBindingsRemovesStaleRemoteModels(t *testing.T) {
	service, manager, _ := newClusterBindingTestService(t)

	modelA := "cluster-service-removed-a"
	modelB := "cluster-service-removed-b"
	binding := cluster.PeerBinding{
		ConfiguredID:            "node-b",
		NodeID:                  "node-b",
		AuthID:                  cluster.RuntimeAuthID("node-b"),
		Provider:                cluster.ProviderKey("node-b"),
		AdvertiseURL:            "http://node-b.example.com",
		Models:                  []string{modelA, modelB},
		RegisterNodePrefixAlias: true,
	}

	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(binding.AuthID)
	})

	service.reconcileClusterBindings(context.Background(), []cluster.PeerBinding{binding})

	binding.Models = []string{modelB}
	service.reconcileClusterBindings(context.Background(), []cluster.PeerBinding{binding})

	if containsProvider(registry.GetGlobalRegistry().GetModelProviders(modelA), binding.Provider) {
		t.Fatalf("raw model %q still registered on remote provider", modelA)
	}
	if containsProvider(registry.GetGlobalRegistry().GetModelProviders("node-b/"+modelA), binding.Provider) {
		t.Fatalf("alias model %q still registered on remote provider", "node-b/"+modelA)
	}
	if !containsProvider(registry.GetGlobalRegistry().GetModelProviders(modelB), binding.Provider) {
		t.Fatalf("raw model %q should remain registered", modelB)
	}

	service.reconcileClusterBindings(context.Background(), nil)

	if containsProvider(registry.GetGlobalRegistry().GetModelProviders(modelB), binding.Provider) {
		t.Fatalf("remote provider should be removed from %q after peer deletion", modelB)
	}
	if _, ok := manager.Executor(binding.Provider); ok {
		t.Fatalf("executor for %q should be unregistered after peer deletion", binding.Provider)
	}
	if auth, ok := manager.GetByID(binding.AuthID); ok || auth != nil {
		t.Fatalf("runtime auth should be deleted after peer deletion: ok=%v auth=%+v", ok, auth)
	}
}

func TestServiceReconcileClusterBindingsSkipsNodePrefixAliasWhenDisabled(t *testing.T) {
	service, _, _ := newClusterBindingTestService(t)

	model := "cluster-service-no-alias"
	binding := cluster.PeerBinding{
		ConfiguredID: "node-c",
		NodeID:       "node-c",
		AuthID:       cluster.RuntimeAuthID("node-c"),
		Provider:     cluster.ProviderKey("node-c"),
		AdvertiseURL: "http://node-c.example.com",
		Models:       []string{model},
	}

	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(binding.AuthID)
	})

	service.reconcileClusterBindings(context.Background(), []cluster.PeerBinding{binding})

	if providers := registry.GetGlobalRegistry().GetModelProviders("node-c/" + model); len(providers) != 0 {
		t.Fatalf("node-prefixed alias should not exist when disabled: %v", providers)
	}
}

func TestServiceReconcileClusterBindingsRemovesAliasWhenToggleTurnsOff(t *testing.T) {
	service, _, _ := newClusterBindingTestService(t)

	model := "cluster-service-alias-toggle"
	binding := cluster.PeerBinding{
		ConfiguredID:            "node-d",
		NodeID:                  "node-d",
		AuthID:                  cluster.RuntimeAuthID("node-d"),
		Provider:                cluster.ProviderKey("node-d"),
		AdvertiseURL:            "http://node-d.example.com",
		Models:                  []string{model},
		RegisterNodePrefixAlias: true,
	}

	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(binding.AuthID)
	})

	service.reconcileClusterBindings(context.Background(), []cluster.PeerBinding{binding})
	if !containsProvider(registry.GetGlobalRegistry().GetModelProviders("node-d/"+model), binding.Provider) {
		t.Fatalf("alias should exist while enabled")
	}

	binding.RegisterNodePrefixAlias = false
	service.reconcileClusterBindings(context.Background(), []cluster.PeerBinding{binding})

	if providers := registry.GetGlobalRegistry().GetModelProviders("node-d/" + model); len(providers) != 0 {
		t.Fatalf("alias should be removed after toggle off: %v", providers)
	}
	if !containsProvider(registry.GetGlobalRegistry().GetModelProviders(model), binding.Provider) {
		t.Fatalf("raw model should remain registered after alias toggle off")
	}
}

func TestServiceReconcileClusterBindingsRegistersMultipleRemoteProvidersForSameModel(t *testing.T) {
	service, _, _ := newClusterBindingTestService(t)

	model := "cluster-service-shared-model"
	bindingB := cluster.PeerBinding{
		ConfiguredID: "node-b",
		NodeID:       "node-b",
		AuthID:       cluster.RuntimeAuthID("node-b"),
		Provider:     cluster.ProviderKey("node-b"),
		AdvertiseURL: "http://node-b.example.com",
		Models:       []string{model},
	}
	bindingC := cluster.PeerBinding{
		ConfiguredID: "node-c",
		NodeID:       "node-c",
		AuthID:       cluster.RuntimeAuthID("node-c"),
		Provider:     cluster.ProviderKey("node-c"),
		AdvertiseURL: "http://node-c.example.com",
		Models:       []string{model},
	}

	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(bindingB.AuthID)
		registry.GetGlobalRegistry().UnregisterClient(bindingC.AuthID)
	})

	service.reconcileClusterBindings(context.Background(), []cluster.PeerBinding{bindingB, bindingC})

	providers := registry.GetGlobalRegistry().GetModelProviders(model)
	if !containsProvider(providers, bindingB.Provider) || !containsProvider(providers, bindingC.Provider) {
		t.Fatalf("shared model providers = %v", providers)
	}
}
