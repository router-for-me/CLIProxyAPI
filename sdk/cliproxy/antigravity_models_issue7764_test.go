package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestAntigravityModelProbe_HonorsPerAuthUserAgent(t *testing.T) {
	cases := []struct {
		name      string
		attrUA    string
		metaUA    string
		wantUA    string
		expectDef bool
	}{
		{
			name:   "attributes user_agent",
			attrUA: "  custom-ua/1.0  ",
			wantUA: "custom-ua/1.0",
		},
		{
			name:   "metadata user_agent",
			metaUA: "  meta-ua/2.0  ",
			wantUA: "meta-ua/2.0",
		},
		{
			name:   "attributes overrides metadata",
			attrUA: "attr-ua/1.0",
			metaUA: "meta-ua/2.0",
			wantUA: "attr-ua/1.0",
		},
		{
			name:   "antigravity long ua strips nodejs client suffix",
			attrUA: "antigravity/hub/2.24.0 google-api-nodejs-client/10.3.0",
			wantUA: "antigravity/hub/2.24.0",
		},
		{
			name:      "empty falls back to global antigravityModelUserAgent",
			expectDef: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetAntigravityCapabilityCache()
			t.Cleanup(resetAntigravityCapabilityCache)

			currentUA := overrideAntigravityModelUserAgent(t)
			wantUA := tc.wantUA
			if tc.expectDef {
				wantUA = currentUA.Load().(string)
			}

			var gotUA atomic.Value
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				gotUA.Store(r.Header.Get("User-Agent"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"models":{"gemini-3.1-flash-lite":{}}}`))
			}))
			defer server.Close()

			svc := &Service{cfg: &config.Config{}}
			defaultAuth := antigravityTestAuth("ua-test", server.URL)
			auth := antigravityTestAuth("ua-test", server.URL)
			if tc.attrUA != "" {
				auth.Attributes["user_agent"] = tc.attrUA
			}
			if tc.metaUA != "" {
				auth.Metadata["user_agent"] = tc.metaUA
			}

			gotKey, hints := svc.fetchAntigravityModelCapabilityHints(t.Context(), auth)
			if calls.Load() != 1 {
				t.Fatalf("probe calls = %d, want 1", calls.Load())
			}
			if _, ok := hints.ModelIDs["gemini-3.1-flash-lite"]; !ok {
				t.Fatalf("missing expected model in hints: %+v", hints)
			}
			if actualUA, _ := gotUA.Load().(string); actualUA != wantUA {
				t.Fatalf("User-Agent header = %q, want %q", actualUA, wantUA)
			}
			if gotKey != svc.antigravityCapabilityKey(auth) {
				t.Fatalf("returned cacheKey %q != antigravityCapabilityKey %q", gotKey, svc.antigravityCapabilityKey(auth))
			}
			if !tc.expectDef && svc.antigravityCapabilityKey(auth) == svc.antigravityCapabilityKey(defaultAuth) {
				t.Fatalf("custom user_agent %q collided with default capability key %q", wantUA, gotKey)
			}
		})
	}
}

func TestAntigravityCachedHints_HonorsPerAuthUserAgentRouteFingerprint(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	_ = overrideAntigravityModelUserAgent(t)

	baseTime := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	prevNow := antigravityNowFunc
	antigravityNowFunc = func() time.Time { return baseTime }
	t.Cleanup(func() { antigravityNowFunc = prevNow })

	var calls atomic.Int32
	var gotUA atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		gotUA.Store(r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":{"gemini-3-flash":{}}}`))
	}))
	defer server.Close()

	svc := &Service{cfg: &config.Config{}}
	auth := antigravityTestAuth("ua-persisted", server.URL)
	auth.Attributes["user_agent"] = "custom-ua/1.0"
	auth.Metadata[antigravityPersistedHintsMetaKey] = buildPersistedAntigravityCapabilityHintsValue(
		"project-ua-persisted",
		server.URL,
		antigravityModelCapabilityHints{
			ModelIDs:         map[string]struct{}{"gemini-3.1-flash-lite": {}},
			updatedAt:        baseTime.Add(-5 * time.Minute),
			expiresAt:        baseTime.Add(antigravityCapabilityCacheTTL - 5*time.Minute),
			routeFingerprint: sdkauth.AntigravityCapabilityRouteFingerprint("", "custom-ua/1.0"),
		},
	)

	// Seeding via cachedAntigravityHints with matching per-auth UA must mark cache fresh.
	seeded := svc.cachedAntigravityHints(auth)
	if _, ok := seeded.ModelIDs["gemini-3.1-flash-lite"]; !ok {
		t.Fatalf("expected seeded persisted hints, got %+v", seeded)
	}
	_, fetched := svc.fetchAntigravityModelCapabilityHints(t.Context(), auth)
	if calls.Load() != 0 {
		t.Fatalf("fresh persisted hints for per-auth user_agent triggered %d probe(s), want 0", calls.Load())
	}
	if _, ok := fetched.ModelIDs["gemini-3.1-flash-lite"]; !ok {
		t.Fatalf("expected cached persisted model, got %+v", fetched)
	}

	// Changing per-auth user_agent must invalidate freshness and probe with the new UA.
	updated := auth.Clone()
	updated.Attributes["user_agent"] = "custom-ua/2.0"
	_, refreshed := svc.fetchAntigravityModelCapabilityHints(t.Context(), updated)
	if calls.Load() != 1 {
		t.Fatalf("changed per-auth user_agent probe calls = %d, want 1", calls.Load())
	}
	if actualUA, _ := gotUA.Load().(string); actualUA != "custom-ua/2.0" {
		t.Fatalf("probe User-Agent = %q, want %q", actualUA, "custom-ua/2.0")
	}
	if _, ok := refreshed.ModelIDs["gemini-3-flash"]; !ok {
		t.Fatalf("expected refreshed catalog for new per-auth UA, got %+v", refreshed)
	}
}

