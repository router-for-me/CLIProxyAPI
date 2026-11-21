package cliproxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"os"
	"path/filepath"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// waitForAntigravityClientModel polls until the registry publishes modelID
// for clientID, bounded so a missing publication fails instead of hanging.
func waitForAntigravityClientModel(t *testing.T, clientID, modelID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !GlobalModelRegistry().ClientSupportsModel(clientID, modelID) {
		if time.Now().After(deadline) {
			t.Fatalf("client %s model %s was not published within 2s", clientID, modelID)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

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

func TestAntigravityRestartSeedsPersistedHintsBeforeProbeAndAcrossProbeFailure(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)

	baseTime := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	var nowUnix atomic.Int64
	nowUnix.Store(baseTime.UnixNano())
	prevNow := antigravityNowFunc
	antigravityNowFunc = func() time.Time { return time.Unix(0, nowUnix.Load()).UTC() }
	t.Cleanup(func() { antigravityNowFunc = prevNow })

	var status atomic.Int32
	status.Store(http.StatusOK)
	var blockProbe atomic.Bool
	probeStarted := make(chan struct{}, 4)
	releaseProbe := make(chan struct{})
	var once sync.Once
	unblockProbe := func() { once.Do(func() { close(releaseProbe) }) }

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if blockProbe.Load() {
			probeStarted <- struct{}{}
			select {
			case <-releaseProbe:
			case <-r.Context().Done():
				return
			}
		}
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(`{"models":{"gemini-3-flash":{},"gemini-3.1-flash-lite":{}},"webSearchModelIds":["gemini-3-flash"]}`))
	}))
	defer server.Close()
	defer unblockProbe()

	authDir := t.TempDir()
	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(authDir)

	manager := coreauth.NewManager(store, nil, nil)
	svc := &Service{cfg: &config.Config{AuthDir: authDir}, coreManager: manager}
	auth := &coreauth.Auth{
		ID:       "antigravity-restart.json",
		FileName: "antigravity-restart.json",
		Provider: "antigravity",
		Attributes: map[string]string{
			coreauth.AttributePath:          filepath.Join(authDir, "antigravity-restart.json"),
			coreauth.AttributeSource:        filepath.Join(authDir, "antigravity-restart.json"),
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
		},
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "token-restart",
			"project_id":   "project-restart",
			"base_url":     server.URL,
		},
	}
	auth, errRegister := manager.Register(t.Context(), auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })

	svc.registerModelsForAuth(t.Context(), auth)
	svc.WaitAntigravityProbes()
	if !GlobalModelRegistry().ClientSupportsModel(auth.ID, "gemini-3-flash") {
		t.Fatal("initial probe did not register gemini-3-flash")
	}

	// Simulate process restart after the persisted hints TTL has elapsed: clear in-memory
	// registry and capability caches, then reload the auth file from disk.
	nowUnix.Store(baseTime.Add(antigravityCapabilityCacheTTL + time.Minute).UnixNano())
	GlobalModelRegistry().UnregisterClient(auth.ID)
	resetAntigravityCapabilityCache()
	blockProbe.Store(true)
	status.Store(http.StatusServiceUnavailable)

	restartedManager := coreauth.NewManager(store, nil, nil)
	if errLoad := restartedManager.Load(t.Context()); errLoad != nil {
		t.Fatal(errLoad)
	}
	loadedAuth, ok := restartedManager.GetByID(auth.ID)
	if !ok || loadedAuth == nil {
		t.Fatal("restarted manager did not load auth from disk")
	}
	restartedSvc := &Service{cfg: &config.Config{AuthDir: authDir}, coreManager: restartedManager}

	restartedSvc.registerModelsForAuth(t.Context(), loadedAuth)
	<-probeStarted

	// Before the startup probe answers, persisted hints must make the account routable immediately.
	models := GlobalModelRegistry().GetModelsForClient(loadedAuth.ID)
	if len(models) != 2 || !GlobalModelRegistry().ClientSupportsModel(loadedAuth.ID, "gemini-3-flash") {
		t.Fatalf("expected persisted models to be registered before startup probe answers, got %v", models)
	}
	var flashModel *ModelInfo
	for _, m := range models {
		if m != nil && m.ID == "gemini-3-flash" {
			flashModel = m
			break
		}
	}
	if flashModel == nil || !flashModel.SupportsWebSearch {
		t.Fatalf("expected gemini-3-flash web search capability to be restored before probe answers, got %+v", flashModel)
	}

	// When the startup probe fails with a transient error, the account must stay routable.
	unblockProbe()
	restartedSvc.WaitAntigravityProbes()
	if !GlobalModelRegistry().ClientSupportsModel(loadedAuth.ID, "gemini-3-flash") {
		t.Fatal("failed startup probe made previously routable account unroutable")
	}

	// Changing project_id or base_url on a cold cache must not reuse hints from the old route.
	GlobalModelRegistry().UnregisterClient(loadedAuth.ID)
	resetAntigravityCapabilityCache()
	changedProjectAuth := loadedAuth.Clone()
	changedProjectAuth.Metadata["project_id"] = "different-project"
	if hints := restartedSvc.cachedAntigravityHints(changedProjectAuth); hints.ModelIDs != nil {
		t.Fatalf("expected project change to ignore persisted hints, got %v", hints)
	}
	changedEndpointAuth := loadedAuth.Clone()
	changedEndpointAuth.Metadata["base_url"] = "https://different-endpoint.invalid"
	if hints := restartedSvc.cachedAntigravityHints(changedEndpointAuth); hints.ModelIDs != nil {
		t.Fatalf("expected endpoint change to ignore persisted hints, got %v", hints)
	}
}

