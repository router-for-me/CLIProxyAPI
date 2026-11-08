package registry

import (
	"fmt"
	"sync"
	"testing"
)

// referenceHasClientSupportingWebSearch recomputes the web-search aggregate with
// the original full scan. The index must agree with it in every state the
// registry can reach, so tests compare the fast path against this oracle
// instead of only asserting hand-picked expectations.
func referenceHasClientSupportingWebSearch(r *ModelRegistry, modelID, provider, excludeClientID string) bool {
	for clientID, infos := range r.clientModelInfos {
		if clientID == excludeClientID {
			continue
		}
		if provider != "" && r.clientProviders[clientID] != provider {
			continue
		}
		info := infos[modelID]
		if info != nil && info.SupportsWebSearch {
			return true
		}
	}
	return false
}

// assertWebSearchIndexMatchesScan verifies the incremental index against the
// full scan for every client/model/provider combination currently registered.
func assertWebSearchIndexMatchesScan(t *testing.T, r *ModelRegistry, context string) {
	t.Helper()
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	providers := make(map[string]struct{})
	modelIDs := make(map[string]struct{})
	for clientID, infos := range r.clientModelInfos {
		providers[r.clientProviders[clientID]] = struct{}{}
		for modelID := range infos {
			modelIDs[modelID] = struct{}{}
		}
	}

	for modelID := range modelIDs {
		for provider := range providers {
			for _, exclude := range []string{"", "__none__"} {
				want := referenceHasClientSupportingWebSearch(r, modelID, provider, exclude)
				got := r.hasClientSupportingWebSearchLocked(modelID, provider, exclude)
				if got != want {
					t.Fatalf("%s: model=%q provider=%q exclude=%q: index=%v scan=%v", context, modelID, provider, exclude, got, want)
				}
			}
		}
		// Cross-check per-client exclusion, which the index answers by
		// subtracting the excluded client instead of skipping it.
		for clientID := range r.clientModelInfos {
			want := referenceHasClientSupportingWebSearch(r, modelID, "", clientID)
			got := r.hasClientSupportingWebSearchLocked(modelID, "", clientID)
			if got != want {
				t.Fatalf("%s: model=%q exclude=%q: index=%v scan=%v", context, modelID, clientID, got, want)
			}
		}

		// The published Info flags are what callers actually read, so verify
		// them against the scan too: a self-consistent index would still be a
		// bug if the stored aggregate had been computed from a stale one.
		registration := r.models[modelID]
		if registration == nil || registration.Info == nil {
			continue
		}
		if want, got := referenceHasClientSupportingWebSearch(r, modelID, "", ""), registration.Info.SupportsWebSearch; got != want {
			t.Fatalf("%s: model=%q Info.SupportsWebSearch=%v scan=%v", context, modelID, got, want)
		}
		for provider, info := range registration.InfoByProvider {
			if info == nil {
				continue
			}
			if want, got := referenceHasClientSupportingWebSearch(r, modelID, provider, ""), info.SupportsWebSearch; got != want {
				t.Fatalf("%s: model=%q provider=%q InfoByProvider.SupportsWebSearch=%v scan=%v", context, modelID, provider, got, want)
			}
		}
	}
}

func newWebSearchIndexRegistry() *ModelRegistry {
	reg := newTestModelRegistry()
	reg.RegisterClient("a", "codex", []*ModelInfo{
		{ID: "solo", SupportsWebSearch: true},
		{ID: "shared", SupportsWebSearch: true},
	})
	reg.RegisterClient("b", "codex", []*ModelInfo{
		{ID: "shared", SupportsWebSearch: true},
	})
	reg.RegisterClient("c", "gemini", []*ModelInfo{
		{ID: "shared", SupportsWebSearch: true},
		{ID: "off"},
	})
	return reg
}

func TestWebSearchIndexMatchesScanAcrossMutations(t *testing.T) {
	reg := newWebSearchIndexRegistry()
	assertWebSearchIndexMatchesScan(t, reg, "initial")

	// Re-registration may add or drop contributions; both paths must retract
	// the old counts before writing the new ones.
	reg.RegisterClient("b", "codex", []*ModelInfo{{ID: "shared"}})
	assertWebSearchIndexMatchesScan(t, reg, "re-register drops web search")

	reg.RegisterClient("b", "codex", []*ModelInfo{{ID: "shared", SupportsWebSearch: true}})
	assertWebSearchIndexMatchesScan(t, reg, "re-register restores web search")

	reg.RegisterClient("d", "codex", []*ModelInfo{{ID: "solo", SupportsWebSearch: true}})
	assertWebSearchIndexMatchesScan(t, reg, "second supporter of solo")

	reg.ApplyClientModelCapabilities("d", reg.ClientRegistrationEpoch("d"), func(_ string, info *ModelInfo) {
		info.SupportsWebSearch = false
	})
	assertWebSearchIndexMatchesScan(t, reg, "capabilities clear web search")

	reg.ApplyClientModelCapabilities("a", reg.ClientRegistrationEpoch("a"), func(_ string, info *ModelInfo) {
		info.SupportsWebSearch = true
	})
	assertWebSearchIndexMatchesScan(t, reg, "capabilities set web search")

	reg.UnregisterClient("a")
	assertWebSearchIndexMatchesScan(t, reg, "unregister sole supporter")

	reg.UnregisterClient("d")
	reg.UnregisterClient("b")
	reg.UnregisterClient("c")
	assertWebSearchIndexMatchesScan(t, reg, "unregister all")
}

