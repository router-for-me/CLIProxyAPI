package store

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter"
)

// The model cache entry must carry a lazily bridged autorouter.Config that
// disappears together with the router on invalidateModelCache, so upserts are
// picked up on the next request without per-request bridging.
func TestAutoRouterModelCacheCarriesConfig(t *testing.T) {
	s := &AutoRouterStore{modelCache: map[string]*cachedAutoRouter{}}
	s.cacheByModel("router:cached", &AutoRouter{
		ID: "pk-1", ModelID: "router:cached", Enabled: true,
		Mappings: []TierMapping{{Tier: "simple", Model: "gpt-4o"}},
	})

	cfg := s.cachedConfigByModel("router:cached")
	if cfg == nil {
		t.Fatal("expected a bridged config from the cache entry")
	}
	if cfg.ID != "pk-1" || len(cfg.Mappings) != 1 {
		t.Fatalf("bridged config malformed: %+v", cfg)
	}

	// Mutation invalidation clears the config along with the router.
	s.invalidateModelCache()
	if again := s.cachedConfigByModel("router:cached"); again != nil {
		t.Fatal("config must not survive invalidateModelCache")
	}
}

// Ensure the bridged config satisfies the pure resolver contract.
func TestBridgedConfigResolves(t *testing.T) {
	s := &AutoRouterStore{modelCache: map[string]*cachedAutoRouter{}}
	s.cacheByModel("router:res", &AutoRouter{
		ID: "pk-2", ModelID: "router:res", Enabled: true,
		Mappings: []TierMapping{{Tier: "complex", Model: "gpt-4o"}},
	})
	cfg := s.cachedConfigByModel("router:res")
	resolved, ok := autorouter.Resolve(autorouter.TierComplex, cfg)
	if !ok || resolved.Model != "gpt-4o" {
		t.Fatalf("Resolve() = %+v, %v; want gpt-4o", resolved, ok)
	}
}