func TestAntigravityRestartReusesFreshPersistedHintsWithoutImmediateProbe(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)

	baseTime := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	var nowUnix atomic.Int64
	nowUnix.Store(baseTime.UnixNano())
	prevNow := antigravityNowFunc
	antigravityNowFunc = func() time.Time { return time.Unix(0, nowUnix.Load()).UTC() }
	t.Cleanup(func() { antigravityNowFunc = prevNow })

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":{"gemini-3-flash":{},"gemini-3.1-flash-lite":{}},"webSearchModelIds":["gemini-3-flash"]}`))
	}))
	defer server.Close()

	authDir := t.TempDir()
	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(authDir)

	manager := coreauth.NewManager(store, nil, nil)
	svc := &Service{cfg: &config.Config{AuthDir: authDir}, coreManager: manager}
	auth := &coreauth.Auth{
		ID:       "antigravity-fresh-restart.json",
		FileName: "antigravity-fresh-restart.json",
		Provider: "antigravity",
		Attributes: map[string]string{
			coreauth.AttributePath:          filepath.Join(authDir, "antigravity-fresh-restart.json"),
			coreauth.AttributeSource:        filepath.Join(authDir, "antigravity-fresh-restart.json"),
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
		},
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "token-fresh-restart",
			"project_id":   "project-fresh-restart",
			"base_url":     server.URL,
		},
	}
	auth, errRegister := manager.Register(t.Context(), auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })

	// Initial startup probes once and persists capability hints with freshness metadata.
	svc.registerModelsForAuth(t.Context(), auth)
	svc.WaitAntigravityProbes()
	if calls.Load() != 1 {
		t.Fatalf("initial probe calls = %d, want 1", calls.Load())
	}
	if !GlobalModelRegistry().ClientSupportsModel(auth.ID, "gemini-3-flash") {
		t.Fatal("initial probe did not register gemini-3-flash")
	}

	// Simulate process restart 5 minutes later (well within antigravityCapabilityCacheTTL).
	nowUnix.Store(baseTime.Add(5 * time.Minute).UnixNano())
	GlobalModelRegistry().UnregisterClient(auth.ID)
	resetAntigravityCapabilityCache()

	restartedManager := coreauth.NewManager(store, nil, nil)
	if errLoad := restartedManager.Load(t.Context()); errLoad != nil {
		t.Fatal(errLoad)
	}
	loadedAuth, ok := restartedManager.GetByID(auth.ID)
	if !ok || loadedAuth == nil {
		t.Fatal("restarted manager did not load auth from disk")
	}
	restartedSvc := &Service{cfg: &config.Config{AuthDir: authDir}, coreManager: restartedManager}

	restartedSvc.registerModelsForAuth(t.Context(), loadedAuth)
	restartedSvc.WaitAntigravityProbes()

	if !GlobalModelRegistry().ClientSupportsModel(loadedAuth.ID, "gemini-3-flash") {
		t.Fatal("restarted service did not register persisted models")
	}
	if calls.Load() != 1 {
		t.Fatalf("fresh persisted hints triggered immediate restart probe: calls = %d, want 1", calls.Load())
	}

	key := restartedSvc.antigravityCapabilityKey(loadedAuth)
	antigravityCapabilityMu.RLock()
	entry := antigravityCapabilityCache[key]
	antigravityCapabilityMu.RUnlock()
	wantExpiry := baseTime.Add(antigravityCapabilityCacheTTL)
	if !entry.expiresAt.Equal(wantExpiry) {
		t.Fatalf("seeded entry.expiresAt = %v, want %v", entry.expiresAt, wantExpiry)
	}

	// Periodic refresh before expiry must still skip probing.
	restartedSvc.refreshAntigravityModels(t.Context())
	restartedSvc.WaitAntigravityProbes()
	if calls.Load() != 1 {
		t.Fatalf("refresh before expiry triggered probe: calls = %d, want 1", calls.Load())
	}

	// Advancing past TTL triggers the scheduled refresh probe.
	nowUnix.Store(wantExpiry.Add(time.Second).UnixNano())
	restartedSvc.refreshAntigravityModels(t.Context())
	restartedSvc.WaitAntigravityProbes()
	if calls.Load() != 2 {
		t.Fatalf("refresh after expiry did not probe: calls = %d, want 2", calls.Load())
	}
}

func TestAntigravityRegistrationEpochAdvanceReusesFreshPersistedHints(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)

	baseTime := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	var nowUnix atomic.Int64
	nowUnix.Store(baseTime.UnixNano())
	prevNow := antigravityNowFunc
	antigravityNowFunc = func() time.Time { return time.Unix(0, nowUnix.Load()).UTC() }
	t.Cleanup(func() { antigravityNowFunc = prevNow })

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":{"gemini-3-flash":{}}}`))
	}))
	defer server.Close()

	authDir := t.TempDir()
	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(authDir)

	manager := coreauth.NewManager(store, nil, nil)
	svc := &Service{cfg: &config.Config{AuthDir: authDir}, coreManager: manager}
	auth := &coreauth.Auth{
		ID:       "antigravity-epoch-advance.json",
		FileName: "antigravity-epoch-advance.json",
		Provider: "antigravity",
		Attributes: map[string]string{
			coreauth.AttributePath:          filepath.Join(authDir, "antigravity-epoch-advance.json"),
			coreauth.AttributeSource:        filepath.Join(authDir, "antigravity-epoch-advance.json"),
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
		},
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "token-epoch",
			"project_id":   "project-epoch",
			"base_url":     server.URL,
		},
	}
	auth, errRegister := manager.Register(t.Context(), auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })

	svc.registerModelsForAuth(t.Context(), auth)
	svc.WaitAntigravityProbes()
	if calls.Load() != 1 {
		t.Fatalf("initial probe calls = %d, want 1", calls.Load())
	}
	initialEpoch := auth.RegistrationEpoch

	// Manager.Load advances RegistrationEpoch across retained auths, invalidating the old cache key.
	nowUnix.Store(baseTime.Add(10 * time.Minute).UnixNano())
	if errLoad := manager.Load(t.Context()); errLoad != nil {
		t.Fatal(errLoad)
	}
	reloaded, ok := manager.GetByID(auth.ID)
	if !ok || reloaded == nil {
		t.Fatal("reloaded auth missing after Manager.Load")
	}
	if reloaded.RegistrationEpoch <= initialEpoch {
		t.Fatalf("RegistrationEpoch = %d, want > %d", reloaded.RegistrationEpoch, initialEpoch)
	}

	svc.registerModelsForAuth(t.Context(), reloaded)
	svc.WaitAntigravityProbes()
	if calls.Load() != 1 {
		t.Fatalf("RegistrationEpoch advance triggered probe burst: calls = %d, want 1", calls.Load())
	}
	if !GlobalModelRegistry().ClientSupportsModel(reloaded.ID, "gemini-3-flash") {
		t.Fatal("reloaded auth missing gemini-3-flash")
	}
}