type issue7764BlockingStore struct {
	mu           sync.Mutex
	blockOnMatch func(*coreauth.Auth) bool
	saveStarted  chan struct{}
	releaseSave  chan struct{}
	startedOnce  sync.Once
}

func (s *issue7764BlockingStore) List(context.Context) ([]*coreauth.Auth, error) {
	return nil, nil
}

func (s *issue7764BlockingStore) Save(_ context.Context, auth *coreauth.Auth) (string, error) {
	s.mu.Lock()
	shouldBlock := s.blockOnMatch != nil && s.blockOnMatch(auth)
	s.mu.Unlock()
	if shouldBlock {
		s.startedOnce.Do(func() { close(s.saveStarted) })
		<-s.releaseSave
	}
	if auth == nil {
		return "", nil
	}
	return auth.ID, nil
}

func (s *issue7764BlockingStore) Delete(context.Context, string) error {
	return nil
}

func TestAntigravityApplyModelHints_FencedByPublishIfRegistrationCurrent(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*coreauth.Auth)
	}{
		{
			name:   "DisabledFlag",
			mutate: func(a *coreauth.Auth) { a.Disabled = true },
		},
		{
			name:   "StatusDisabled",
			mutate: func(a *coreauth.Auth) { a.Status = coreauth.StatusDisabled },
		},
	}

	modes := []struct {
		name           string
		capabilityOnly bool
	}{
		{name: "ReplaceClientModels", capabilityOnly: false},
		{name: "ApplyClientModelCapabilities", capabilityOnly: true},
	}

	for _, mode := range modes {
		for _, mut := range mutations {
			t.Run(mode.name+"/"+mut.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					resetAntigravityCapabilityCache()
					defer resetAntigravityCapabilityCache()

					const model = "gemini-3.1-flash-lite"
					store := &issue7764BlockingStore{
						blockOnMatch: func(a *coreauth.Auth) bool {
							return a != nil && (a.Disabled || a.Status == coreauth.StatusDisabled)
						},
						saveStarted: make(chan struct{}),
						releaseSave: make(chan struct{}),
					}
					var releaseOnce sync.Once
					releaseSave := func() { releaseOnce.Do(func() { close(store.releaseSave) }) }
					defer releaseSave()

					manager := coreauth.NewManager(store, nil, nil)
					svc := &Service{cfg: &config.Config{}, coreManager: manager}

					authID := "fence-" + mode.name + "-" + mut.name
					auth, errRegister := manager.Register(t.Context(), antigravityTestAuth(authID, "http://127.0.0.1:1"))
					if errRegister != nil {
						t.Fatal(errRegister)
					}
					reg := registry.GetGlobalRegistry()
					defer reg.UnregisterClient(auth.ID)

					if mode.capabilityOnly {
						reg.RegisterClient(auth.ID, "antigravity", []*ModelInfo{{ID: model, SupportsWebSearch: false}})
					}
					initialEpoch := reg.ClientRegistrationEpoch(auth.ID)
					key := svc.antigravityCapabilityKey(auth)

					var hints antigravityModelCapabilityHints
					if mode.capabilityOnly {
						hints = antigravityModelCapabilityHints{
							WebSearchModelIDs: map[string]struct{}{model: {}},
						}
					} else {
						hints = antigravityModelCapabilityHints{
							ModelIDs: map[string]struct{}{model: {}},
							revision: 1,
						}
						antigravityCapabilityMu.Lock()
						antigravityCapabilityCache[key] = antigravityCapabilityCacheEntry{
							hints:         hints,
							probeRevision: 1,
							expiresAt:     time.Now().Add(antigravityCapabilityCacheTTL),
						}
						antigravityCapabilityMu.Unlock()
					}

					disabled := auth.Clone()
					mut.mutate(disabled)

					updateDone := make(chan error, 1)
					go func() {
						_, errUpdate := manager.Update(t.Context(), disabled)
						updateDone <- errUpdate
					}()
					<-store.saveStarted

					// At this point Manager.Update holds lockAuthMutation(auth.ID) during Save,
					// while m.auths[auth.ID] still holds the active auth.
					applyDone := make(chan struct{})
					go func() {
						defer close(applyDone)
						svc.applyAntigravityModelHints(t.Context(), auth, "antigravity", hints, key, auth.RegistrationEpoch, initialEpoch)
					}()

					synctest.Wait()

					select {
					case <-applyDone:
						releaseSave()
						<-updateDone
						t.Fatal("applyAntigravityModelHints completed without waiting for PublishIfRegistrationCurrent during concurrent auth disable")
					default:
					}
					if mode.capabilityOnly {
						if models := reg.GetModelsForClient(auth.ID); len(models) != 1 || models[0].SupportsWebSearch {
							releaseSave()
							<-updateDone
							<-applyDone
							t.Fatalf("capability-only publication mutated registry while Update held mutation lock: %+v", models)
						}
					} else if gotEpoch := reg.ClientRegistrationEpoch(auth.ID); gotEpoch != initialEpoch {
						releaseSave()
						<-updateDone
						<-applyDone
						t.Fatalf("ReplaceClientModels advanced epoch from %d to %d while Update held mutation lock", initialEpoch, gotEpoch)
					}

					releaseSave()
					if errUpdate := <-updateDone; errUpdate != nil {
						t.Fatalf("manager.Update failed: %v", errUpdate)
					}
					<-applyDone

					if mode.capabilityOnly {
						models := reg.GetModelsForClient(auth.ID)
						if len(models) != 1 || models[0].SupportsWebSearch {
							t.Fatalf("stale capability-only publication mutated disabled auth models: %+v", models)
						}
					} else {
						if gotEpoch := reg.ClientRegistrationEpoch(auth.ID); gotEpoch != initialEpoch {
							t.Fatalf("registry epoch advanced from %d to %d after concurrent disable", initialEpoch, gotEpoch)
						}
						if got := reg.GetModelsForClient(auth.ID); len(got) != 0 {
							t.Fatalf("stale publication registered models for disabled auth: %+v", got)
						}
					}
				})
			})
		}
	}
}