func TestWebSearchIndexDropsOrphanCountsOnPureAddition(t *testing.T) {
	// The pure-addition path assumes the client has no prior bookkeeping, but
	// clientModelInfos can outlive clientModels (any code path that rewrites one
	// without the other). A stale entry still contributes to the index, so
	// registering the client again must retract it instead of counting it twice.
	reg := newTestModelRegistry()
	reg.mutex.Lock()
	reg.clientModelInfos["ghost"] = map[string]*ModelInfo{
		"orphaned": {ID: "orphaned", SupportsWebSearch: true},
	}
	reg.clientProviders["ghost"] = "codex"
	// The orphan is seeded in the shape any buggy path would leave behind: the
	// index still agrees with clientModelInfos, but clientModels no longer
	// carries the client, so the next registration takes the pure-addition path.
	reg.incrementWebSearchCountLocked("orphaned", "codex")
	reg.mutex.Unlock()
	assertWebSearchIndexMatchesScan(t, reg, "orphan seeded")

	// Registering the same client with a catalog that advertises no web search
	// must leave a clean index, not the orphaned contribution plus nothing.
	reg.RegisterClient("ghost", "codex", []*ModelInfo{{ID: "other"}})
	assertWebSearchIndexMatchesScan(t, reg, "orphan replaced by pure addition")
	if reg.hasClientSupportingWebSearchLocked("orphaned", "", "") {
		t.Fatalf("index kept the orphaned web-search contribution after re-registration")
	}
	if reg.hasClientSupportingWebSearchLocked("orphaned", "codex", "") {
		t.Fatalf("provider index kept the orphaned web-search contribution after re-registration")
	}
}

func assertPublishedWebSearchSupport(t *testing.T, reg *ModelRegistry, modelID, provider string, want bool) {
	t.Helper()
	for _, view := range []string{"", provider} {
		info := reg.GetModelInfo(modelID, view)
		if info == nil {
			t.Errorf("GetModelInfo(%q, %q) returned nil", modelID, view)
			continue
		}
		if info.SupportsWebSearch != want {
			t.Errorf("GetModelInfo(%q, %q).SupportsWebSearch = %v, want %v", modelID, view, info.SupportsWebSearch, want)
		}
	}
}

func TestWebSearchIndexEmptyClientExclusion(t *testing.T) {
	for _, clientID := range []string{"", "supporter"} {
		t.Run(fmt.Sprintf("client=%q", clientID), func(t *testing.T) {
			reg := newTestModelRegistry()
			reg.RegisterClient(clientID, "antigravity", []*ModelInfo{{ID: "m", SupportsWebSearch: true}})
			reg.RegisterClient("observer", "antigravity", []*ModelInfo{{ID: "m"}})

			reg.mutex.RLock()
			defer reg.mutex.RUnlock()
			for _, provider := range []string{"", "antigravity", "gemini"} {
				for _, exclude := range []string{"", "supporter", "observer", "missing"} {
					want := provider != "gemini" && clientID != exclude
					if got := referenceHasClientSupportingWebSearch(reg, "m", provider, exclude); got != want {
						t.Errorf("provider=%q exclude=%q: scan=%v, want %v", provider, exclude, got, want)
					}
					if got := reg.hasClientSupportingWebSearchLocked("m", provider, exclude); got != want {
						t.Errorf("provider=%q exclude=%q: index=%v, want %v", provider, exclude, got, want)
					}
				}
			}
		})
	}
}

func TestWebSearchIndexPublishedViewsAfterSupporterUnregister(t *testing.T) {
	for _, clientID := range []string{"", "supporter"} {
		t.Run(fmt.Sprintf("client=%q", clientID), func(t *testing.T) {
			reg := newTestModelRegistry()
			reg.RegisterClient(clientID, "antigravity", []*ModelInfo{{ID: "m", SupportsWebSearch: true}})
			reg.RegisterClient("observer", "antigravity", []*ModelInfo{{ID: "m"}})
			assertPublishedWebSearchSupport(t, reg, "m", "antigravity", true)

			reg.UnregisterClient(clientID)
			assertPublishedWebSearchSupport(t, reg, "m", "antigravity", false)
			assertWebSearchIndexMatchesScan(t, reg, "supporter unregistered")
		})
	}
}