func TestAntigravitySeededPersistedHintsClampsExpiryAndHandlesLegacyMetadata(t *testing.T) {
	baseTime := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	currentRouteFP := sdkAuth.AntigravityCapabilityRouteFingerprint("", antigravityModelUserAgent())

	cases := []struct {
		name      string
		persisted map[string]any
		wantExp   time.Time
		wantCalls int32
	}{
		{
			name: "far future expires_at is clamped to antigravityCapabilityCacheTTL",
			persisted: map[string]any{
				"project_id":        "project-clamp",
				"endpoint":          "https://clamp.invalid",
				"model_ids":         []any{"gemini-3-flash"},
				"route_fingerprint": currentRouteFP,
				"expires_at":        baseTime.Add(48 * time.Hour).Format(time.RFC3339),
			},
			wantExp:   baseTime.Add(antigravityCapabilityCacheTTL),
			wantCalls: 0,
		},
		{
			name: "expires_at beyond updated_at plus TTL is capped by updated_at plus TTL",
			persisted: map[string]any{
				"project_id":        "project-clamp",
				"endpoint":          "https://clamp.invalid",
				"model_ids":         []any{"gemini-3-flash"},
				"route_fingerprint": currentRouteFP,
				"updated_at":        baseTime.Add(-2 * time.Hour).Format(time.RFC3339),
				"expires_at":        baseTime.Add(10 * time.Hour).Format(time.RFC3339),
			},
			wantExp:   baseTime.Add(-2 * time.Hour).Add(antigravityCapabilityCacheTTL),
			wantCalls: 0,
		},
		{
			name: "updated_at without expires_at derives expiry from TTL",
			persisted: map[string]any{
				"project_id":        "project-clamp",
				"endpoint":          "https://clamp.invalid",
				"model_ids":         []any{"gemini-3-flash"},
				"route_fingerprint": currentRouteFP,
				"updated_at":        baseTime.Add(-30 * time.Minute).Format(time.RFC3339),
			},
			wantExp:   baseTime.Add(-30 * time.Minute).Add(antigravityCapabilityCacheTTL),
			wantCalls: 0,
		},
		{
			name: "legacy persisted metadata without timestamps seeds stale fallback and probes once",
			persisted: map[string]any{
				"project_id": "project-clamp",
				"endpoint":   "https://clamp.invalid",
				"model_ids":  []any{"gemini-3-flash"},
			},
			wantExp:   time.Time{},
			wantCalls: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetAntigravityCapabilityCache()
			t.Cleanup(resetAntigravityCapabilityCache)

			prevNow := antigravityNowFunc
			antigravityNowFunc = func() time.Time { return baseTime }
			t.Cleanup(func() { antigravityNowFunc = prevNow })

			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"models":{"gemini-3-flash":{}}}`))
			}))
			defer server.Close()

			tc.persisted["endpoint"] = server.URL
			svc := &Service{cfg: &config.Config{}}
			auth := &coreauth.Auth{
				ID:       "antigravity-clamp.json",
				Provider: "antigravity",
				Metadata: map[string]any{
					"access_token":                   "token-clamp",
					"project_id":                     "project-clamp",
					"base_url":                       server.URL,
					antigravityPersistedHintsMetaKey: tc.persisted,
				},
			}

			seededHints := svc.cachedAntigravityHints(auth)
			if _, ok := seededHints.ModelIDs["gemini-3-flash"]; !ok {
				t.Fatalf("seeded hints missing gemini-3-flash: %+v", seededHints)
			}
			key := svc.antigravityCapabilityKey(auth)
			antigravityCapabilityMu.RLock()
			entry := antigravityCapabilityCache[key]
			antigravityCapabilityMu.RUnlock()
			if !entry.expiresAt.Equal(tc.wantExp) {
				t.Fatalf("seeded entry.expiresAt = %v, want %v", entry.expiresAt, tc.wantExp)
			}

			svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
			if calls.Load() != tc.wantCalls {
				t.Fatalf("fetch calls = %d, want %d", calls.Load(), tc.wantCalls)
			}
		})
	}
}

func TestAntigravityProxyChangeReprobesAndKeepsPersistedFallback(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)

	baseTime := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	var nowUnix atomic.Int64
	nowUnix.Store(baseTime.UnixNano())
	prevNow := antigravityNowFunc
	antigravityNowFunc = func() time.Time { return time.Unix(0, nowUnix.Load()).UTC() }
	t.Cleanup(func() { antigravityNowFunc = prevNow })

	var callsA, callsB atomic.Int32
	var statusB atomic.Int32
	statusB.Store(http.StatusServiceUnavailable)

	proxyA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callsA.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":{"gemini-3-flash":{}}}`))
	}))
	defer proxyA.Close()

	proxyB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callsB.Add(1)
		status := int(statusB.Load())
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"models":{"gemini-3.1-flash-lite":{}}}`))
		}
	}))
	defer proxyB.Close()

	authDir := t.TempDir()
	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(authDir)

	manager := coreauth.NewManager(store, nil, nil)
	svc := &Service{cfg: &config.Config{AuthDir: authDir}, coreManager: manager}
	auth := &coreauth.Auth{
		ID:       "antigravity-proxy-switch.json",
		FileName: "antigravity-proxy-switch.json",
		Provider: "antigravity",
		ProxyURL: proxyA.URL,
		Attributes: map[string]string{
			coreauth.AttributePath:          filepath.Join(authDir, "antigravity-proxy-switch.json"),
			coreauth.AttributeSource:        filepath.Join(authDir, "antigravity-proxy-switch.json"),
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
		},
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "token-proxy-switch",
			"project_id":   "project-proxy-switch",
			"base_url":     "http://upstream.invalid",
		},
	}
	auth, errRegister := manager.Register(t.Context(), auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })

	// Initial probe through proxy A succeeds and persists hints.
	svc.registerModelsForAuth(t.Context(), auth)
	svc.WaitAntigravityProbes()
	if callsA.Load() != 1 || callsB.Load() != 0 {
		t.Fatalf("initial probe counts: proxyA=%d proxyB=%d, want 1 and 0", callsA.Load(), callsB.Load())
	}
	if !GlobalModelRegistry().ClientSupportsModel(auth.ID, "gemini-3-flash") {
		t.Fatal("initial probe via proxy A did not register gemini-3-flash")
	}

	// Switching proxy to proxy B within TTL must NOT treat proxy A's persisted hints as fresh.
	// When proxy B fails transiently, proxy A's persisted catalog remains available as stale fallback.
	nowUnix.Store(baseTime.Add(5 * time.Minute).UnixNano())
	updatedAuth, ok := manager.GetByID(auth.ID)
	if !ok || updatedAuth == nil {
		t.Fatal("auth missing before proxy switch")
	}
	updatedAuth = updatedAuth.Clone()
	updatedAuth.ProxyURL = proxyB.URL
	updatedAuth, errUpdate := manager.Update(t.Context(), updatedAuth)
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}

	svc.registerModelsForAuth(t.Context(), updatedAuth)
	svc.WaitAntigravityProbes()
	if callsB.Load() != 1 {
		t.Fatalf("proxy switch to proxy B did not probe new route: proxyA=%d proxyB=%d", callsA.Load(), callsB.Load())
	}
	if !GlobalModelRegistry().ClientSupportsModel(updatedAuth.ID, "gemini-3-flash") {
		t.Fatal("failed proxy B probe discarded stale persisted fallback")
	}
	keyB := svc.antigravityCapabilityKey(updatedAuth)
	antigravityCapabilityMu.RLock()
	entryB := antigravityCapabilityCache[keyB]
	antigravityCapabilityMu.RUnlock()
	if !entryB.expiresAt.IsZero() {
		t.Fatalf("proxy A persisted hints marked fresh for proxy B: expiresAt=%v", entryB.expiresAt)
	}

	// Once proxy B recovers and backoff elapses, the new route's catalog replaces the fallback.
	statusB.Store(http.StatusOK)
	nowUnix.Store(baseTime.Add(10 * time.Minute).UnixNano())
	svc.refreshAntigravityModels(t.Context())
	svc.WaitAntigravityProbes()
	if callsB.Load() != 2 {
		t.Fatalf("proxy B recovery probe count = %d, want 2", callsB.Load())
	}
	if !GlobalModelRegistry().ClientSupportsModel(updatedAuth.ID, "gemini-3.1-flash-lite") {
		t.Fatal("proxy B catalog did not register gemini-3.1-flash-lite")
	}
	if GlobalModelRegistry().ClientSupportsModel(updatedAuth.ID, "gemini-3-flash") {
		t.Fatal("stale proxy A model remained registered after proxy B probe succeeded")
	}

	// Restarting with proxy B still configured reuses proxy B's fresh persisted hints with 0 probes.
	nowUnix.Store(baseTime.Add(15 * time.Minute).UnixNano())
	GlobalModelRegistry().UnregisterClient(updatedAuth.ID)
	resetAntigravityCapabilityCache()
	restartedManager := coreauth.NewManager(store, nil, nil)
	if errLoad := restartedManager.Load(t.Context()); errLoad != nil {
		t.Fatal(errLoad)
	}
	reloaded, ok := restartedManager.GetByID(auth.ID)
	if !ok || reloaded == nil {
		t.Fatal("reloaded auth missing after restart")
	}
	reloaded = reloaded.Clone()
	reloaded.ProxyURL = proxyB.URL
	reloaded, errUpdate = restartedManager.Update(t.Context(), reloaded)
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	restartedSvc := &Service{cfg: &config.Config{AuthDir: authDir}, coreManager: restartedManager}
	restartedSvc.registerModelsForAuth(t.Context(), reloaded)
	restartedSvc.WaitAntigravityProbes()
	if callsB.Load() != 2 {
		t.Fatalf("restart on unchanged proxy B triggered probe: proxyB=%d, want 2", callsB.Load())
	}
	if !GlobalModelRegistry().ClientSupportsModel(reloaded.ID, "gemini-3.1-flash-lite") {
		t.Fatal("restarted service on proxy B missing persisted gemini-3.1-flash-lite")
	}
}

func TestAntigravityAuthUpdateWithSkipPersistStillPersistsHintsAcrossRestart(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":{"gemini-3-flash":{}}}`))
	}))
	defer server.Close()

	authDir := t.TempDir()
	authPath := filepath.Join(authDir, "antigravity-watcher.json")
	initialJSON := fmt.Sprintf(`{"type":"antigravity","access_token":"token-watcher","project_id":"project-watcher","base_url":%q}`, server.URL)
	if errWrite := os.WriteFile(authPath, []byte(initialJSON), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}

	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	manager := coreauth.NewManager(store, nil, nil)
	svc := &Service{cfg: &config.Config{AuthDir: authDir}, coreManager: manager}
	auth := &coreauth.Auth{
		ID:       "antigravity-watcher.json",
		FileName: "antigravity-watcher.json",
		Provider: "antigravity",
		Attributes: map[string]string{
			coreauth.AttributePath:          authPath,
			coreauth.AttributeSource:        authPath,
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
		},
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "token-watcher",
			"project_id":   "project-watcher",
			"base_url":     server.URL,
		},
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })

	// Exercise the real consumeAuthUpdates / runtimeAuthSyncHook path which wraps ctx with WithSkipPersist.
	svc.handleAuthUpdate(coreauth.WithSkipPersist(t.Context()), watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionAdd,
		ID:     auth.ID,
		Auth:   auth,
	})
	svc.WaitAntigravityProbes()
	if !GlobalModelRegistry().ClientSupportsModel(auth.ID, "gemini-3-flash") {
		t.Fatal("initial watcher auth update did not register gemini-3-flash")
	}

	// Simulate restart and verify hints were written to disk despite WithSkipPersist on the auth update.
	GlobalModelRegistry().UnregisterClient(auth.ID)
	resetAntigravityCapabilityCache()
	restartedManager := coreauth.NewManager(store, nil, nil)
	if errLoad := restartedManager.Load(t.Context()); errLoad != nil {
		t.Fatal(errLoad)
	}
	loadedAuth, ok := restartedManager.GetByID(auth.ID)
	if !ok || loadedAuth == nil {
		t.Fatal("restarted manager did not load auth")
	}
	restartedSvc := &Service{cfg: &config.Config{AuthDir: authDir}, coreManager: restartedManager}
	if hints := restartedSvc.cachedAntigravityHints(loadedAuth); hints.ModelIDs == nil {
		t.Fatal("expected capability hints to be persisted to disk when auth was registered via WithSkipPersist")
	}
}