func TestAntigravityApplyModelHints_SkipsStatusDisabledWithoutDisabledFlag(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)

	const model = "gemini-3.1-flash-lite"
	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{cfg: &config.Config{}, coreManager: manager}

	auth := antigravityTestAuth("status-disabled-apply", "http://127.0.0.1:1")
	auth.Disabled = false
	auth.Status = coreauth.StatusDisabled
	registered, errRegister := manager.Register(t.Context(), auth)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	reg := registry.GetGlobalRegistry()
	t.Cleanup(func() { reg.UnregisterClient(registered.ID) })

	key := svc.antigravityCapabilityKey(registered)
	hints := antigravityModelCapabilityHints{
		ModelIDs: map[string]struct{}{model: {}},
		revision: 1,
	}
	antigravityCapabilityMu.Lock()
	antigravityCapabilityCache[key] = antigravityCapabilityCacheEntry{
		hints:         hints,
		probeRevision: 1,
		expiresAt:     time.Now().Add(antigravityCapabilityCacheTTL),
	}
	antigravityCapabilityMu.Unlock()

	epoch := reg.ClientRegistrationEpoch(registered.ID)
	svc.applyAntigravityModelHints(t.Context(), registered, "antigravity", hints, key, registered.RegistrationEpoch, epoch)
	if got := reg.GetModelsForClient(registered.ID); len(got) != 0 {
		t.Fatalf("StatusDisabled auth published models: %+v", got)
	}

	// Also verify when caller holds an active snapshot but manager state transitioned to StatusDisabled.
	activeSnapshot := registered.Clone()
	activeSnapshot.Status = coreauth.StatusActive
	svc.applyAntigravityModelHints(t.Context(), activeSnapshot, "antigravity", hints, key, registered.RegistrationEpoch, epoch)
	if got := reg.GetModelsForClient(registered.ID); len(got) != 0 {
		t.Fatalf("active snapshot published models for StatusDisabled manager auth: %+v", got)
	}
}

