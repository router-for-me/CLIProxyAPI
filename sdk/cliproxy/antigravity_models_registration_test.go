package cliproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestAntigravityDynamicRegistrationLifecycle(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	const allowed = "gemini-3.1-flash-lite"
	var response atomic.Value
	response.Store(map[string]any{"models": map[string]any{allowed: map[string]any{"maxTokens": 1}, "not-in-static": map[string]any{}}, "webSearchModelIds": []string{allowed}})
	var status atomic.Int32
	status.Store(200)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(int(status.Load()))
		_ = json.NewEncoder(w).Encode(response.Load())
	}))
	defer server.Close()
	cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{"antigravity": {{Name: allowed, Alias: "my-model"}}}}
	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{cfg: cfg, coreManager: manager}
	auth := antigravityTestAuth("dynamic-account", server.URL)
	auth.Prefix = "tenant"
	var err error
	auth, err = manager.Register(t.Context(), auth)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
	svc.registerModelsForAuth(t.Context(), auth)
	svc.WaitAntigravityProbes()
	assertModels := func(want bool) {
		t.Helper()
		models := GlobalModelRegistry().GetModelsForClient(auth.ID)
		if !want {
			if len(models) != 0 {
				t.Fatalf("expected empty catalog: %v", models)
			}
			return
		}
		if len(models) != 2 {
			t.Fatalf("expected alias and prefixed alias, got %v", models)
		}
		for _, m := range models {
			if m.ID != "my-model" && m.ID != "tenant/my-model" {
				t.Fatalf("unentitled model %s", m.ID)
			}
			if m.ContextLength <= 1 || !m.SupportsWebSearch {
				t.Fatalf("lost static info or fetched capability: %+v", m)
			}
		}
	}
	assertModels(true)
	svc.refreshAntigravityModels(t.Context())
	svc.WaitAntigravityProbes()
	if calls.Load() != 1 {
		t.Fatal("fresh cache probed")
	}
	expire := func() {
		key := svc.antigravityCapabilityKey(auth)
		antigravityCapabilityMu.Lock()
		entry := antigravityCapabilityCache[key]
		entry.expiresAt = time.Time{}
		antigravityCapabilityCache[key] = entry
		antigravityAuthFailureCache = make(map[string]antigravityFailureState)
		antigravityCapabilityMu.Unlock()
	}
	// Errors preserve the last successful account catalog.
	expire()
	status.Store(403)
	svc.refreshAntigravityModels(t.Context())
	svc.WaitAntigravityProbes()
	assertModels(true)
	// A successful empty catalog revokes all registrations, without disabling the auth.
	expire()
	status.Store(200)
	response.Store(map[string]any{"models": map[string]any{}})
	svc.refreshAntigravityModels(t.Context())
	svc.WaitAntigravityProbes()
	assertModels(false)
	current, _ := manager.GetByID(auth.ID)
	if current.Disabled {
		t.Fatal("empty catalog disabled credential")
	}
	// Registration from cache must not temporarily restore static-only models.
	svc.registerModelsForAuth(t.Context(), current)
	svc.WaitAntigravityProbes()
	assertModels(false)
	expire()
	response.Store(map[string]any{"models": map[string]any{allowed: map[string]any{}}, "webSearchModelIds": []string{allowed}})
	svc.refreshAntigravityModels(t.Context())
	svc.WaitAntigravityProbes()
	assertModels(true)
	if len(registry.GetAntigravityModels()) < 3 {
		t.Fatal("static catalog mutated")
	}
}

func TestAntigravityDynamicRegistrationExclusions(t *testing.T) {
	svc := &Service{cfg: &config.Config{OAuthExcludedModels: map[string][]string{"antigravity": {"gemini-3.1-flash-lite"}}}}
	auth := antigravityTestAuth("excluded", "http://127.0.0.1:1")
	hints := antigravityModelCapabilityHints{ModelIDs: map[string]struct{}{"gemini-3.1-flash-lite": {}}}
	if models := svc.antigravityModelsForHints(auth, hints); len(models) != 0 {
		t.Fatalf("excluded models restored: %v", models)
	}
}

func TestAntigravityDynamicRegistrationBatches(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	const workers = modelRegistrationMaxWorkersPerCategory
	var active, peak, calls atomic.Int32
	started := make(chan struct{}, workers*2+3)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		calls.Add(1)
		for previous := peak.Load(); current > previous; previous = peak.Load() {
			if peak.CompareAndSwap(previous, current) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(`{"models":{"gemini-3.1-flash-lite":{}}}`))
	}))
	defer server.Close()
	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{cfg: &config.Config{}, coreManager: manager}
	for i := range workers*2 + 3 {
		auth := antigravityTestAuth(fmt.Sprint("batch-", i), server.URL)
		if _, err := manager.Register(t.Context(), auth); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
	}
	done := make(chan struct{})
	go func() { defer close(done); svc.refreshAntigravityModels(t.Context()) }()
	for range workers {
		<-started
	}
	if peak.Load() > workers {
		t.Fatal("unbounded probes")
	}
	unblock()
	<-done
	svc.WaitAntigravityProbes()
	if calls.Load() != workers*2+3 || peak.Load() > workers {
		t.Fatalf("calls=%d peak=%d", calls.Load(), peak.Load())
	}
	for _, auth := range manager.List() {
		if len(GlobalModelRegistry().GetModelsForClient(auth.ID)) != 1 {
			t.Fatal("batch registration incomplete")
		}
	}
}

func TestAntigravityDynamicRegistrationDropsStaleFullCatalog(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte(`{"models":{"gemini-3.1-flash-lite":{}}}`))
	}))
	defer server.Close()
	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{cfg: &config.Config{}, coreManager: manager}
	auth := antigravityTestAuth("stale-full", server.URL)
	auth, _ = manager.Register(t.Context(), auth)
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
	svc.registerModelsForAuth(t.Context(), auth)
	<-started
	GlobalModelRegistry().UnregisterClient(auth.ID)
	manager.Remove(context.Background(), auth.ID)
	unblock()
	svc.WaitAntigravityProbes()
	if len(GlobalModelRegistry().GetModelsForClient(auth.ID)) != 0 {
		t.Fatal("removed credential resurrected")
	}
}