type flakyAntigravityHintStore struct {
	*sdkAuth.FileTokenStore
	saveMu           sync.Mutex
	failSaves        atomic.Bool
	blockFirst       atomic.Bool
	blockAll         atomic.Bool
	ignoreCtxOnBlock atomic.Bool
	firstSaveIn      chan struct{}
	saveEntered      chan string
	releaseSave      chan struct{}
	remoteMu         sync.Mutex
	remoteFiles      map[string][]byte
}

func (s *flakyAntigravityHintStore) Save(ctx context.Context, auth *coreauth.Auth) (string, error) {
	if s.blockAll.Load() && s.saveEntered != nil && auth != nil {
		s.saveEntered <- auth.ID
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	path, errSave := s.FileTokenStore.Save(ctx, auth)
	if errSave != nil {
		return "", errSave
	}
	if s.blockFirst.CompareAndSwap(true, false) {
		close(s.firstSaveIn)
		if s.ignoreCtxOnBlock.Load() {
			<-s.releaseSave
		} else {
			select {
			case <-s.releaseSave:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}
	if s.blockAll.Load() {
		if s.ignoreCtxOnBlock.Load() {
			<-s.releaseSave
		} else {
			select {
			case <-s.releaseSave:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
	}
	if s.failSaves.Load() {
		return "", fmt.Errorf("simulated remote store write failure")
	}
	if path != "" {
		if data, errRead := os.ReadFile(path); errRead == nil {
			s.remoteMu.Lock()
			if s.remoteFiles == nil {
				s.remoteFiles = make(map[string][]byte)
			}
			s.remoteFiles[path] = data
			s.remoteMu.Unlock()
		}
	}
	return path, nil
}

func (s *flakyAntigravityHintStore) restoreRemoteToDisk() error {
	s.remoteMu.Lock()
	defer s.remoteMu.Unlock()
	for path, data := range s.remoteFiles {
		if errWrite := os.WriteFile(path, data, 0o600); errWrite != nil {
			return errWrite
		}
	}
	return nil
}

func TestAntigravityPersistedHintsFencesStaleRevision(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)

	authDir := t.TempDir()
	baseStore := sdkAuth.NewFileTokenStore()
	baseStore.SetBaseDir(authDir)
	store := &flakyAntigravityHintStore{
		FileTokenStore: baseStore,
		firstSaveIn:    make(chan struct{}),
		releaseSave:    make(chan struct{}),
	}

	manager := coreauth.NewManager(store, nil, nil)
	svc := &Service{cfg: &config.Config{AuthDir: authDir}, coreManager: manager}
	auth := &coreauth.Auth{
		ID:       "antigravity-fence-persist.json",
		FileName: "antigravity-fence-persist.json",
		Provider: "antigravity",
		Attributes: map[string]string{
			coreauth.AttributePath:          filepath.Join(authDir, "antigravity-fence-persist.json"),
			coreauth.AttributeSource:        filepath.Join(authDir, "antigravity-fence-persist.json"),
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
		},
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "token-fence-persist",
			"project_id":   "project-fence-persist",
			"base_url":     "https://daily.invalid",
		},
	}
	auth, errRegister := manager.Register(t.Context(), auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })

	key := svc.antigravityCapabilityKey(auth)
	oldHints := antigravityModelCapabilityHints{
		ModelIDs: map[string]struct{}{"gemini-3-flash": {}},
		revision: 1,
	}
	newHints := antigravityModelCapabilityHints{
		ModelIDs: map[string]struct{}{"gemini-3.1-flash-lite": {}},
		revision: 2,
	}

	// Revision 1 starts persisting first and blocks inside Store.Save.
	antigravityCapabilityMu.Lock()
	antigravityCapabilityCache[key] = antigravityCapabilityCacheEntry{
		hints:     oldHints.clone(),
		expiresAt: time.Now().Add(antigravityCapabilityCacheTTL),
	}
	antigravityCapabilityMu.Unlock()
	store.blockFirst.Store(true)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		svc.persistAntigravityCapabilityHints(t.Context(), auth, key, "https://daily.invalid", oldHints)
	}()
	<-store.firstSaveIn

	// Revision 2 publishes to the cache while revision 1 is stalled in Store.Save.
	antigravityCapabilityMu.Lock()
	antigravityCapabilityCache[key] = antigravityCapabilityCacheEntry{
		hints:     newHints.clone(),
		expiresAt: time.Now().Add(antigravityCapabilityCacheTTL),
	}
	antigravityCapabilityMu.Unlock()
	go func() {
		defer wg.Done()
		svc.persistAntigravityCapabilityHints(t.Context(), auth, key, "https://daily.invalid", newHints)
	}()
	close(store.releaseSave)
	wg.Wait()

	// A delayed caller from revision 1 also attempts to persist afterward.
	svc.persistAntigravityCapabilityHints(t.Context(), auth, key, "https://daily.invalid", oldHints)

	resetAntigravityCapabilityCache()
	restartedManager := coreauth.NewManager(baseStore, nil, nil)
	if errLoad := restartedManager.Load(t.Context()); errLoad != nil {
		t.Fatal(errLoad)
	}
	loadedAuth, _ := restartedManager.GetByID(auth.ID)
	restartedSvc := &Service{cfg: &config.Config{AuthDir: authDir}, coreManager: restartedManager}
	hints := restartedSvc.cachedAntigravityHints(loadedAuth)
	if _, ok := hints.ModelIDs["gemini-3.1-flash-lite"]; !ok || len(hints.ModelIDs) != 1 {
		t.Fatalf("stale revision overwrote newer persisted hints: got %v", hints.ModelIDs)
	}
}

func TestAntigravityPersistedHintsRetriesAfterTransientStoreFailure(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":{"gemini-3-flash":{}}}`))
	}))
	defer server.Close()

	authDir := t.TempDir()
	baseStore := sdkAuth.NewFileTokenStore()
	baseStore.SetBaseDir(authDir)
	store := &flakyAntigravityHintStore{FileTokenStore: baseStore}

	manager := coreauth.NewManager(store, nil, nil)
	svc := &Service{cfg: &config.Config{AuthDir: authDir}, coreManager: manager}
	auth := &coreauth.Auth{
		ID:       "antigravity-retry-persist.json",
		FileName: "antigravity-retry-persist.json",
		Provider: "antigravity",
		Attributes: map[string]string{
			coreauth.AttributePath:          filepath.Join(authDir, "antigravity-retry-persist.json"),
			coreauth.AttributeSource:        filepath.Join(authDir, "antigravity-retry-persist.json"),
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
		},
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "token-retry-persist",
			"project_id":   "project-retry-persist",
			"base_url":     server.URL,
		},
	}
	auth, errRegister := manager.Register(t.Context(), auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })

	// Fail store writes during the initial probe.
	store.failSaves.Store(true)
	svc.registerModelsForAuth(t.Context(), auth)
	svc.WaitAntigravityProbes()

	// Restore store and run a refresh/registration pass with the same cached hints.
	store.failSaves.Store(false)
	svc.refreshAntigravityModels(t.Context())
	svc.WaitAntigravityProbes()

	// Simulate remote-backed restart (e.g. Postgres/ObjectStore resetting local spool from remote)
	// and verify hints were persisted to remote once store recovered.
	if errRemove := os.RemoveAll(authDir); errRemove != nil {
		t.Fatal(errRemove)
	}
	if errMkdir := os.MkdirAll(authDir, 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	if errRestore := store.restoreRemoteToDisk(); errRestore != nil {
		t.Fatal(errRestore)
	}
	GlobalModelRegistry().UnregisterClient(auth.ID)
	resetAntigravityCapabilityCache()
	restartedManager := coreauth.NewManager(baseStore, nil, nil)
	if errLoad := restartedManager.Load(t.Context()); errLoad != nil {
		t.Fatal(errLoad)
	}
	loadedAuth, _ := restartedManager.GetByID(auth.ID)
	restartedSvc := &Service{cfg: &config.Config{AuthDir: authDir}, coreManager: restartedManager}
	if hints := restartedSvc.cachedAntigravityHints(loadedAuth); hints.ModelIDs == nil {
		t.Fatal("expected identical hints to be persisted after transient store failure recovered")
	}
}

