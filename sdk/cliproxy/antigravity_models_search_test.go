package cliproxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const (
	searchFixtureLegacy = "fixture-search-legacy"
	searchFixtureNative = "fixture-search-native"
	searchFixtureVeto   = "fixture-search-veto"
	searchFixtureNone   = "fixture-search-none"
)

// antigravitySearchFixtureCatalog covers every combination of the two catalog
// search fields without depending on production models.json entries.
func antigravitySearchFixtureCatalog() []*ModelInfo {
	enabled, disabled := true, false
	return []*ModelInfo{
		{ID: searchFixtureLegacy, Object: "model", SupportsWebSearch: true},
		{ID: searchFixtureNative, Object: "model", NativeCapabilities: &registry.NativeCapabilities{WebSearch: &enabled}},
		{ID: searchFixtureVeto, Object: "model", SupportsWebSearch: true, NativeCapabilities: &registry.NativeCapabilities{WebSearch: &disabled}},
		{ID: searchFixtureNone, Object: "model"},
	}
}

func searchFixtureWant(modelID string) bool {
	return modelID == searchFixtureLegacy || modelID == searchFixtureNative
}

// A valid catalog must publish normally; fencing tests use the same response shape.
func TestAntigravityAsyncProbe_ValidCatalogPublishesWithoutSearchHints(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":{"` + searchFixtureNative + `":{}}}`))
	}))
	defer server.Close()
	svc := &Service{cfg: &config.Config{}, antigravityCatalog: antigravitySearchFixtureCatalog}
	auth := antigravityTestAuth("valid-catalog-no-search", server.URL)
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
	svc.registerModelsForAuth(t.Context(), auth)
	svc.WaitAntigravityProbes()
	models := GlobalModelRegistry().GetModelsForClient(auth.ID)
	if len(models) != 1 || models[0].ID != searchFixtureNative || !registry.AntigravityModelSupportsWebSearch(models[0]) {
		t.Fatalf("valid catalog did not publish with static search capability: %+v", models)
	}
}

func TestAntigravitySearchCapabilitiesComeFromCatalog(t *testing.T) {
	aliases := []config.OAuthModelAlias{
		{Name: searchFixtureLegacy, Alias: "alias-legacy"},
		{Name: searchFixtureNative, Alias: "alias-native"},
		{Name: searchFixtureVeto, Alias: "alias-veto"},
		{Name: searchFixtureNone, Alias: "alias-none"},
	}
	svc := &Service{
		cfg:                &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{"antigravity": aliases}},
		antigravityCatalog: antigravitySearchFixtureCatalog,
	}
	auth := antigravityTestAuth("catalog-search", "http://127.0.0.1:1")
	auth.Prefix = "tenant"
	aliasTargets := map[string]string{}
	for _, alias := range aliases {
		aliasTargets[alias.Alias] = alias.Name
		aliasTargets["tenant/"+alias.Alias] = alias.Name
	}
	const entitled = `"fixture-search-legacy":{},"fixture-search-native":{},"fixture-search-veto":{},"fixture-search-none":{}`
	for _, tc := range []struct{ name, body string }{
		{"absent", `{"models":{` + entitled + `}}`},
		{"empty", `{"models":{` + entitled + `},"webSearchModelIds":[]}`},
		{"contradictory", `{"models":{` + entitled + `},"webSearchModelIds":["fixture-search-none","fixture-search-veto"]}`},
		{"malformed-ignored-field", `{"models":{` + entitled + `},"webSearchModelIds":42}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hints, ok := parseAntigravityModelCapabilityHints([]byte(tc.body))
			if !ok || len(hints.ModelIDs) != 4 {
				t.Fatalf("entitlements not parsed: %+v", hints)
			}
			models := svc.antigravityModelsForHints(auth, hints)
			if len(models) != 8 {
				t.Fatalf("expected aliases and prefixes for every fixture, got %+v", models)
			}
			for _, model := range models {
				target, ok := aliasTargets[model.ID]
				if !ok {
					t.Fatalf("unexpected model %q", model.ID)
				}
				want := searchFixtureWant(target)
				if got := registry.AntigravityModelSupportsWebSearch(model); got != want {
					t.Fatalf("%s search=%v, want catalog value %v (supports=%v native=%+v)", model.ID, got, want, model.SupportsWebSearch, model.NativeCapabilities)
				}
			}
		})
	}
	hints, ok := parseAntigravityModelCapabilityHints([]byte(`{"webSearchModelIds":["fixture-search-none"]}`))
	if !ok || hints.ModelIDs != nil {
		t.Fatal("search-only response granted model entitlements")
	}
}