func TestWebSearchIndexPreservesRealSupporterOnOrphanAddition(t *testing.T) {
	reg := newTestModelRegistry()
	reg.RegisterClient("real", "antigravity", []*ModelInfo{{ID: "m", SupportsWebSearch: true}})
	reg.RegisterClient("ghost", "antigravity", []*ModelInfo{{ID: "m", SupportsWebSearch: true}})
	// Artificially leave orphaned metadata and index entries. Normal registry
	// operations keep these maps together; this exercises defensive cleanup.
	reg.mutex.Lock()
	delete(reg.clientModels, "ghost")
	reg.mutex.Unlock()
	assertWebSearchIndexMatchesScan(t, reg, "orphan seeded beside real supporter")

	reg.RegisterClient("ghost", "antigravity", []*ModelInfo{{ID: "m"}})
	assertPublishedWebSearchSupport(t, reg, "m", "antigravity", true)
	assertWebSearchIndexMatchesScan(t, reg, "orphan replaced beside real supporter")
}

func TestWebSearchIndexRefreshesOrphanedModelsOnPureAddition(t *testing.T) {
	for _, realSupportsWebSearch := range []bool{false, true} {
		t.Run(fmt.Sprintf("real_supports_web_search=%v", realSupportsWebSearch), func(t *testing.T) {
			reg := newTestModelRegistry()
			reg.RegisterClient("real", "antigravity", []*ModelInfo{{ID: "m", SupportsWebSearch: realSupportsWebSearch}})
			reg.RegisterClient("ghost", "antigravity", []*ModelInfo{{ID: "m", SupportsWebSearch: true}})
			// Artificially orphan the old catalog to exercise defensive cleanup.
			reg.mutex.Lock()
			delete(reg.clientModels, "ghost")
			reg.mutex.Unlock()
			assertPublishedWebSearchSupport(t, reg, "m", "antigravity", true)
			assertWebSearchIndexMatchesScan(t, reg, "orphan seeded before catalog replacement")

			// The old model is absent from the new catalog, so additions cannot
			// refresh its published capability as a side effect.
			reg.RegisterClient("ghost", "antigravity", []*ModelInfo{{ID: "n"}})
			assertPublishedWebSearchSupport(t, reg, "m", "antigravity", realSupportsWebSearch)
			assertPublishedWebSearchSupport(t, reg, "n", "antigravity", false)
			assertWebSearchIndexMatchesScan(t, reg, "orphan replaced with a different catalog")
		})
	}
}

func TestWebSearchIndexCleansOrphanOnUnregister(t *testing.T) {
	for _, realSupportsWebSearch := range []bool{false, true} {
		t.Run(fmt.Sprintf("real_supports_web_search=%v", realSupportsWebSearch), func(t *testing.T) {
			reg := newTestModelRegistry()
			reg.RegisterClient("real", "antigravity", []*ModelInfo{{ID: "m", SupportsWebSearch: realSupportsWebSearch}})
			reg.RegisterClient("ghost", "antigravity", []*ModelInfo{{ID: "m", SupportsWebSearch: true}})
			// This inconsistent state is constructed only to test orphan cleanup.
			reg.mutex.Lock()
			delete(reg.clientModels, "ghost")
			reg.mutex.Unlock()

			reg.UnregisterClient("ghost")
			reg.mutex.RLock()
			if _, exists := reg.clientModelInfos["ghost"]; exists {
				t.Error("unregister left orphaned client model metadata")
			}
			reg.mutex.RUnlock()
			assertPublishedWebSearchSupport(t, reg, "m", "antigravity", realSupportsWebSearch)

			// Repeated cleanup must not retract the real client's contribution.
			reg.UnregisterClient("ghost")
			reg.mutex.RLock()
			wantCount := 0
			if realSupportsWebSearch {
				wantCount = 1
			}
			if got := reg.webSearchClientCounts["m"]; got != wantCount {
				t.Errorf("global count after repeated orphan cleanup = %d, want %d", got, wantCount)
			}
			if got := reg.webSearchProviderCounts["m"]["antigravity"]; got != wantCount {
				t.Errorf("provider count after repeated orphan cleanup = %d, want %d", got, wantCount)
			}
			reg.mutex.RUnlock()
			assertWebSearchIndexMatchesScan(t, reg, "orphan unregistered twice")

			reg.RegisterClient("ghost", "antigravity", []*ModelInfo{{ID: "m"}})
			assertPublishedWebSearchSupport(t, reg, "m", "antigravity", realSupportsWebSearch)
			assertWebSearchIndexMatchesScan(t, reg, "orphan re-registered after cleanup")
		})
	}
}