func TestAntigravityBlockedHintPersistenceDoesNotBlockPublicationOrOtherAccounts(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)

	var authAModel atomic.Value
	authAModel.Store("gemini-3-flash")
	authBProbed := make(chan struct{}, 2)
	authARefreshed := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer token-slow-b" {
			_, _ = w.Write([]byte(`{"models":{"gemini-3-flash":{}}}`))
			authBProbed <- struct{}{}
			// Hold the response briefly after the server-side signal so the client
			// deterministically receives and publishes it well after authBProbed
			// fires; waiting only on that signal would race the probe result
			// against later authB updates.
			time.Sleep(200 * time.Millisecond)
			return
		}
		model := authAModel.Load().(string)
		_, _ = fmt.Fprintf(w, `{"models":{%q:{}}}`, model)
		if model == "gemini-3.1-flash-lite" {
			authARefreshed <- struct{}{}
		}
	}))
	defer server.Close()

	authDir := t.TempDir()
	baseStore := sdkAuth.NewFileTokenStore()
	baseStore.SetBaseDir(authDir)
	store := &flakyAntigravityHintStore{
		FileTokenStore: baseStore,
		firstSaveIn:    make(chan struct{}),
		saveEntered:    make(chan string, 8),
		releaseSave:    make(chan struct{}),
	}

	lifetime, cancelLifetime := context.WithCancel(t.Context())
	defer cancelLifetime()
	manager := coreauth.NewManager(store, nil, nil)
	svc := &Service{cfg: &config.Config{AuthDir: authDir}, coreManager: manager, antigravityContext: lifetime}

	authA := &coreauth.Auth{
		ID:       "antigravity-slow-save-a.json",
		FileName: "antigravity-slow-save-a.json",
		Provider: "antigravity",
		Attributes: map[string]string{
			coreauth.AttributePath:          filepath.Join(authDir, "antigravity-slow-save-a.json"),
			coreauth.AttributeSource:        filepath.Join(authDir, "antigravity-slow-save-a.json"),
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
		},
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "token-slow-a",
			"project_id":   "project-slow-a",
			"base_url":     server.URL,
		},
	}
	authB := &coreauth.Auth{
		ID:       "antigravity-slow-save-b.json",
		FileName: "antigravity-slow-save-b.json",
		Provider: "antigravity",
		Attributes: map[string]string{
			coreauth.AttributePath:          filepath.Join(authDir, "antigravity-slow-save-b.json"),
			coreauth.AttributeSource:        filepath.Join(authDir, "antigravity-slow-save-b.json"),
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
		},
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "token-slow-b",
			"project_id":   "project-slow-b",
			"base_url":     server.URL,
		},
	}
	authA, _ = manager.Register(t.Context(), authA)
	authB, _ = manager.Register(t.Context(), authB)
	t.Cleanup(func() {
		GlobalModelRegistry().UnregisterClient(authA.ID)
		GlobalModelRegistry().UnregisterClient(authB.ID)
	})

	// Block all subsequent store Save calls (holding store-wide saveMu).
	store.blockAll.Store(true)
	svc.registerModelsForAuth(t.Context(), authA)
	if got := <-store.saveEntered; got != authA.ID {
		t.Fatalf("first saveEntered = %q, want %q", got, authA.ID)
	}

	// Even while authA's metadata persistence is blocked in Store.Save, its models must already be published.
	if !GlobalModelRegistry().ClientSupportsModel(authA.ID, "gemini-3-flash") {
		cancelLifetime()
		svc.WaitAntigravityProbes()
		t.Fatal("blocked metadata persistence delayed model publication to registry")
	}

	// Refreshing models must not block on authA's stalled persistence, must still publish authB,
	// and must allow authA to run a subsequent probe when its cache expires.
	keyA := svc.antigravityCapabilityKey(authA)
	antigravityCapabilityMu.Lock()
	entryA := antigravityCapabilityCache[keyA]
	entryA.expiresAt = time.Time{}
	antigravityCapabilityCache[keyA] = entryA
	antigravityCapabilityMu.Unlock()
	authAModel.Store("gemini-3.1-flash-lite")

	svc.refreshAntigravityModels(t.Context())
	select {
	case <-authBProbed:
	case <-time.After(time.Second):
		cancelLifetime()
		svc.WaitAntigravityProbes()
		t.Fatal("stalled persistence on authA blocked authB probe")
	}
	select {
	case <-authARefreshed:
	case <-time.After(time.Second):
		cancelLifetime()
		svc.WaitAntigravityProbes()
		t.Fatal("stalled persistence on authA blocked subsequent probe for authA")
	}
	// authBProbed only proves the handler wrote the response; the probe may not
	// have parsed it or published the catalog yet. Wait for actual registry
	// publication before any authB update below, otherwise that update changes
	// authB's model settings and correctly discards the late probe result.
	waitForAntigravityClientModel(t, authB.ID, "gemini-3-flash")
	// Wait for authA's refreshed persistence to preempt authA's first Save and enter Save
	// (which happens after applyAntigravityModelHints published authA's updated catalog).
	select {
	case <-store.saveEntered:
	case <-time.After(time.Second):
		cancelLifetime()
		svc.WaitAntigravityProbes()
		t.Fatal("subsequent persistence task did not start after registry publication")
	}
	// While authA's background Save is blocked holding store.saveMu, a normal persisting
	// foreground Update on authB must preempt authA's background Save and succeed.
	store.blockAll.Store(false)
	updateBDone := make(chan error, 1)
	go func() {
		updatedB := authB.Clone()
		updatedB.Prefix = "b-prefix"
		_, errUpdateB := manager.Update(t.Context(), updatedB)
		updateBDone <- errUpdateB
	}()
	select {
	case errUpdateB := <-updateBDone:
		if errUpdateB != nil {
			cancelLifetime()
			svc.WaitAntigravityProbes()
			t.Fatal(errUpdateB)
		}
	case <-time.After(time.Second):
		cancelLifetime()
		svc.WaitAntigravityProbes()
		t.Fatal("persisting Update on authB blocked while store Save was stalled on authA")
	}
	// Also verify that if a store ignores context cancellation during a background Save on authA,
	// a foreground persisting Update on authB still honors its own context cancellation.
	svc.WaitAntigravityProbes()
	store.firstSaveIn = make(chan struct{})
	store.releaseSave = make(chan struct{})
	store.ignoreCtxOnBlock.Store(true)
	store.blockFirst.Store(true)
	newerHintsA := antigravityModelCapabilityHints{
		ModelIDs: map[string]struct{}{"gemini-3-flash": {}},
		revision: 99,
	}
	antigravityCapabilityMu.Lock()
	antigravityCapabilityCache[keyA] = antigravityCapabilityCacheEntry{
		hints:     newerHintsA.clone(),
		expiresAt: time.Now().Add(antigravityCapabilityCacheTTL),
	}
	antigravityCapabilityMu.Unlock()
	svc.queueAntigravityHintPersistence(t.Context(), authA, keyA, server.URL, newerHintsA)
	<-store.firstSaveIn

	ctxB, cancelB := context.WithCancel(t.Context())
	canceledUpdateBDone := make(chan error, 1)
	go func() {
		currentB, _ := manager.GetByID(authB.ID)
		nextB := currentB.Clone()
		nextB.Prefix = "b-prefix-2"
		_, errUpdate := manager.Update(ctxB, nextB)
		canceledUpdateBDone <- errUpdate
	}()
	cancelB()
	select {
	case <-canceledUpdateBDone:
	case <-time.After(time.Second):
		close(store.releaseSave)
		cancelLifetime()
		svc.WaitAntigravityProbes()
		t.Fatal("canceled foreground Update on authB remained stuck on authA's uncancelable store Save")
	}
	close(store.releaseSave)
	if !GlobalModelRegistry().ClientSupportsModel(authB.ID, "gemini-3-flash") {
		cancelLifetime()
		svc.WaitAntigravityProbes()
		t.Fatal("authB models were not published while authA persistence was stalled")
	}
	if !GlobalModelRegistry().ClientSupportsModel(authA.ID, "gemini-3.1-flash-lite") {
		cancelLifetime()
		svc.WaitAntigravityProbes()
		t.Fatal("authA updated models were not published while authA initial persistence was stalled")
	}

	// Canceling service lifetime must unblock the stalled persistence without closing releaseSave.
	cancelLifetime()
	doneWait := make(chan struct{})
	go func() {
		defer close(doneWait)
		svc.WaitAntigravityProbes()
	}()
	select {
	case <-doneWait:
	case <-time.After(time.Second):
		close(store.releaseSave)
		t.Fatal("service context cancellation did not cancel blocked metadata persistence")
	}
}

