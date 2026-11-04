package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// Manager.Remove closes executor sessions after removing the runtime auth. Use
// that boundary to verify the registry cannot expose a tombstone epoch first.
type antigravityRemovalExecutor struct {
	coreauth.ProviderExecutor
	onClose func()
}

func (e *antigravityRemovalExecutor) CloseExecutionSession(string) { e.onClose() }

func TestAntigravityRemovalInvalidatesAuthBeforeRegistry(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	svc := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	auth, errRegister := svc.coreManager.Register(t.Context(), antigravityTestAuth("removal-publication", "http://127.0.0.1:1"))
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	reg := registry.GetGlobalRegistry()
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	key := svc.antigravityCapabilityKey(auth)
	hints := antigravityModelCapabilityHints{ModelIDs: map[string]struct{}{"gemini-3.1-flash-lite": {}}, revision: 1}
	antigravityCapabilityMu.Lock()
	antigravityCapabilityCache[key] = antigravityCapabilityCacheEntry{hints: hints, expiresAt: time.Now().Add(antigravityCapabilityCacheTTL)}
	antigravityCapabilityMu.Unlock()
	svc.applyAntigravityModelHints(t.Context(), auth, "antigravity", hints, key, auth.RegistrationEpoch, reg.ClientRegistrationEpoch(auth.ID))
	initialEpoch := reg.ClientRegistrationEpoch(auth.ID)
	if !reg.ClientSupportsModel(auth.ID, "gemini-3.1-flash-lite") {
		t.Fatal("initial catalog not registered")
	}
	closed := false
	svc.coreManager.RegisterExecutor(&antigravityRemovalExecutor{
		ProviderExecutor: &mockAntigravityRefreshExecutor{},
		onClose: func() {
			closed = true
			if _, exists := svc.coreManager.GetByID(auth.ID); exists {
				t.Error("runtime auth still exists during removal cleanup")
			}
			if reg.ClientRegistrationEpoch(auth.ID) != initialEpoch {
				t.Error("registry epoch advanced before runtime auth invalidation")
			}
			// Neither a new scan nor work queued with an old auth snapshot may
			// publish from cache while the deletion is between its two stores.
			svc.refreshAntigravityModels(t.Context())
			svc.scheduleAntigravityModelRefresh(t.Context(), auth)
			svc.WaitAntigravityProbes()
		},
	})
	svc.applyCoreAuthRemoval(t.Context(), auth.ID)
	if !closed {
		t.Fatal("removal cleanup boundary was not exercised")
	}
	if _, exists := svc.coreManager.GetByID(auth.ID); exists {
		t.Fatal("removed auth retained in manager")
	}
	if len(reg.GetModelsForClient(auth.ID)) != 0 || reg.ClientRegistrationEpoch(auth.ID) != initialEpoch+1 {
		t.Fatal("removal did not leave a single final registry tombstone")
	}
	// A publisher that already validated its auth must still lose registry CAS.
	if _, applied := reg.ReplaceClientModels(auth.ID, "antigravity", initialEpoch, []*ModelInfo{{ID: "gemini-3.1-flash-lite"}}); applied {
		t.Fatal("pre-removal publisher overwrote the tombstone")
	}
}

func TestAntigravityCachedRegistrationPreservesCooldownWhileProbePending(t *testing.T) {
	for _, registered := range []bool{false, true} {
		name := "first-publication"
		if registered {
			name = "reregistration"
		}
		t.Run(name, func(t *testing.T) {
			resetAntigravityCapabilityCache()
			t.Cleanup(resetAntigravityCapabilityCache)
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(started)
				select {
				case <-release:
					_, _ = w.Write([]byte(`{"models":{}}`))
				case <-r.Context().Done():
				}
			}))
			ctx, cancel := context.WithCancel(t.Context())
			svc := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
			t.Cleanup(func() {
				unblock()
				svc.WaitAntigravityProbes()
				cancel()
				server.Close()
			})
			const model = "gemini-3.1-flash-lite"
			auth := antigravityTestAuth("cached-cooldown-"+name, server.URL)
			retry := time.Now().Add(time.Hour)
			auth.ModelStates = map[string]*coreauth.ModelState{
				model: {Unavailable: true, NextRetryAfter: retry, Status: coreauth.StatusError, Quota: coreauth.QuotaState{Exceeded: true, NextRecoverAt: retry}},
			}
			auth, errRegister := svc.coreManager.Register(ctx, auth)
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			reg := registry.GetGlobalRegistry()
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
			key := svc.antigravityCapabilityKey(auth)
			hints := antigravityModelCapabilityHints{ModelIDs: map[string]struct{}{model: {}}, revision: 1}
			antigravityCapabilityMu.Lock()
			antigravityCapabilityCache[key] = antigravityCapabilityCacheEntry{hints: hints, expiresAt: time.Now().Add(-time.Hour)}
			antigravityCapabilityMu.Unlock()
			if registered {
				svc.applyAntigravityModelHints(ctx, auth, "antigravity", hints, key, auth.RegistrationEpoch, reg.ClientRegistrationEpoch(auth.ID))
				if !reg.IsModelQuotaExceededForClient(auth.ID, model) || !reg.IsModelSuspendedForClient(auth.ID, model) {
					t.Fatal("initial cooldown was not projected")
				}
			}
			current, _ := svc.coreManager.GetByID(auth.ID)
			svc.completeModelRegistrationForAuth(ctx, current)
			<-started
			if !reg.IsModelQuotaExceededForClient(auth.ID, model) || !reg.IsModelSuspendedForClient(auth.ID, model) {
				t.Error("cached publication lost active cooldown while the network probe was pending")
			}
			current, _ = svc.coreManager.GetByID(auth.ID)
			if state := current.ModelStates[model]; state == nil || !state.NextRetryAfter.Equal(retry) {
				t.Error("cached publication lost runtime cooldown")
			}
			if reg.ApplyClientModelProjections(auth.ID, reg.ClientRegistrationEpoch(auth.ID), current.Generation-1, []registry.ClientModelProjection{{ModelID: model}}) {
				t.Error("cached publication accepted a stale scheduling projection")
			}
			// A later authoritative empty response must still revoke the binding
			// and prune its cooldown; preserving cached state cannot prevent updates.
			unblock()
			svc.WaitAntigravityProbes()
			current, _ = svc.coreManager.GetByID(auth.ID)
			if len(reg.GetModelsForClient(auth.ID)) != 0 || current.ModelStates[model] != nil {
				t.Fatal("authoritative empty catalog did not revoke cached registration")
			}
		})
	}
}
