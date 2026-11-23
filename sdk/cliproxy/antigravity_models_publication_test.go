package cliproxy

import (
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestAntigravityPublishedCacheDoesNotReconcileEmptyRegistry(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{cfg: &config.Config{}, coreManager: manager}
	const model = "gemini-3.1-flash-lite"
	auth := antigravityTestAuth("publication-cooldown", "http://127.0.0.1:1")
	retry := time.Now().Add(time.Hour)
	auth.ModelStates = map[string]*coreauth.ModelState{model: {Unavailable: true, NextRetryAfter: retry, Status: coreauth.StatusError}}
	auth, _ = manager.Register(t.Context(), auth)
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
	key := svc.antigravityCapabilityKey(auth)
	hints := antigravityModelCapabilityHints{ModelIDs: map[string]struct{}{model: {}}, revision: 1}
	antigravityCapabilityMu.Lock()
	antigravityCapabilityCache[key] = antigravityCapabilityCacheEntry{hints: hints, probeRevision: 1, expiresAt: time.Now().Add(antigravityCapabilityCacheTTL)}
	antigravityCapabilityMu.Unlock()
	// Cache published, but the async publisher has not registered any model yet.
	svc.reconcileRegisteredModelStates(t.Context(), auth)
	current, _ := manager.GetByID(auth.ID)
	if current.ModelStates[model] == nil || !current.ModelStates[model].NextRetryAfter.Equal(retry) {
		t.Fatal("cache publication erased cooldown before registry publication")
	}
	epoch := GlobalModelRegistry().ClientRegistrationEpoch(auth.ID)
	svc.applyAntigravityModelHints(t.Context(), auth, "antigravity", hints, key, auth.RegistrationEpoch, epoch)
	current, _ = manager.GetByID(auth.ID)
	if current.ModelStates[model] == nil || !current.ModelStates[model].NextRetryAfter.Equal(retry) {
		t.Fatal("registry publication lost retained cooldown")
	}
}

func TestAntigravityRegistryPublicationFencesCacheRevision(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{cfg: &config.Config{}, coreManager: manager}
	auth := antigravityTestAuth("publication-version", "http://127.0.0.1:1")
	auth, _ = manager.Register(t.Context(), auth)
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
	key := svc.antigravityCapabilityKey(auth)
	epoch := GlobalModelRegistry().ClientRegistrationEpoch(auth.ID)
	old := antigravityModelCapabilityHints{ModelIDs: map[string]struct{}{"gemini-3.1-flash-lite": {}}, revision: 1}
	newer := antigravityModelCapabilityHints{ModelIDs: map[string]struct{}{}, revision: 2}
	antigravityCapabilityMu.Lock()
	antigravityCapabilityCache[key] = antigravityCapabilityCacheEntry{hints: newer, probeRevision: 2, expiresAt: time.Now().Add(antigravityCapabilityCacheTTL)}
	antigravityCapabilityMu.Unlock()
	// The old caller resumes after the newer cache publication, with the same registry epoch.
	svc.applyAntigravityModelHints(t.Context(), auth, "antigravity", old, key, auth.RegistrationEpoch, epoch)
	if GlobalModelRegistry().ClientRegistrationEpoch(auth.ID) != epoch {
		t.Fatal("old hints advanced registry epoch")
	}
	svc.applyAntigravityModelHints(t.Context(), auth, "antigravity", newer, key, auth.RegistrationEpoch, epoch)
	if len(GlobalModelRegistry().GetModelsForClient(auth.ID)) != 0 {
		t.Fatal("new empty catalog not applied")
	}
	// A second successful catalog may lose CAS to another publisher. It must
	// retry with a current auth/registry snapshot, never promote its old epoch.
	newest := antigravityModelCapabilityHints{ModelIDs: map[string]struct{}{"gemini-3.1-flash-lite": {}}, revision: 3}
	antigravityCapabilityMu.Lock()
	entry := antigravityCapabilityCache[key]
	entry.hints = newest
	entry.probeRevision = 3
	antigravityCapabilityCache[key] = entry
	antigravityCapabilityMu.Unlock()
	svc.applyAntigravityModelHints(t.Context(), auth, "antigravity", newest, key, auth.RegistrationEpoch, epoch)
	if len(GlobalModelRegistry().GetModelsForClient(auth.ID)) != 0 {
		t.Fatal("stale registry epoch was promoted")
	}
	svc.refreshAntigravityModels(t.Context())
	svc.WaitAntigravityProbes()
	if len(GlobalModelRegistry().GetModelsForClient(auth.ID)) != 1 {
		t.Fatal("periodic retry failed to publish the newer cached catalog")
	}
	// External removal must still win over a cached publication.
	publishedEpoch := GlobalModelRegistry().ClientRegistrationEpoch(auth.ID)
	GlobalModelRegistry().UnregisterClient(auth.ID)
	svc.applyAntigravityModelHints(t.Context(), auth, "antigravity", newest, key, auth.RegistrationEpoch, publishedEpoch)
	if len(GlobalModelRegistry().GetModelsForClient(auth.ID)) != 0 {
		t.Fatal("external unregister overwritten")
	}
}
