package pluginhost

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestRegisterExecutorsPreservesNativeCatalogPriority(t *testing.T) {
	for _, tc := range []struct {
		name             string
		nativeExecutor   bool
		extraPluginModel bool
	}{
		{name: "native catalog blocks competing executor", nativeExecutor: true},
		{name: "native catalog only blocks overlapping model", nativeExecutor: true, extraPluginModel: true},
		{name: "catalog without native executor does not reserve model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			modelRegistry := newFakeModelRegistry()
			manager := newFakeExecutorManager()
			native := &fakeProviderExecutor{provider: "native-provider"}
			if tc.nativeExecutor {
				manager.RegisterExecutor(native)
			}
			// The existing credential has not yet registered the new catalog model.
			modelRegistry.RegisterClient("native-auth", "native-provider", []*registry.ModelInfo{{ID: "native-base"}})
			pluginModels := []pluginapi.ModelInfo{{ID: "shared-model"}}
			if tc.extraPluginModel {
				pluginModels = append(pluginModels, pluginapi.ModelInfo{ID: "plugin-only"})
			}
			catalog := capabilityRecord{
				id:       "native-catalog",
				priority: 10,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
					ModelRegistrar: staticModelRegistrar("native-provider", "shared-model"),
				}},
			}
			competitor := capabilityRecord{
				id:       "competing-executor",
				priority: 20,
				plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
					ModelRegistrar: modelRegistrarFunc(func(context.Context, pluginapi.ModelRegistrationRequest) (pluginapi.ModelRegistrationResponse, error) {
						return pluginapi.ModelRegistrationResponse{Provider: "plugin-provider", Models: pluginModels}, nil
					}),
					Executor: &fakeExecutor{identifier: "plugin-provider"},
				}},
			}
			host := newHostWithRecords(competitor, catalog)
			for _, stage := range []struct {
				name         string
				publishModel bool
			}{
				{name: "first load before credential refresh"},
				{name: "reload before credential refresh"},
				{name: "reload after credential refresh", publishModel: true},
				{name: "reload after credential withdraws model"},
			} {
				nativeModels := []*registry.ModelInfo{{ID: "native-base"}}
				if stage.publishModel && tc.nativeExecutor {
					nativeModels = append(nativeModels, &registry.ModelInfo{ID: "shared-model"})
				}
				modelRegistry.RegisterClient("native-auth", "native-provider", nativeModels)
				host.RegisterModels(t.Context(), modelRegistry)
				host.RegisterExecutors(manager, modelRegistry)

				if tc.nativeExecutor {
					if got, _ := manager.Executor("native-provider"); got != native {
						t.Fatalf("%s: native executor was replaced", stage.name)
					}
				}
				wantExecutor := !tc.nativeExecutor || tc.extraPluginModel
				if _, got := manager.Executor("plugin-provider"); got != wantExecutor {
					t.Fatalf("%s: competing executor registered = %t, want %t", stage.name, got, wantExecutor)
				}
				gotModels := host.ModelsForProvider("plugin-provider")
				wantCount := 0
				if !tc.nativeExecutor {
					wantCount++
				}
				if tc.extraPluginModel {
					wantCount++
				}
				if len(gotModels) != wantCount {
					t.Fatalf("%s: competing catalog has %d models, want %d", stage.name, len(gotModels), wantCount)
				}
				for _, model := range gotModels {
					if tc.nativeExecutor && model.ID == "shared-model" {
						t.Fatalf("%s: competing catalog claimed native shared-model", stage.name)
					}
				}
				if models := host.ModelsForProvider("native-provider"); len(models) != 1 || models[0].ID != "shared-model" {
					t.Fatalf("%s: native source catalog was not retained", stage.name)
				}
				if _, published := modelRegistry.clients["plugin:native-catalog:native-provider"]; published {
					t.Fatalf("%s: catalog-only plugin independently published models", stage.name)
				}
				providers := modelRegistry.GetModelProviders("shared-model")
				wantProvider := ""
				if !tc.nativeExecutor {
					wantProvider = "plugin-provider"
				} else if stage.publishModel {
					wantProvider = "native-provider"
				}
				if wantProvider == "" && len(providers) != 0 || wantProvider != "" && (len(providers) != 1 || providers[0] != wantProvider) {
					t.Fatalf("%s: shared-model providers = %v, want %q", stage.name, providers, wantProvider)
				}
			}

			// Removing the catalog releases ownership on the next arbitration pass.
			setHostSnapshotForTest(host, true, competitor)
			host.RegisterModels(t.Context(), modelRegistry)
			host.RegisterExecutors(manager, modelRegistry)
			if _, ok := manager.Executor("plugin-provider"); !ok {
				t.Fatal("competing executor was not registered after native catalog removal")
			}
			if models := host.ModelsForProvider("plugin-provider"); len(models) != len(pluginModels) {
				t.Fatalf("competing models after catalog removal = %d, want %d", len(models), len(pluginModels))
			}
		})
	}
}