func TestAntigravityApplyModelHints_RejectsConcurrentMetadataRouteChange(t *testing.T) {
	routeMutations := []struct {
		name   string
		mutate func(*coreauth.Auth)
		match  func(*coreauth.Auth) bool
	}{
		{
			name:   "MetadataUserAgent",
			mutate: func(a *coreauth.Auth) { a.Metadata["user_agent"] = "meta-ua/2.0" },
			match: func(a *coreauth.Auth) bool {
				ua, _ := a.Metadata["user_agent"].(string)
				return ua == "meta-ua/2.0"
			},
		},
		{
			name:   "MetadataProjectID",
			mutate: func(a *coreauth.Auth) { a.Metadata["project_id"] = "project-next" },
			match: func(a *coreauth.Auth) bool {
				pid, _ := a.Metadata["project_id"].(string)
				return pid == "project-next"
			},
		},
		{
			name:   "MetadataBaseURL",
			mutate: func(a *coreauth.Auth) { a.Metadata["base_url"] = "http://127.0.0.1:2" },
			match: func(a *coreauth.Auth) bool {
				baseURL, _ := a.Metadata["base_url"].(string)
				return baseURL == "http://127.0.0.1:2"
			},
		},
		{
			name:   "ProxyURL",
			mutate: func(a *coreauth.Auth) { a.ProxyURL = "http://proxy.invalid:8080" },
			match: func(a *coreauth.Auth) bool {
				return a.ProxyURL == "http://proxy.invalid:8080"
			},
		},
	}

	modes := []struct {
		name           string
		capabilityOnly bool
	}{
		{name: "ReplaceClientModels", capabilityOnly: false},
		{name: "ApplyClientModelCapabilities", capabilityOnly: true},
	}

	for _, mode := range modes {
		for _, mut := range routeMutations {
			t.Run(mode.name+"/"+mut.name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					resetAntigravityCapabilityCache()
					defer resetAntigravityCapabilityCache()

					const model = "gemini-3.1-flash-lite"
					store := &issue7764BlockingStore{
						blockOnMatch: func(a *coreauth.Auth) bool {
							return a != nil && mut.match(a)
						},
						saveStarted: make(chan struct{}),
						releaseSave: make(chan struct{}),
					}
					var releaseOnce sync.Once
					releaseSave := func() { releaseOnce.Do(func() { close(store.releaseSave) }) }
					defer releaseSave()

					manager := coreauth.NewManager(store, nil, nil)
					svc := &Service{cfg: &config.Config{}, coreManager: manager}

					authID := "route-change-" + mode.name + "-" + mut.name
					initial := antigravityTestAuth(authID, "http://127.0.0.1:1")
					initial.Metadata["user_agent"] = "meta-ua/1.0"
					if mut.name == "MetadataBaseURL" {
						delete(initial.Attributes, "base_url")
						initial.Metadata["base_url"] = "http://127.0.0.1:1"
					}
					auth, errRegister := manager.Register(t.Context(), initial)
					if errRegister != nil {
						t.Fatal(errRegister)
					}
					reg := registry.GetGlobalRegistry()
					defer reg.UnregisterClient(auth.ID)

					if mode.capabilityOnly {
						reg.RegisterClient(auth.ID, "antigravity", []*ModelInfo{{ID: model, SupportsWebSearch: false}})
					}
					initialEpoch := reg.ClientRegistrationEpoch(auth.ID)
					key := svc.antigravityCapabilityKey(auth)

					var hints antigravityModelCapabilityHints
					if mode.capabilityOnly {
						hints = antigravityModelCapabilityHints{
							WebSearchModelIDs: map[string]struct{}{model: {}},
						}
					} else {
						hints = antigravityModelCapabilityHints{
							ModelIDs: map[string]struct{}{model: {}},
							revision: 1,
						}
						antigravityCapabilityMu.Lock()
						antigravityCapabilityCache[key] = antigravityCapabilityCacheEntry{
							hints:         hints,
							probeRevision: 1,
							expiresAt:     time.Now().Add(antigravityCapabilityCacheTTL),
						}
						antigravityCapabilityMu.Unlock()
					}

					updated := auth.Clone()
					mut.mutate(updated)

					updateDone := make(chan error, 1)
					go func() {
						_, errUpdate := manager.Update(t.Context(), updated)
						updateDone <- errUpdate
					}()
					<-store.saveStarted

					applyDone := make(chan struct{})
					go func() {
						defer close(applyDone)
						svc.applyAntigravityModelHints(t.Context(), auth, "antigravity", hints, key, auth.RegistrationEpoch, initialEpoch)
					}()

					synctest.Wait()

					releaseSave()
					if errUpdate := <-updateDone; errUpdate != nil {
						t.Fatalf("manager.Update failed: %v", errUpdate)
					}
					<-applyDone

					if mode.capabilityOnly {
						models := reg.GetModelsForClient(auth.ID)
						if len(models) != 1 || models[0].SupportsWebSearch {
							t.Fatalf("stale capability-only publication applied after concurrent route change: %+v", models)
						}
					} else {
						if gotEpoch := reg.ClientRegistrationEpoch(auth.ID); gotEpoch != initialEpoch {
							t.Fatalf("registry epoch advanced from %d to %d after concurrent route change", initialEpoch, gotEpoch)
						}
						if got := reg.GetModelsForClient(auth.ID); len(got) != 0 {
							t.Fatalf("stale catalog published after concurrent route change: %+v", got)
						}
					}
				})
			})
		}
	}
}