func TestAntigravityConcurrentAuthUpdatePreservesBothNewTokenAndPersistedHints(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":{"gemini-3-flash":{}}}`))
	}))
	defer server.Close()

	authDir := t.TempDir()
	baseStore := sdkAuth.NewFileTokenStore()
	baseStore.SetBaseDir(authDir)
	store := &flakyAntigravityHintStore{
		FileTokenStore: baseStore,
		firstSaveIn:    make(chan struct{}),
		releaseSave:    make(chan struct{}),
	}

	manager := coreauth.NewManager(store, nil, nil)
	svc := &Service{cfg: &config.Config{AuthDir: authDir}, coreManager: manager}
	auth := &coreauth.Auth{
		ID:       "antigravity-concurrent-update.json",
		FileName: "antigravity-concurrent-update.json",
		Provider: "antigravity",
		Attributes: map[string]string{
			coreauth.AttributePath:          filepath.Join(authDir, "antigravity-concurrent-update.json"),
			coreauth.AttributeSource:        filepath.Join(authDir, "antigravity-concurrent-update.json"),
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
		},
		Metadata: map[string]any{
			"type":         "antigravity",
			"access_token": "initial-token",
			"project_id":   "project-concurrent",
			"base_url":     server.URL,
		},
	}
	auth, errRegister := manager.Register(t.Context(), auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })

	// Capture a pre-probe auth snapshot before hints are persisted.
	staleSnapshot := auth.Clone()

	// 1. Interleave an active Update (saving rotated-token-1) with concurrent hint persistence.
	store.blockFirst.Store(true)
	staleSnapshot.Metadata["access_token"] = "rotated-token-1"
	updateDone := make(chan error, 1)
	go func() {
		_, errUpdate := manager.Update(t.Context(), staleSnapshot)
		updateDone <- errUpdate
	}()
	<-store.firstSaveIn

	key := svc.antigravityCapabilityKey(auth)
	hints := antigravityModelCapabilityHints{
		ModelIDs: map[string]struct{}{"gemini-3-flash": {}},
		revision: 1,
	}
	antigravityCapabilityMu.Lock()
	antigravityCapabilityCache[key] = antigravityCapabilityCacheEntry{
		hints:     hints.clone(),
		expiresAt: time.Now().Add(antigravityCapabilityCacheTTL),
	}
	antigravityCapabilityMu.Unlock()

	persistDone := make(chan struct{})
	go func() {
		defer close(persistDone)
		svc.persistAntigravityCapabilityHints(t.Context(), auth, key, server.URL, hints)
	}()
	close(store.releaseSave)
	if errUpdate := <-updateDone; errUpdate != nil {
		t.Fatal(errUpdate)
	}
	<-persistDone
	svc.WaitAntigravityProbes()

	// 2. Capture a snapshot that already contains version 1 hints ("gemini-3-flash"),
	// then persist version 2 hints ("gemini-3-flash", "gemini-3.1-flash-lite") while blocked,
	// and trigger manager.Update with the version-1 snapshot to preempt and then retry version 2.
	snapshotWithOldHints, _ := manager.GetByID(auth.ID)
	snapshotWithOldHints = snapshotWithOldHints.Clone()
	snapshotWithOldHints.Metadata["access_token"] = "rotated-token-2"

	store.firstSaveIn = make(chan struct{})
	store.releaseSave = make(chan struct{})
	store.blockFirst.Store(true)

	newerHints := antigravityModelCapabilityHints{
		ModelIDs: map[string]struct{}{
			"gemini-3-flash":        {},
			"gemini-3.1-flash-lite": {},
		},
		revision: 2,
	}
	antigravityCapabilityMu.Lock()
	antigravityCapabilityCache[key] = antigravityCapabilityCacheEntry{
		hints:             newerHints.clone(),
		persistedRevision: 1,
		expiresAt:         time.Now().Add(antigravityCapabilityCacheTTL),
	}
	antigravityCapabilityMu.Unlock()

	svc.queueAntigravityHintPersistence(t.Context(), auth, key, server.URL, newerHints)
	<-store.firstSaveIn

	// Update must preempt the blocked hint persistence without closing store.releaseSave,
	// and the preempted persistence must re-queue and save version 2 hints.
	if _, errUpdate := manager.Update(t.Context(), snapshotWithOldHints); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	svc.WaitAntigravityProbes()

	// A subsequent Update carrying version-1 hints in its snapshot must also preserve version 2 hints.
	snapshotWithOldHints.Metadata["access_token"] = "rotated-token-2"
	if _, errUpdate := manager.Update(t.Context(), snapshotWithOldHints); errUpdate != nil {
		t.Fatal(errUpdate)
	}

	// Simulate restart and verify both rotated-token-2 and version 2 hints survived on disk.
	GlobalModelRegistry().UnregisterClient(auth.ID)
	resetAntigravityCapabilityCache()
	restartedManager := coreauth.NewManager(baseStore, nil, nil)
	if errLoad := restartedManager.Load(t.Context()); errLoad != nil {
		t.Fatal(errLoad)
	}
	loadedAuth, ok := restartedManager.GetByID(auth.ID)
	if !ok || loadedAuth == nil {
		t.Fatal("restarted manager did not load auth")
	}
	if gotToken, _ := loadedAuth.Metadata["access_token"].(string); gotToken != "rotated-token-2" {
		t.Fatalf("loaded access_token = %q, want rotated-token-2", gotToken)
	}
	restartedSvc := &Service{cfg: &config.Config{AuthDir: authDir}, coreManager: restartedManager}
	loadedHints := restartedSvc.cachedAntigravityHints(loadedAuth)
	if _, ok := loadedHints.ModelIDs["gemini-3.1-flash-lite"]; !ok || len(loadedHints.ModelIDs) != 2 {
		t.Fatalf("expected version 2 hints to survive preemption and stale-snapshot Update, got %v", loadedHints.ModelIDs)
	}
}
