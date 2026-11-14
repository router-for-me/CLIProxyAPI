package cliproxy

import (
	"context"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestRegisterModelsForAuth_CursorCanceledRefreshPreservesCatalogs(t *testing.T) {
	for _, excluded := range []string{"gpt-5-high", "*"} {
		t.Run(excluded, func(t *testing.T) {
			service := &Service{cfg: &config.Config{}}
			auth := &coreauth.Auth{
				ID:         t.Name(),
				Provider:   "cursor",
				Status:     coreauth.StatusActive,
				Attributes: map[string]string{"auth_kind": "oauth"},
			}
			reg := registry.GetGlobalRegistry()
			t.Cleanup(func() {
				reg.UnregisterClient(auth.ID)
				helps.DeleteCursorRoutingModels(auth.ID)
			})

			originalFetch := fetchCursorModelsForRegistration
			t.Cleanup(func() { fetchCursorModelsForRegistration = originalFetch })
			fetchCursorModelsForRegistration = func(context.Context, *coreauth.Auth, *config.Config) []*ModelInfo {
				return []*ModelInfo{{ID: "old-model", Type: "cursor", ContextLength: 32000}}
			}
			service.registerModelsForAuth(context.Background(), auth)
			oldRouting := helps.CursorRoutingModels(auth.ID, nil)
			oldRegistered := reg.GetModelsForClient(auth.ID)
			if len(oldRouting) != 1 || len(oldRegistered) != 1 {
				t.Fatal("initial registration did not populate both catalogs")
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fetched := false
			fetchCursorModelsForRegistration = func(fetchCtx context.Context, _ *coreauth.Auth, _ *config.Config) []*ModelInfo {
				if fetchCtx.Err() != nil {
					t.Fatal("refresh was canceled before fetching models")
				}
				fetched = true
				cancel()
				return []*ModelInfo{{ID: "gpt-5-low"}, {ID: "gpt-5-high"}}
			}
			auth.Attributes["excluded_models"] = excluded
			service.registerModelsForAuth(ctx, auth)

			if !fetched || ctx.Err() != context.Canceled {
				t.Fatal("refresh did not cancel during model fetching")
			}
			if got := helps.CursorRoutingModels(auth.ID, nil); !reflect.DeepEqual(got, oldRouting) {
				t.Fatalf("canceled refresh changed routing catalog: got %#v, want %#v", got, oldRouting)
			}
			if got := reg.GetModelsForClient(auth.ID); !reflect.DeepEqual(got, oldRegistered) {
				t.Fatalf("canceled refresh changed registered models: got %#v, want %#v", got, oldRegistered)
			}
		})
	}
}

func TestRegisterModelsForAuth_CursorRefreshPublishesFilteredCatalogs(t *testing.T) {
	service := &Service{cfg: &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"cursor": {
				{Name: "gpt-5", Alias: "friendly"},
				{Name: "gpt-5-high", Alias: "excluded-alias"},
			},
		},
	}}
	auth := &coreauth.Auth{
		ID:         t.Name(),
		Provider:   "cursor",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth"},
	}
	reg := registry.GetGlobalRegistry()
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
		helps.DeleteCursorRoutingModels(auth.ID)
	})

	originalFetch := fetchCursorModelsForRegistration
	t.Cleanup(func() { fetchCursorModelsForRegistration = originalFetch })
	fetchedModels := []*ModelInfo{{ID: "old-model", Type: "cursor"}}
	fetchCursorModelsForRegistration = func(context.Context, *coreauth.Auth, *config.Config) []*ModelInfo {
		return fetchedModels
	}
	service.registerModelsForAuth(context.Background(), auth)
	if len(reg.GetModelsForClient(auth.ID)) != 1 || len(helps.CursorRoutingModels(auth.ID, nil)) != 1 {
		t.Fatal("initial registration did not populate both catalogs")
	}

	fetchedModels = []*ModelInfo{
		{ID: "gpt-5-low", Type: "cursor", ContextLength: 128000, MaxCompletionTokens: 16000},
		{ID: "gpt-5-high", Type: "cursor"},
	}
	auth.Attributes["excluded_models"] = "gpt-5-high"
	service.registerModelsForAuth(context.Background(), auth)

	if got := helps.CursorRoutingModels(auth.ID, nil); !reflect.DeepEqual(got, fetchedModels[:1]) {
		t.Fatalf("routing catalog = %#v, want only the unaliased, nonexcluded upstream model", got)
	}
	registered := reg.GetModelsForClient(auth.ID)
	if len(registered) != 1 || registered[0].ID != "friendly" {
		t.Fatalf("registered models = %#v, want only the collapsed family alias", registered)
	}
	family := registered[0]
	if family == nil || family.ContextLength != 128000 || family.MaxCompletionTokens != 16000 {
		t.Fatalf("family alias did not preserve representative metadata: %#v", family)
	}
	if family.Thinking == nil || !reflect.DeepEqual(family.Thinking.Levels, []string{"low"}) {
		t.Fatalf("family alias thinking = %#v, want only nonexcluded low effort", family.Thinking)
	}
	if !reflect.DeepEqual(family.SupportedParameters, []string{"reasoning_effort"}) {
		t.Fatalf("family alias supported parameters = %v", family.SupportedParameters)
	}

	replacementModels := fetchedModels
	for _, testCase := range []struct {
		name     string
		models   []*ModelInfo
		excluded string
	}{
		{name: "fully excluded", models: replacementModels, excluded: "*"},
		{name: "empty fetch"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fetchedModels = replacementModels
			auth.Attributes["excluded_models"] = "gpt-5-high"
			service.registerModelsForAuth(context.Background(), auth)

			fetchedModels = testCase.models
			auth.Attributes["excluded_models"] = testCase.excluded
			service.registerModelsForAuth(context.Background(), auth)
			if got := helps.CursorRoutingModels(auth.ID, replacementModels); len(got) != 0 {
				t.Fatalf("empty refresh retained routing models: %#v", got)
			}
			if got := reg.GetModelsForClient(auth.ID); len(got) != 0 {
				t.Fatalf("empty refresh retained registered models: %#v", got)
			}
		})
	}
}