func TestAntigravityApplyModelHints_UnblocksOnContextCancelDuringSave(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetAntigravityCapabilityCache()
		defer resetAntigravityCapabilityCache()

		const model = "gemini-3.1-flash-lite"
		store := &issue7764BlockingStore{
			blockOnMatch: func(a *coreauth.Auth) bool {
				return a != nil && a.Label == "saving"
			},
			saveStarted: make(chan struct{}),
			releaseSave: make(chan struct{}),
		}
		var releaseOnce sync.Once
		releaseSave := func() { releaseOnce.Do(func() { close(store.releaseSave) }) }
		defer releaseSave()

		manager := coreauth.NewManager(store, nil, nil)
		svc := &Service{cfg: &config.Config{}, coreManager: manager}

		auth, errRegister := manager.Register(t.Context(), antigravityTestAuth("cancel-during-save", "http://127.0.0.1:1"))
		if errRegister != nil {
			t.Fatal(errRegister)
		}
		reg := registry.GetGlobalRegistry()
		defer reg.UnregisterClient(auth.ID)

		initialEpoch := reg.ClientRegistrationEpoch(auth.ID)
		key := svc.antigravityCapabilityKey(auth)
		hints := antigravityModelCapabilityHints{
			ModelIDs: map[string]struct{}{model: {}},
			revision: 1,
		}
		antigravityCapabilityMu.Lock()
		antigravityCapabilityCache[key] = antigravityCapabilityCacheEntry{
			hints:         hints,
			probeRevision: 1,
			expiresAt:     time.Now().Add(antigravityCapabilityCacheTTL),
		}
		antigravityCapabilityMu.Unlock()

		saving := auth.Clone()
		saving.Label = "saving"
		updateDone := make(chan error, 1)
		go func() {
			_, errUpdate := manager.Update(t.Context(), saving)
			updateDone <- errUpdate
		}()
		<-store.saveStarted

		probeCtx, cancelProbe := context.WithCancel(t.Context())
		defer cancelProbe()

		applyDone := make(chan struct{})
		go func() {
			defer close(applyDone)
			svc.applyAntigravityModelHints(probeCtx, auth, "antigravity", hints, key, auth.RegistrationEpoch, initialEpoch)
		}()

		synctest.Wait()
		select {
		case <-applyDone:
			releaseSave()
			<-updateDone
			t.Fatal("applyAntigravityModelHints returned before cancel while Save held mutation lock")
		default:
		}

		// Cancel the probe context while Store.Save is still blocked.
		cancelProbe()
		synctest.Wait()

		select {
		case <-applyDone:
		default:
			releaseSave()
			<-updateDone
			<-applyDone
			t.Fatal("applyAntigravityModelHints stayed blocked on mutation lock after context cancellation")
		}

		if gotEpoch := reg.ClientRegistrationEpoch(auth.ID); gotEpoch != initialEpoch {
			releaseSave()
			<-updateDone
			t.Fatalf("canceled publication advanced registry epoch from %d to %d", initialEpoch, gotEpoch)
		}
		if got := reg.GetModelsForClient(auth.ID); len(got) != 0 {
			releaseSave()
			<-updateDone
			t.Fatalf("canceled publication registered models: %+v", got)
		}

		releaseSave()
		if errUpdate := <-updateDone; errUpdate != nil {
			t.Fatalf("manager.Update failed: %v", errUpdate)
		}
	})
}