func TestWebSearchIndexTracksSoleSupporterAfterUnregister(t *testing.T) {
	reg := newWebSearchIndexRegistry()
	if !reg.hasClientSupportingWebSearchLocked("solo", "", "") {
		t.Fatalf("expected solo to be supported before its only supporter left")
	}
	reg.UnregisterClient("a")
	if reg.hasClientSupportingWebSearchLocked("solo", "", "") {
		t.Fatalf("sole supporter left, but the aggregate still reports web search")
	}
	// The published flag must follow the index, otherwise callers keep
	// advertising web search for a model no client can serve.
	if reg.models["solo"] != nil && reg.models["solo"].Info != nil && reg.models["solo"].Info.SupportsWebSearch {
		t.Fatalf("published Info.SupportsWebSearch stayed true after the sole supporter left")
	}
}

func TestWebSearchIndexToleratesNilIndexMaps(t *testing.T) {
	// The index maps are nil in a partially constructed registry (tests and any
	// future literal). The helpers must lazily initialize instead of panicking
	// on assignment to a nil map.
	reg := &ModelRegistry{mutex: &sync.RWMutex{}}
	if reg.hasClientSupportingWebSearchLocked("m", "", "") {
		t.Fatalf("registry without index must not report web-search support")
	}
	reg.incrementWebSearchCountLocked("m", "codex")
	if !reg.hasClientSupportingWebSearchLocked("m", "codex", "") {
		t.Fatalf("expected the increment to be visible after lazy initialization")
	}
	reg.decrementWebSearchCountLocked("m", "codex")
	if reg.hasClientSupportingWebSearchLocked("m", "codex", "") {
		t.Fatalf("expected the decrement to clear the count")
	}
}

func TestWebSearchIndexProviderChangeRetractsOldProvider(t *testing.T) {
	// Re-registering the same client under a different provider must retract
	// the old provider's contribution. The old-provider aggregate is recomputed
	// while this client's counts are still indexed, so a wrong drop/add ordering
	// leaves a phantom count for the provider the client no longer serves.
	reg := newTestModelRegistry()
	reg.RegisterClient("x", "codex", []*ModelInfo{
		{ID: "m", SupportsWebSearch: true},
	})
	if !reg.hasClientSupportingWebSearchLocked("m", "codex", "") {
		t.Fatalf("expected codex to support web search before the provider change")
	}

	reg.RegisterClient("x", "gemini", []*ModelInfo{
		{ID: "m", SupportsWebSearch: true},
	})
	if reg.hasClientSupportingWebSearchLocked("m", "codex", "") {
		t.Fatalf("codex must no longer report web search after the client moved to gemini")
	}
	if !reg.hasClientSupportingWebSearchLocked("m", "gemini", "") {
		t.Fatalf("gemini must report web search after the provider change")
	}
	if got := reg.webSearchProviderCounts["m"]["codex"]; got != 0 {
		t.Fatalf("stale codex provider count = %d, want 0", got)
	}
	if got := reg.webSearchProviderCounts["m"]["gemini"]; got != 1 {
		t.Fatalf("gemini provider count = %d, want 1", got)
	}
	assertWebSearchIndexMatchesScan(t, reg, "provider change")
}

// BenchmarkRegisterClientWebSearchAggregate measures whole-registry
// registration, the hot path that blocked startup: every registered model
// recomputes the web-search aggregate while the registry write lock is held.
// Sizes are reported separately because the pre-fix implementation is quadratic
// in the client count and only the growth curve exposes that.
func BenchmarkRegisterClientWebSearchAggregate(b *testing.B) {
	const modelsPerClient = 20

	infos := make([]*ModelInfo, 0, modelsPerClient)
	for i := 0; i < modelsPerClient; i++ {
		// Only the first model advertises web search, and it must be set on
		// SupportsWebSearch: the aggregate is keyed off that field, not off
		// NativeCapabilities.WebSearch. Leaving it false makes every lookup miss
		// the client set, which silently benchmarks a different code path.
		infos = append(infos, &ModelInfo{
			ID:                fmt.Sprintf("model-%d", i),
			SupportsWebSearch: i == 0,
		})
	}

	for _, clients := range []int{250, 500, 1000} {
		b.Run(fmt.Sprintf("clients=%d", clients), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				reg := newTestModelRegistry()
				for c := 0; c < clients; c++ {
					reg.RegisterClient(fmt.Sprintf("client-%d", c), "codex", infos)
				}
			}
		})
	}
}