func TestService_CursorAuthDisableAndRemovalClearsRoutingCatalog(t *testing.T) {
	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:         t.Name(),
		Provider:   "cursor",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"auth_kind": "oauth"},
	}
	reg := registry.GetGlobalRegistry()
	t.Cleanup(func() {
		reg.UnregisterClient(auth.ID)
		helps.DeleteCursorRoutingModels(auth.ID)
	})

	originalFetch := fetchCursorModelsForRegistration
	t.Cleanup(func() { fetchCursorModelsForRegistration = originalFetch })
	fetchCursorModelsForRegistration = func(context.Context, *coreauth.Auth, *config.Config) []*ModelInfo {
		return []*ModelInfo{{ID: "claude-fable-5-1", Type: "cursor"}}
	}

	fallback := []*registry.ModelInfo{{ID: "unfiltered-fallback"}}

	// 1. Initial active registration: stores routing models
	service.registerModelsForAuth(context.Background(), auth)
	if got := helps.CursorRoutingModels(auth.ID, fallback); len(got) != 1 || got[0].ID != "claude-fable-5-1" {
		t.Fatalf("expected registered model, got: %#v", got)
	}

	// 2. Disabling auth in service clears routing cache and restores fallback
	auth.Disabled = true
	service.registerModelsForAuth(context.Background(), auth)
	if got := helps.CursorRoutingModels(auth.ID, fallback); len(got) != 1 || got[0].ID != "unfiltered-fallback" {
		t.Fatalf("disabled auth retained routing cache, got: %#v", got)
	}

	// 3. Re-enable: restores routing models
	auth.Disabled = false
	service.registerModelsForAuth(context.Background(), auth)
	if got := helps.CursorRoutingModels(auth.ID, fallback); len(got) != 1 || got[0].ID != "claude-fable-5-1" {
		t.Fatalf("re-enabled auth failed to store routing models: %#v", got)
	}

	// 4. Auth removal via applyCoreAuthRemoval cleans routing cache and restores fallback
	service.coreManager = coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	_, _ = service.coreManager.Register(context.Background(), auth)
	service.applyCoreAuthRemoval(context.Background(), auth.ID)
	if got := helps.CursorRoutingModels(auth.ID, fallback); len(got) != 1 || got[0].ID != "unfiltered-fallback" {
		t.Fatalf("removed auth retained routing cache, got: %#v", got)
	}
}

func TestService_PrepareCoreAuthForModelRegistration_UpdateErrorWithDisabledAuthClearsRoutingCatalog(t *testing.T) {
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil),
	}
	authID := t.Name()
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authID)
		helps.DeleteCursorRoutingModels(authID)
	})

	// 1. Seed a disabled auth in coreManager
	disabledAuth := &coreauth.Auth{
		ID:       authID,
		Provider: "cursor",
		Disabled: true,
		Status:   coreauth.StatusDisabled,
	}
	if _, err := service.coreManager.Register(context.Background(), disabledAuth); err != nil {
		t.Fatalf("Register error: %v", err)
	}

	// 2. Seed the Cursor routing catalog for this auth
	helps.StoreCursorRoutingModels(authID, []*registry.ModelInfo{{ID: "claude-fable-5-1"}})
	fallback := []*registry.ModelInfo{{ID: "fallback-model"}}

	// Verify the catalog is currently populated
	if got := helps.CursorRoutingModels(authID, fallback); len(got) != 1 || got[0].ID != "claude-fable-5-1" {
		t.Fatalf("expected seeded model, got: %#v", got)
	}

	// 3. Pass an incoming auth with invalid weight so Update fails
	incoming := &coreauth.Auth{
		ID:       authID,
		Provider: "cursor",
		Attributes: map[string]string{
			coreauth.AttributeWeight: "invalid-not-a-number",
		},
	}

	// 4. Call prepareCoreAuthForModelRegistration: Update fails, detects current.Disabled,
	// unregisters client, calls DeleteCursorRoutingModels, and returns nil
	result := service.prepareCoreAuthForModelRegistration(context.Background(), incoming)
	if result != nil {
		t.Fatalf("expected nil from failed prepareCoreAuth, got: %#v", result)
	}

	// 5. Verify routing catalog was deleted and fallback is restored
	if got := helps.CursorRoutingModels(authID, fallback); len(got) != 1 || got[0].ID != "fallback-model" {
		t.Fatalf("expected fallback after prepareCoreAuth failure, got: %#v", got)
	}
}
