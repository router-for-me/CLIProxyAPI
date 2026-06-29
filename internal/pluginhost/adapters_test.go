package pluginhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestPluginModelInfoToRegistryModelInfoClonesThinkingAndSlices(t *testing.T) {
	model := pluginapi.ModelInfo{
		ID:                         "model-1",
		Object:                     "model",
		Created:                    123,
		OwnedBy:                    "owner",
		Type:                       "plugin",
		DisplayName:                "Model One",
		Name:                       "provider-model",
		Version:                    "v1",
		Description:                "desc",
		InputTokenLimit:            100,
		OutputTokenLimit:           200,
		SupportedGenerationMethods: []string{"generate"},
		ContextLength:              300,
		MaxCompletionTokens:        400,
		SupportedParameters:        []string{"temperature"},
		SupportedInputModalities:   []string{"text"},
		SupportedOutputModalities:  []string{"image"},
		Thinking: &pluginapi.ThinkingSupport{
			Min:            1,
			Max:            2,
			ZeroAllowed:    true,
			DynamicAllowed: true,
			Levels:         []string{"low", "high"},
		},
		UserDefined: true,
	}

	got := pluginModelInfoToRegistryModelInfo(model)
	if got.ID != model.ID || got.Object != model.Object || got.Created != model.Created || got.OwnedBy != model.OwnedBy || got.Type != model.Type ||
		got.DisplayName != model.DisplayName || got.Name != model.Name || got.Version != model.Version || got.Description != model.Description ||
		got.InputTokenLimit != int(model.InputTokenLimit) || got.OutputTokenLimit != int(model.OutputTokenLimit) ||
		got.ContextLength != int(model.ContextLength) || got.MaxCompletionTokens != int(model.MaxCompletionTokens) || !got.UserDefined {
		t.Fatalf("converted model = %#v, want fields copied from %#v", got, model)
	}
	if got.Thinking == nil {
		t.Fatal("Thinking = nil, want converted thinking support")
	}
	if got.Thinking.Min != 1 || got.Thinking.Max != 2 || !got.Thinking.ZeroAllowed || !got.Thinking.DynamicAllowed || fmt.Sprint(got.Thinking.Levels) != "[low high]" {
		t.Fatalf("Thinking = %#v, want copied thinking support", got.Thinking)
	}

	model.SupportedGenerationMethods[0] = "mutated"
	model.SupportedParameters[0] = "mutated"
	model.SupportedInputModalities[0] = "mutated"
	model.SupportedOutputModalities[0] = "mutated"
	model.Thinking.Levels[0] = "mutated"
	if got.SupportedGenerationMethods[0] != "generate" || got.SupportedParameters[0] != "temperature" ||
		got.SupportedInputModalities[0] != "text" || got.SupportedOutputModalities[0] != "image" ||
		got.Thinking.Levels[0] != "low" {
		t.Fatalf("converted model kept aliases to plugin slices: %#v", got)
	}
}

func TestExecutorNativeStreamResponseTranslatorExistsRequiresStreamTransform(t *testing.T) {
	outputFormat := sdktranslator.Format("plugin-output-non-stream-only")
	requestedFormat := sdktranslator.Format("client-output-non-stream-only")
	sdktranslator.Register(requestedFormat, outputFormat, nil, sdktranslator.ResponseTransform{
		NonStream: func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
			return rawJSON
		},
	})

	if executorNativeStreamResponseTranslatorExists(outputFormat, requestedFormat) {
		t.Fatal("non-stream-only response transformer was accepted for stream executor output")
	}

	streamOutputFormat := sdktranslator.Format("plugin-output-stream")
	streamRequestedFormat := sdktranslator.Format("client-output-stream")
	sdktranslator.Register(streamRequestedFormat, streamOutputFormat, nil, sdktranslator.ResponseTransform{
		Stream: func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) [][]byte {
			return [][]byte{rawJSON}
		},
	})

	if !executorNativeStreamResponseTranslatorExists(streamOutputFormat, streamRequestedFormat) {
		t.Fatal("stream response transformer was not accepted for stream executor output")
	}
}

func TestRegisterModelsRegistersProviderModelsAndClientID(t *testing.T) {
	modelRegistry := newFakeModelRegistry()
	host := newHostWithRecords(capabilityRecord{
		id:   "alpha",
		meta: pluginapi.Metadata{Name: "Alpha", Version: "1.0.0"},
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ModelRegistrar: modelRegistrarFunc(func(ctx context.Context, req pluginapi.ModelRegistrationRequest) (pluginapi.ModelRegistrationResponse, error) {
				if req.Plugin.Name != "Alpha" || req.Plugin.Version != "1.0.0" {
					t.Fatalf("RegisterModels request plugin = %#v, want Alpha metadata", req.Plugin)
				}
				return pluginapi.ModelRegistrationResponse{
					Provider: "  MixedProvider  ",
					Models: []pluginapi.ModelInfo{{
						ID:                         " model-1 ",
						Object:                     "model",
						Created:                    123,
						OwnedBy:                    "owner",
						Type:                       "chat",
						DisplayName:                "Model One",
						Name:                       "native-model-1",
						Version:                    "v1",
						Description:                "description",
						InputTokenLimit:            100,
						OutputTokenLimit:           200,
						SupportedGenerationMethods: []string{"generate"},
						ContextLength:              300,
						MaxCompletionTokens:        400,
						SupportedParameters:        []string{"temperature"},
						SupportedInputModalities:   []string{"text"},
						SupportedOutputModalities:  []string{"text"},
						Thinking: &pluginapi.ThinkingSupport{
							Min:            1,
							Max:            2,
							ZeroAllowed:    true,
							DynamicAllowed: true,
							Levels:         []string{"low"},
						},
						UserDefined: true,
					}},
				}, nil
			}),
		}},
	})

	host.RegisterModels(context.Background(), modelRegistry)

	reg := modelRegistry.clients["plugin:alpha:mixedprovider"]
	if reg == nil {
		t.Fatal("plugin:alpha:mixedprovider was not registered")
	}
	if reg.provider != "mixedprovider" {
		t.Fatalf("registered provider = %q, want mixedprovider", reg.provider)
	}
	if len(reg.models) != 1 {
		t.Fatalf("registered model count = %d, want 1", len(reg.models))
	}
	model := reg.models[0]
	if model.ID != "model-1" || model.Object != "model" || model.Created != 123 || model.OwnedBy != "owner" || model.Type != "chat" ||
		model.DisplayName != "Model One" || model.Name != "native-model-1" || model.Version != "v1" || model.Description != "description" ||
		model.InputTokenLimit != 100 || model.OutputTokenLimit != 200 || model.ContextLength != 300 || model.MaxCompletionTokens != 400 ||
		model.SupportedGenerationMethods[0] != "generate" || model.SupportedParameters[0] != "temperature" ||
		model.SupportedInputModalities[0] != "text" || model.SupportedOutputModalities[0] != "text" || !model.UserDefined {
		t.Fatalf("registered model = %#v, want converted fields", model)
	}
	if model.Thinking == nil || model.Thinking.Min != 1 || model.Thinking.Max != 2 || !model.Thinking.ZeroAllowed ||
		!model.Thinking.DynamicAllowed || model.Thinking.Levels[0] != "low" {
		t.Fatalf("registered thinking = %#v, want converted thinking", model.Thinking)
	}
}

func TestRegisterModelsUsesModelProviderStaticModels(t *testing.T) {
	modelRegistry := newFakeModelRegistry()
	called := false
	host := newHostWithRecords(capabilityRecord{
		id:   "alpha",
		meta: pluginapi.Metadata{Name: "Alpha", Version: "1.0.0"},
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ModelProvider: modelProviderFunc{
				staticModels: func(ctx context.Context, req pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
					called = true
					if req.Plugin.Name != "Alpha" || req.Plugin.Version != "1.0.0" {
						t.Fatalf("StaticModels request plugin = %#v, want Alpha metadata", req.Plugin)
					}
					if req.Host.AuthDir != "/tmp/plugin-auth" || req.Host.ProxyURL != "http://proxy.local" || !req.Host.ForceModelPrefix {
						t.Fatalf("StaticModels host = %#v, want configured summary", req.Host)
					}
					if len(req.Host.OAuthModelAlias["plugin-provider"]) != 1 || req.Host.OAuthModelAlias["plugin-provider"][0].Alias != "alias-model" {
						t.Fatalf("StaticModels OAuthModelAlias = %#v, want configured alias", req.Host.OAuthModelAlias)
					}
					if len(req.Host.ExcludedModels["plugin-provider"]) != 1 || req.Host.ExcludedModels["plugin-provider"][0] != "hidden-model" {
						t.Fatalf("StaticModels ExcludedModels = %#v, want configured exclusion", req.Host.ExcludedModels)
					}
					return pluginapi.ModelResponse{
						Provider: "  Plugin-Provider  ",
						Models: []pluginapi.ModelInfo{{
							ID:          " model-static ",
							Object:      "model",
							DisplayName: "Static Model",
						}},
					}, nil
				},
			},
			ModelRegistrar: staticModelRegistrar("legacy-provider", "legacy-model"),
		}},
	})
	host.runtimeConfig = &config.Config{
		SDKConfig: config.SDKConfig{
			ProxyURL:         "http://proxy.local",
			ForceModelPrefix: true,
		},
		AuthDir: "/tmp/plugin-auth",
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"plugin-provider": []config.OAuthModelAlias{{Name: "upstream-model", Alias: "alias-model"}},
		},
		OAuthExcludedModels: map[string][]string{
			"plugin-provider": []string{"hidden-model"},
		},
	}

	host.RegisterModels(context.Background(), modelRegistry)

	if !called {
		t.Fatal("ModelProvider.StaticModels was not called")
	}
	reg := modelRegistry.clients["plugin:alpha:plugin-provider"]
	if reg == nil {
		t.Fatal("plugin:alpha:plugin-provider was not registered")
	}
	if reg.provider != "plugin-provider" {
		t.Fatalf("registered provider = %q, want plugin-provider", reg.provider)
	}
	if len(reg.models) != 1 || reg.models[0].ID != "model-static" || reg.models[0].DisplayName != "Static Model" {
		t.Fatalf("registered models = %#v, want static model", reg.models)
	}
	if _, okLegacy := modelRegistry.clients["plugin:alpha:legacy-provider"]; okLegacy {
		t.Fatal("legacy ModelRegistrar path was used despite ModelProvider.StaticModels")
	}
}

func TestRegisterModelsSkipsErrorEmptyAndInvalidModels(t *testing.T) {
	modelRegistry := newFakeModelRegistry()
	host := newHostWithRecords(
		capabilityRecord{
			id: "error",
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ModelRegistrar: modelRegistrarFunc(func(ctx context.Context, req pluginapi.ModelRegistrationRequest) (pluginapi.ModelRegistrationResponse, error) {
					return pluginapi.ModelRegistrationResponse{}, errors.New("register failed")
				}),
			}},
		},
		capabilityRecord{
			id: "empty-provider",
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ModelRegistrar: modelRegistrarFunc(func(ctx context.Context, req pluginapi.ModelRegistrationRequest) (pluginapi.ModelRegistrationResponse, error) {
					return pluginapi.ModelRegistrationResponse{Provider: " ", Models: []pluginapi.ModelInfo{{ID: "model"}}}, nil
				}),
			}},
		},
		capabilityRecord{
			id: "empty-models",
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ModelRegistrar: modelRegistrarFunc(func(ctx context.Context, req pluginapi.ModelRegistrationRequest) (pluginapi.ModelRegistrationResponse, error) {
					return pluginapi.ModelRegistrationResponse{Provider: "provider"}, nil
				}),
			}},
		},
		capabilityRecord{
			id: "invalid-models",
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ModelRegistrar: modelRegistrarFunc(func(ctx context.Context, req pluginapi.ModelRegistrationRequest) (pluginapi.ModelRegistrationResponse, error) {
					return pluginapi.ModelRegistrationResponse{Provider: "provider", Models: []pluginapi.ModelInfo{{ID: " "}}}, nil
				}),
			}},
		},
	)

	host.RegisterModels(context.Background(), modelRegistry)

	if len(modelRegistry.clients) != 0 {
		t.Fatalf("registered clients = %#v, want none", modelRegistry.clients)
	}
}

func TestRegisterModelsPrunesStaleClientAfterSnapshotChange(t *testing.T) {
	modelRegistry := newFakeModelRegistry()
	host := newHostWithRecords(capabilityRecord{
		id: "alpha",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ModelRegistrar: staticModelRegistrar("provider-a", "model-a"),
		}},
	})
	host.RegisterModels(context.Background(), modelRegistry)

	setHostSnapshotForTest(host, true, capabilityRecord{
		id: "bravo",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ModelRegistrar: staticModelRegistrar("provider-b", "model-b"),
		}},
	})
	host.RegisterModels(context.Background(), modelRegistry)

	if _, okClient := modelRegistry.clients["plugin:alpha:provider-a"]; okClient {
		t.Fatal("stale alpha client is still registered")
	}
	if modelRegistry.unregisters[0] != "plugin:alpha:provider-a" {
		t.Fatalf("unregistered clients = %#v, want alpha client first", modelRegistry.unregisters)
	}
	if _, okClient := modelRegistry.clients["plugin:bravo:provider-b"]; !okClient {
		t.Fatal("bravo client was not registered")
	}
}

func TestRegisterModelsDropsResultsWhenSnapshotChangesDuringRegistration(t *testing.T) {
	modelRegistry := newFakeModelRegistry()
	host := New()
	oldRecord := capabilityRecord{
		id: "alpha",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ModelRegistrar: modelRegistrarFunc(func(ctx context.Context, req pluginapi.ModelRegistrationRequest) (pluginapi.ModelRegistrationResponse, error) {
				setHostSnapshotForTest(host, true, capabilityRecord{
					id: "bravo",
					plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
						ModelRegistrar: staticModelRegistrar("provider-b", "model-b"),
					}},
				})
				return pluginapi.ModelRegistrationResponse{
					Provider: "provider-a",
					Models: []pluginapi.ModelInfo{{
						ID: "model-a",
					}},
				}, nil
			}),
		}},
	}
	setHostSnapshotForTest(host, true, oldRecord)
	host.modelProviders["alpha"] = "existing-provider"

	host.RegisterModels(context.Background(), modelRegistry)

	if len(modelRegistry.clients) != 0 {
		t.Fatalf("registered clients = %#v, want none after stale snapshot", modelRegistry.clients)
	}
	if len(modelRegistry.unregisters) != 0 {
		t.Fatalf("unregistered clients = %#v, want none after stale snapshot", modelRegistry.unregisters)
	}
	if host.modelProvider("alpha") != "existing-provider" {
		t.Fatalf("model provider = %q, want existing-provider", host.modelProvider("alpha"))
	}
}

func TestRegisterModelsPanicFusesPluginAndSkipsLaterCalls(t *testing.T) {
	calls := 0
	modelRegistry := newFakeModelRegistry()
	host := newHostWithRecords(capabilityRecord{
		id: "panic-plugin",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ModelRegistrar: modelRegistrarFunc(func(ctx context.Context, req pluginapi.ModelRegistrationRequest) (pluginapi.ModelRegistrationResponse, error) {
				calls++
				panic("register models panic")
			}),
		}},
	})

	host.RegisterModels(context.Background(), modelRegistry)
	host.RegisterModels(context.Background(), modelRegistry)

	if calls != 1 {
		t.Fatalf("RegisterModels calls = %d, want 1", calls)
	}
	if !host.isPluginFused("panic-plugin") {
		t.Fatal("panic-plugin was not fused")
	}
	if len(modelRegistry.clients) != 0 {
		t.Fatalf("registered clients = %#v, want none", modelRegistry.clients)
	}
}

func TestRegisterExecutorsDoesNotOverwriteExistingExecutor(t *testing.T) {
	manager := newFakeExecutorManager()
	existing := &fakeProviderExecutor{provider: "provider"}
	manager.RegisterExecutor(existing)
	host := newHostWithRecords(capabilityRecord{
		id: "alpha",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			Executor: &fakeExecutor{identifier: "provider"},
		}},
	})

	host.RegisterExecutors(manager, nil)

	if manager.registerCalls != 1 {
		t.Fatalf("RegisterExecutor calls = %d, want only existing registration", manager.registerCalls)
	}
	got, _ := manager.Executor("provider")
	if got != existing {
		t.Fatalf("registered executor = %#v, want existing executor", got)
	}
}

func TestRegisterExecutorsSameProviderKeepsFirstSnapshotCandidate(t *testing.T) {
	manager := newFakeExecutorManager()
	first := &fakeExecutor{identifier: "provider"}
	second := &fakeExecutor{identifier: "provider"}
	host := newHostWithRecords(
		capabilityRecord{
			id:       "low",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor: second,
			}},
		},
		capabilityRecord{
			id:       "high",
			priority: 20,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor: first,
			}},
		},
	)

	host.RegisterExecutors(manager, nil)

	if manager.registerCalls != 1 {
		t.Fatalf("RegisterExecutor calls = %d, want 1", manager.registerCalls)
	}
	adapter, okAdapter := manager.executors["provider"].(*executorAdapter)
	if !okAdapter {
		t.Fatalf("registered executor = %#v, want executorAdapter", manager.executors["provider"])
	}
	if adapter.pluginID != "high" || adapter.executor != first {
		t.Fatalf("registered adapter = %#v, want high priority executor", adapter)
	}
}

func TestRegisterExecutorsIdentifierPanicFusesPlugin(t *testing.T) {
	manager := newFakeExecutorManager()
	host := newHostWithRecords(capabilityRecord{
		id: "panic-identifier",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			Executor: &fakeExecutor{panicIdentifier: true},
		}},
	})

	host.RegisterExecutors(manager, nil)

	if !host.isPluginFused("panic-identifier") {
		t.Fatal("panic-identifier was not fused")
	}
	if manager.registerCalls != 0 {
		t.Fatalf("RegisterExecutor calls = %d, want 0", manager.registerCalls)
	}
}

func TestRegisterExecutorsSelectsHighestPriorityPluginExecutorPerModel(t *testing.T) {
	modelRegistry := newFakeModelRegistry()
	manager := newFakeExecutorManager()
	host := newHostWithRecords(
		capabilityRecord{
			id:       "low",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ModelRegistrar: staticModelRegistrar("low-provider", "shared-model"),
				Executor:       &fakeExecutor{identifier: "low-provider"},
			}},
		},
		capabilityRecord{
			id:       "high",
			priority: 20,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ModelRegistrar: staticModelRegistrar("high-provider", "shared-model"),
				Executor:       &fakeExecutor{identifier: "high-provider"},
			}},
		},
	)
	host.RegisterModels(context.Background(), modelRegistry)

	host.RegisterExecutors(manager, modelRegistry)

	if _, okLow := manager.executors["low-provider"]; okLow {
		t.Fatal("low priority executor was registered for shared-model")
	}
	if _, okHigh := manager.executors["high-provider"]; !okHigh {
		t.Fatal("high priority executor was not registered for shared-model")
	}
	if got := host.ModelsForProvider("low-provider"); len(got) != 0 {
		t.Fatalf("low provider models = %#v, want none", got)
	}
	got := host.ModelsForProvider("high-provider")
	if len(got) != 1 || got[0].ID != "shared-model" {
		t.Fatalf("high provider models = %#v, want shared-model", got)
	}
}

func TestRegisterExecutorsKeepsPluginModelsForNativeProviderWithoutOverwritingExecutor(t *testing.T) {
	modelRegistry := newFakeModelRegistry()
	manager := newFakeExecutorManager()
	native := &fakeProviderExecutor{provider: "native-provider"}
	manager.RegisterExecutor(native)
	host := newHostWithRecords(capabilityRecord{
		id:       "native-extension",
		priority: 20,
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ModelRegistrar: staticModelRegistrar("native-provider", "native-extension-model"),
			Executor:       &fakeExecutor{identifier: "native-provider"},
		}},
	})
	host.RegisterModels(context.Background(), modelRegistry)

	host.RegisterExecutors(manager, modelRegistry)

	if manager.registerCalls != 1 {
		t.Fatalf("RegisterExecutor calls = %d, want only native registration", manager.registerCalls)
	}
	gotExecutor, _ := manager.Executor("native-provider")
	if gotExecutor != native {
		t.Fatalf("native provider executor = %#v, want native executor", gotExecutor)
	}
	gotModels := host.ModelsForProvider("native-provider")
	if len(gotModels) != 1 || gotModels[0].ID != "native-extension-model" {
		t.Fatalf("native provider plugin models = %#v, want native-extension-model", gotModels)
	}
}

func TestRegisterExecutorsSkipsPluginModelWhenModelAlreadyHasNativeExecutor(t *testing.T) {
	modelRegistry := newFakeModelRegistry()
	modelRegistry.RegisterClient("native-auth", "native-provider", []*registry.ModelInfo{{ID: "shared-model"}})
	manager := newFakeExecutorManager()
	manager.RegisterExecutor(&fakeProviderExecutor{provider: "native-provider"})
	host := newHostWithRecords(capabilityRecord{
		id:       "plugin-executor",
		priority: 20,
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ModelRegistrar: staticModelRegistrar("plugin-provider", "shared-model"),
			Executor:       &fakeExecutor{identifier: "plugin-provider"},
		}},
	})
	host.RegisterModels(context.Background(), modelRegistry)

	host.RegisterExecutors(manager, modelRegistry)

	if _, okPlugin := manager.executors["plugin-provider"]; okPlugin {
		t.Fatal("plugin executor was registered for a model that already has a native executor")
	}
	if got := host.ModelsForProvider("plugin-provider"); len(got) != 0 {
		t.Fatalf("plugin provider models = %#v, want none", got)
	}
}

func TestRegisterExecutorsUsesRegisteredModelProviderBeforeFallback(t *testing.T) {
	modelRegistry := newFakeModelRegistry()
	manager := newFakeExecutorManager()
	exec := &fakeExecutor{identifier: "fallback-provider"}
	host := newHostWithRecords(capabilityRecord{
		id: "alpha",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ModelRegistrar: staticModelRegistrar("registered-provider", "model"),
			Executor:       exec,
		}},
	})
	host.RegisterModels(context.Background(), modelRegistry)

	host.RegisterExecutors(manager, modelRegistry)

	adapter, okAdapter := manager.executors["registered-provider"].(*executorAdapter)
	if !okAdapter {
		t.Fatalf("registered executor = %#v, want executorAdapter", manager.executors["registered-provider"])
	}
	if adapter.provider != "registered-provider" || adapter.executor != exec {
		t.Fatalf("adapter = %#v, want registered provider executor", adapter)
	}
	if _, okFallback := manager.executors["fallback-provider"]; okFallback {
		t.Fatal("fallback provider was registered despite model provider cache")
	}
}

func TestRegisterExecutorsExposesExecutorModelsForUserAuthBinding(t *testing.T) {
	modelRegistry := newFakeModelRegistry()
	manager := newFakeExecutorManager()
	exec := &fakeExecutor{identifier: "plugin-provider"}
	host := newHostWithRecords(capabilityRecord{
		id: "alpha",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ModelRegistrar: staticModelRegistrar("plugin-provider", "plugin-model"),
			Executor:       exec,
		}},
	})
	host.RegisterModels(context.Background(), modelRegistry)

	if len(modelRegistry.clients) != 0 {
		t.Fatalf("registered model clients = %#v, want none until a matching auth binds provider models", modelRegistry.clients)
	}

	host.RegisterExecutors(manager, modelRegistry)

	if _, okExecutor := manager.executors["plugin-provider"]; !okExecutor {
		t.Fatal("plugin provider executor was not registered")
	}
	models := host.ModelsForProvider("plugin-provider")
	if len(models) != 1 || models[0].ID != "plugin-model" {
		t.Fatalf("provider models = %#v, want plugin-model for user auth binding", models)
	}
	clientID := pluginExecutorModelClientID("alpha", "plugin-provider")
	reg := modelRegistry.clients[clientID]
	if reg == nil {
		t.Fatalf("executor model client %s was not registered", clientID)
	}
	if reg.provider != "plugin-provider" || len(reg.models) != 1 || reg.models[0].ID != "plugin-model" {
		t.Fatalf("executor model registry client = %#v, want plugin-provider/plugin-model", reg)
	}
	if providers := modelRegistry.GetModelProviders("plugin-model"); len(providers) != 1 || providers[0] != "plugin-provider" {
		t.Fatalf("providers for plugin-model = %#v, want plugin-provider", providers)
	}
}

func TestRegisterExecutorsOAuthScopeSkipsStaticModelClientButRegistersExecutor(t *testing.T) {
	modelRegistry := newFakeModelRegistry()
	manager := newFakeExecutorManager()
	staticCalled := false
	host := newHostWithRecords(capabilityRecord{
		id: "sample-provider",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			AuthProvider: fakeAuthProvider{identifier: "sample-provider"},
			ModelProvider: modelProviderFunc{
				staticModels: func(ctx context.Context, req pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
					staticCalled = true
					return pluginapi.ModelResponse{
						Provider: "sample-provider",
						Models:   []pluginapi.ModelInfo{{ID: "static-model"}},
					}, nil
				},
				modelsForAuth: func(ctx context.Context, req pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
					return pluginapi.ModelResponse{
						Provider: "sample-provider",
						Models:   []pluginapi.ModelInfo{{ID: "oauth-model"}},
					}, nil
				},
			},
			Executor:           &fakeExecutor{identifier: "sample-provider"},
			ExecutorModelScope: pluginapi.ExecutorModelScopeOAuth,
		}},
	})

	host.RegisterModels(context.Background(), modelRegistry)
	host.RegisterExecutors(manager, modelRegistry)

	if staticCalled {
		t.Fatal("StaticModels was called for an OAuth-only executor")
	}
	if _, okExecutor := manager.executors["sample-provider"]; !okExecutor {
		t.Fatal("OAuth-only executor was not registered")
	}
	if _, okClient := modelRegistry.clients[pluginExecutorModelClientID("sample-provider", "sample-provider")]; okClient {
		t.Fatal("OAuth-only executor registered a static model client")
	}
	if got := host.ModelsForProvider("sample-provider"); len(got) != 0 {
		t.Fatalf("OAuth-only provider models = %#v, want none", got)
	}

	result := host.ModelsForAuth(context.Background(), &coreauth.Auth{
		ID:       "sample-provider-auth",
		Provider: "sample-provider",
	})
	if !result.Handled || result.Provider != "sample-provider" || len(result.Models) != 1 || result.Models[0].ID != "oauth-model" {
		t.Fatalf("OAuth model result = %#v, want oauth-model", result)
	}
}

func TestModelsForAuthOAuthScopeFallsBackToExecutorIdentifier(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "alpha",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ModelProvider: modelProviderFunc{
				modelsForAuth: func(ctx context.Context, req pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
					return pluginapi.ModelResponse{
						Provider: "plugin-provider",
						Models:   []pluginapi.ModelInfo{{ID: "oauth-model"}},
					}, nil
				},
			},
			Executor:           &fakeExecutor{identifier: "plugin-provider"},
			ExecutorModelScope: pluginapi.ExecutorModelScopeOAuth,
		}},
	})

	result := host.ModelsForAuth(context.Background(), &coreauth.Auth{
		ID:       "plugin-auth",
		Provider: "plugin-provider",
	})

	if !result.Handled || result.Provider != "plugin-provider" || len(result.Models) != 1 || result.Models[0].ID != "oauth-model" {
		t.Fatalf("OAuth model result = %#v, want executor-identifier match", result)
	}
}

func TestRegisterExecutorsStaticScopeSkipsModelsForAuth(t *testing.T) {
	modelRegistry := newFakeModelRegistry()
	manager := newFakeExecutorManager()
	modelsForAuthCalled := false
	host := newHostWithRecords(capabilityRecord{
		id: "alpha",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			AuthProvider: fakeAuthProvider{identifier: "plugin-provider"},
			ModelProvider: modelProviderFunc{
				staticModels: func(ctx context.Context, req pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
					return pluginapi.ModelResponse{
						Provider: "plugin-provider",
						Models:   []pluginapi.ModelInfo{{ID: "static-model"}},
					}, nil
				},
				modelsForAuth: func(ctx context.Context, req pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
					modelsForAuthCalled = true
					return pluginapi.ModelResponse{
						Provider: "plugin-provider",
						Models:   []pluginapi.ModelInfo{{ID: "oauth-model"}},
					}, nil
				},
			},
			Executor:           &fakeExecutor{identifier: "plugin-provider"},
			ExecutorModelScope: pluginapi.ExecutorModelScopeStatic,
		}},
	})

	host.RegisterModels(context.Background(), modelRegistry)
	host.RegisterExecutors(manager, modelRegistry)

	clientID := pluginExecutorModelClientID("alpha", "plugin-provider")
	reg := modelRegistry.clients[clientID]
	if reg == nil || reg.provider != "plugin-provider" || len(reg.models) != 1 || reg.models[0].ID != "static-model" {
		t.Fatalf("static executor model client = %#v, want static-model", reg)
	}
	result := host.ModelsForAuth(context.Background(), &coreauth.Auth{
		ID:       "plugin-auth",
		Provider: "plugin-provider",
	})
	if result.Handled {
		t.Fatalf("static-only executor handled per-auth models: %#v", result)
	}
	if modelsForAuthCalled {
		t.Fatal("ModelsForAuth was called for a static-only executor")
	}
}

func TestRegisterExecutorsBothScopeKeepsStaticAndOAuthModels(t *testing.T) {
	modelRegistry := newFakeModelRegistry()
	manager := newFakeExecutorManager()
	host := newHostWithRecords(capabilityRecord{
		id: "alpha",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			AuthProvider: fakeAuthProvider{identifier: "plugin-provider"},
			ModelProvider: modelProviderFunc{
				staticModels: func(ctx context.Context, req pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
					return pluginapi.ModelResponse{
						Provider: "plugin-provider",
						Models:   []pluginapi.ModelInfo{{ID: "static-model"}},
					}, nil
				},
				modelsForAuth: func(ctx context.Context, req pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
					return pluginapi.ModelResponse{
						Provider: "plugin-provider",
						Models:   []pluginapi.ModelInfo{{ID: "oauth-model"}},
					}, nil
				},
			},
			Executor:           &fakeExecutor{identifier: "plugin-provider"},
			ExecutorModelScope: pluginapi.ExecutorModelScopeBoth,
		}},
	})

	host.RegisterModels(context.Background(), modelRegistry)
	host.RegisterExecutors(manager, modelRegistry)

	clientID := pluginExecutorModelClientID("alpha", "plugin-provider")
	reg := modelRegistry.clients[clientID]
	if reg == nil || reg.provider != "plugin-provider" || len(reg.models) != 1 || reg.models[0].ID != "static-model" {
		t.Fatalf("both-scope static model client = %#v, want static-model", reg)
	}
	result := host.ModelsForAuth(context.Background(), &coreauth.Auth{
		ID:       "plugin-auth",
		Provider: "plugin-provider",
	})
	if !result.Handled || result.Provider != "plugin-provider" || len(result.Models) != 1 || result.Models[0].ID != "oauth-model" {
		t.Fatalf("both-scope OAuth model result = %#v, want oauth-model", result)
	}
}

func TestRegisterExecutorsDropsResultsWhenSnapshotChangesBeforeCommit(t *testing.T) {
	manager := newFakeExecutorManager()
	host := New()
	staleExecutor := &executorAdapter{
		host:     host,
		pluginID: "stale",
		provider: "stale-provider",
	}
	manager.executors["stale-provider"] = staleExecutor
	host.executorProviders["stale-provider"] = struct{}{}

	changedSnapshot := false
	exec := &fakeExecutor{
		identifierFunc: func() string {
			if !changedSnapshot {
				changedSnapshot = true
				setHostSnapshotForTest(host, true)
			}
			return "provider-a"
		},
	}
	setHostSnapshotForTest(host, true, capabilityRecord{
		id: "alpha",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			Executor: exec,
		}},
	})

	host.RegisterExecutors(manager, nil)

	if manager.registerCalls != 0 {
		t.Fatalf("RegisterExecutor calls = %d, want none for stale snapshot", manager.registerCalls)
	}
	if _, okProvider := manager.executors["provider-a"]; okProvider {
		t.Fatal("provider-a executor was registered from a stale snapshot")
	}
	if manager.executors["stale-provider"] != staleExecutor {
		t.Fatalf("stale-provider executor = %#v, want existing executor preserved", manager.executors["stale-provider"])
	}
	if _, okProvider := host.executorProviders["stale-provider"]; !okProvider {
		t.Fatal("stale-provider ownership was pruned by a stale snapshot")
	}
}

func TestRegisterExecutorsFallbackUsesExecutorIdentifier(t *testing.T) {
	manager := newFakeExecutorManager()
	exec := &fakeExecutor{identifier: "  FallbackProvider  "}
	host := newHostWithRecords(capabilityRecord{
		id: "alpha",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			Executor: exec,
		}},
	})

	host.RegisterExecutors(manager, nil)

	adapter, okAdapter := manager.executors["fallbackprovider"].(*executorAdapter)
	if !okAdapter {
		t.Fatalf("registered executor = %#v, want fallback executorAdapter", manager.executors["fallbackprovider"])
	}
	if adapter.provider != "fallbackprovider" || adapter.executor != exec {
		t.Fatalf("adapter = %#v, want fallback provider executor", adapter)
	}
}

func TestRegisterExecutorsPrunesStaleProviderAfterMigration(t *testing.T) {
	modelRegistry := newFakeModelRegistry()
	manager := newFakeExecutorManager()
	exec := &fakeExecutor{identifier: "fallback-provider"}
	host := newHostWithRecords(capabilityRecord{
		id: "alpha",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ModelRegistrar: staticModelRegistrar("provider-a", "plugin-model"),
			Executor:       exec,
		}},
	})
	host.modelProviders["alpha"] = "provider-a"
	host.modelRegistrations["alpha"] = pluginModelRegistration{
		pluginID:    "alpha",
		provider:    "provider-a",
		models:      []*registry.ModelInfo{{ID: "plugin-model"}},
		hasExecutor: true,
	}
	host.RegisterExecutors(manager, modelRegistry)

	host.modelProviders["alpha"] = "provider-b"
	host.modelRegistrations["alpha"] = pluginModelRegistration{
		pluginID:    "alpha",
		provider:    "provider-b",
		models:      []*registry.ModelInfo{{ID: "plugin-model"}},
		hasExecutor: true,
	}
	host.RegisterExecutors(manager, modelRegistry)

	if _, okProvider := manager.executors["provider-a"]; okProvider {
		t.Fatal("provider-a executor is still registered")
	}
	if manager.unregisters[0] != "provider-a" {
		t.Fatalf("unregistered providers = %#v, want provider-a", manager.unregisters)
	}
	adapter, okAdapter := manager.executors["provider-b"].(*executorAdapter)
	if !okAdapter {
		t.Fatalf("provider-b executor = %#v, want executorAdapter", manager.executors["provider-b"])
	}
	if adapter.executor != exec {
		t.Fatalf("provider-b adapter executor = %#v, want migrated executor", adapter.executor)
	}
	if _, okClient := modelRegistry.clients[pluginExecutorModelClientID("alpha", "provider-a")]; okClient {
		t.Fatal("provider-a executor model client is still registered")
	}
	if _, okClient := modelRegistry.clients[pluginExecutorModelClientID("alpha", "provider-b")]; !okClient {
		t.Fatal("provider-b executor model client was not registered")
	}
}

func TestRegisterExecutorsDoesNotUnregisterStaleProviderOwnedExternally(t *testing.T) {
	manager := newFakeExecutorManager()
	exec := &fakeExecutor{identifier: "fallback-provider"}
	host := newHostWithRecords(capabilityRecord{
		id: "alpha",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			Executor: exec,
		}},
	})
	host.modelProviders["alpha"] = "provider-a"
	host.RegisterExecutors(manager, nil)

	external := &fakeProviderExecutor{provider: "provider-a"}
	manager.executors["provider-a"] = external
	host.modelProviders["alpha"] = "provider-b"
	host.RegisterExecutors(manager, nil)

	if len(manager.unregisters) != 0 {
		t.Fatalf("unregistered providers = %#v, want none for external owner", manager.unregisters)
	}
	if manager.executors["provider-a"] != external {
		t.Fatalf("provider-a executor = %#v, want external executor", manager.executors["provider-a"])
	}
	if _, okProvider := manager.executors["provider-b"]; !okProvider {
		t.Fatal("provider-b executor was not registered")
	}
}

func TestNormalizeRequestChainsByPriority(t *testing.T) {
	host := newHostWithRecords(
		capabilityRecord{
			id:       "high",
			priority: 20,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				RequestNormalizer: requestNormalizerFunc(func(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
					return pluginapi.PayloadResponse{Body: append(req.Body, []byte("|high")...)}, nil
				}),
			}},
		},
		capabilityRecord{
			id:       "low",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				RequestNormalizer: requestNormalizerFunc(func(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
					return pluginapi.PayloadResponse{Body: append(req.Body, []byte("|low")...)}, nil
				}),
			}},
		},
	)

	got := host.NormalizeRequest(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, "model", []byte("start"), false)
	if string(got) != "start|high|low" {
		t.Fatalf("NormalizeRequest() = %q, want %q", got, "start|high|low")
	}
}

func TestTranslateRequestStopsAtFirstSuccessfulCandidate(t *testing.T) {
	calls := make([]string, 0, 2)
	host := newHostWithRecords(
		capabilityRecord{
			id:       "high",
			priority: 20,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				RequestTranslator: requestTranslatorFunc(func(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
					calls = append(calls, "high")
					return pluginapi.PayloadResponse{Body: []byte("translated-high")}, nil
				}),
			}},
		},
		capabilityRecord{
			id:       "low",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				RequestTranslator: requestTranslatorFunc(func(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
					calls = append(calls, "low")
					return pluginapi.PayloadResponse{Body: []byte("translated-low")}, nil
				}),
			}},
		},
	)

	got, ok := host.TranslateRequest(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, "model", []byte("input"), false)
	if !ok {
		t.Fatal("TranslateRequest() ok = false, want true")
	}
	if string(got) != "translated-high" {
		t.Fatalf("TranslateRequest() = %q, want %q", got, "translated-high")
	}
	if fmt.Sprint(calls) != "[high]" {
		t.Fatalf("calls = %v, want [high]", calls)
	}
}

func TestAdaptersKeepPayloadOrTryNextOnErrorAndEmptyBody(t *testing.T) {
	host := newHostWithRecords(
		capabilityRecord{
			id:       "normalizer-error",
			priority: 30,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				RequestNormalizer: requestNormalizerFunc(func(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
					return pluginapi.PayloadResponse{}, fmt.Errorf("normalize failed")
				}),
			}},
		},
		capabilityRecord{
			id:       "normalizer-empty",
			priority: 20,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				RequestNormalizer: requestNormalizerFunc(func(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
					return pluginapi.PayloadResponse{}, nil
				}),
			}},
		},
		capabilityRecord{
			id:       "normalizer-success",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				RequestNormalizer: requestNormalizerFunc(func(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
					return pluginapi.PayloadResponse{Body: []byte("kept-then-success")}, nil
				}),
			}},
		},
	)

	normalized := host.NormalizeRequest(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, "model", []byte("original"), false)
	if string(normalized) != "kept-then-success" {
		t.Fatalf("NormalizeRequest() = %q, want %q", normalized, "kept-then-success")
	}

	translatorHost := newHostWithRecords(
		capabilityRecord{
			id:       "translator-error",
			priority: 30,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				RequestTranslator: requestTranslatorFunc(func(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
					return pluginapi.PayloadResponse{}, fmt.Errorf("translate failed")
				}),
			}},
		},
		capabilityRecord{
			id:       "translator-empty",
			priority: 20,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				RequestTranslator: requestTranslatorFunc(func(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
					return pluginapi.PayloadResponse{}, nil
				}),
			}},
		},
		capabilityRecord{
			id:       "translator-success",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				RequestTranslator: requestTranslatorFunc(func(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
					return pluginapi.PayloadResponse{Body: []byte("translated")}, nil
				}),
			}},
		},
	)

	translated, ok := translatorHost.TranslateRequest(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, "model", []byte("original"), false)
	if !ok {
		t.Fatal("TranslateRequest() ok = false, want true")
	}
	if string(translated) != "translated" {
		t.Fatalf("TranslateRequest() = %q, want %q", translated, "translated")
	}
}

func TestTranslatorPanicFusesPlugin(t *testing.T) {
	host := newHostWithRecords(
		capabilityRecord{
			id:       "panic-plugin",
			priority: 20,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				RequestNormalizer: requestNormalizerFunc(func(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
					panic("normalize panic")
				}),
			}},
		},
		capabilityRecord{
			id:       "next-plugin",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				RequestNormalizer: requestNormalizerFunc(func(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
					return pluginapi.PayloadResponse{Body: append(req.Body, []byte("|next")...)}, nil
				}),
			}},
		},
	)

	got := host.NormalizeRequest(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, "model", []byte("original"), false)
	if string(got) != "original|next" {
		t.Fatalf("NormalizeRequest() = %q, want %q", got, "original|next")
	}
	if !host.isPluginFused("panic-plugin") {
		t.Fatal("panic-plugin was not fused")
	}
}

func TestTranslatorPanicFusesEveryHookPath(t *testing.T) {
	cases := []struct {
		name     string
		pluginID string
		call     func(*Host) ([]byte, bool)
	}{
		{
			name:     "request translator",
			pluginID: "request-translator-panic",
			call: func(host *Host) ([]byte, bool) {
				setHostSnapshotForTest(host, true, capabilityRecord{
					id:       "request-translator-panic",
					priority: 10,
					plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
						RequestTranslator: requestTranslatorFunc(func(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
							panic("request translator panic")
						}),
					}},
				})
				return host.TranslateRequest(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, "model", []byte("body"), false)
			},
		},
		{
			name:     "response before normalizer",
			pluginID: "response-before-panic",
			call: func(host *Host) ([]byte, bool) {
				setHostSnapshotForTest(host, true, capabilityRecord{
					id:       "response-before-panic",
					priority: 10,
					plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
						ResponseBeforeTranslator: responseNormalizerFunc(func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
							panic("response before panic")
						}),
					}},
				})
				return host.NormalizeResponseBefore(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, "model", nil, nil, []byte("body"), false), false
			},
		},
		{
			name:     "response translator",
			pluginID: "response-translator-panic",
			call: func(host *Host) ([]byte, bool) {
				setHostSnapshotForTest(host, true, capabilityRecord{
					id:       "response-translator-panic",
					priority: 10,
					plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
						ResponseTranslator: responseTranslatorFunc(func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
							panic("response translator panic")
						}),
					}},
				})
				return host.TranslateResponse(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, "model", nil, nil, []byte("body"), false)
			},
		},
		{
			name:     "response after normalizer",
			pluginID: "response-after-panic",
			call: func(host *Host) ([]byte, bool) {
				setHostSnapshotForTest(host, true, capabilityRecord{
					id:       "response-after-panic",
					priority: 10,
					plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
						ResponseAfterTranslator: responseNormalizerFunc(func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
							panic("response after panic")
						}),
					}},
				})
				return host.NormalizeResponseAfter(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, "model", nil, nil, []byte("body"), false), false
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			host := New()
			got, _ := tt.call(host)
			if string(got) != "body" {
				t.Fatalf("hook result = %q, want original body", got)
			}
			if !host.isPluginFused(tt.pluginID) {
				t.Fatalf("%s was not fused", tt.pluginID)
			}
		})
	}
}

func TestResponseNormalizersChainByPriority(t *testing.T) {
	host := newHostWithRecords(
		capabilityRecord{
			id:       "high",
			priority: 20,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ResponseBeforeTranslator: responseNormalizerFunc(func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
					return pluginapi.PayloadResponse{Body: append(req.Body, []byte("|before-high")...)}, nil
				}),
				ResponseAfterTranslator: responseNormalizerFunc(func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
					return pluginapi.PayloadResponse{Body: append(req.Body, []byte("|after-high")...)}, nil
				}),
			}},
		},
		capabilityRecord{
			id:       "low",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ResponseBeforeTranslator: responseNormalizerFunc(func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
					return pluginapi.PayloadResponse{Body: append(req.Body, []byte("|before-low")...)}, nil
				}),
				ResponseAfterTranslator: responseNormalizerFunc(func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
					return pluginapi.PayloadResponse{Body: append(req.Body, []byte("|after-low")...)}, nil
				}),
			}},
		},
	)

	before := host.NormalizeResponseBefore(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, "model", []byte("original-request"), []byte("translated-request"), []byte("body"), true)
	if string(before) != "body|before-high|before-low" {
		t.Fatalf("NormalizeResponseBefore() = %q, want %q", before, "body|before-high|before-low")
	}
	after := host.NormalizeResponseAfter(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, "model", []byte("original-request"), []byte("translated-request"), []byte("body"), true)
	if string(after) != "body|after-high|after-low" {
		t.Fatalf("NormalizeResponseAfter() = %q, want %q", after, "body|after-high|after-low")
	}
}

func TestTranslateResponseStopsAtFirstSuccessfulCandidate(t *testing.T) {
	calls := make([]string, 0, 2)
	host := newHostWithRecords(
		capabilityRecord{
			id:       "high",
			priority: 20,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ResponseTranslator: responseTranslatorFunc(func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
					calls = append(calls, "high")
					return pluginapi.PayloadResponse{Body: []byte("response-high")}, nil
				}),
			}},
		},
		capabilityRecord{
			id:       "low",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ResponseTranslator: responseTranslatorFunc(func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
					calls = append(calls, "low")
					return pluginapi.PayloadResponse{Body: []byte("response-low")}, nil
				}),
			}},
		},
	)

	got, ok := host.TranslateResponse(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, "model", nil, nil, []byte("input"), false)
	if !ok {
		t.Fatal("TranslateResponse() ok = false, want true")
	}
	if string(got) != "response-high" {
		t.Fatalf("TranslateResponse() = %q, want %q", got, "response-high")
	}
	if fmt.Sprint(calls) != "[high]" {
		t.Fatalf("calls = %v, want [high]", calls)
	}
}

func TestInterceptRequestChainsByPriorityAndHeaders(t *testing.T) {
	host := newHostWithRecords(
		capabilityRecord{
			id:       "high",
			priority: 20,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				RequestInterceptor: requestInterceptorFunc(func(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
					if req.SourceFormat != "openai" || req.Model != "normalized" || req.RequestedModel != "requested" {
						t.Fatalf("unexpected request context: %#v", req)
					}
					return pluginapi.RequestInterceptResponse{
						Headers: http.Header{"X-Plugin": []string{"high"}},
						Body:    append(req.Body, []byte("|high")...),
					}, nil
				}),
			}},
		},
		capabilityRecord{
			id:       "low",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				RequestInterceptor: requestInterceptorFunc(func(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
					return pluginapi.RequestInterceptResponse{
						Headers:      http.Header{"X-Plugin": []string{"low"}, "X-Low": []string{"1"}},
						Body:         append(req.Body, []byte("|low")...),
						ClearHeaders: []string{"X-Remove"},
					}, nil
				}),
			}},
		},
	)
	headers := http.Header{"X-Remove": []string{"yes"}}

	got := host.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
		SourceFormat:   "openai",
		Model:          "normalized",
		RequestedModel: "requested",
		Stream:         false,
		Headers:        headers,
		Body:           []byte("start"),
	})

	if string(got.Body) != "start|high|low" {
		t.Fatalf("body = %q, want %q", got.Body, "start|high|low")
	}
	if got.Headers.Get("X-Plugin") != "low" || got.Headers.Get("X-Low") != "1" || got.Headers.Get("X-Remove") != "" {
		t.Fatalf("headers = %#v", got.Headers)
	}
	if headers.Get("X-Plugin") != "" {
		t.Fatalf("input headers were mutated: %#v", headers)
	}
}

func TestInterceptRequestAfterAuthPassesTargetFormat(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "after",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			RequestInterceptor: requestInterceptorFunc(func(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
				if req.SourceFormat != "openai" || req.ToFormat != "codex" {
					t.Fatalf("request formats = %q -> %q, want openai -> codex", req.SourceFormat, req.ToFormat)
				}
				return pluginapi.RequestInterceptResponse{Body: append(req.Body, []byte("|after")...)}, nil
			}),
		}},
	})

	got := host.InterceptRequestAfterAuth(context.Background(), pluginapi.RequestInterceptRequest{
		SourceFormat: "openai",
		ToFormat:     "codex",
		Model:        "gpt-5.4",
		Body:         []byte("body"),
	})

	if string(got.Body) != "body|after" {
		t.Fatalf("body = %q, want body|after", got.Body)
	}
}

func TestInterceptorsSkipExceptedPlugin(t *testing.T) {
	originCalls := 0
	otherCalls := 0
	host := newHostWithRecords(
		capabilityRecord{
			id:       "origin",
			priority: 20,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				RequestInterceptor: requestInterceptorFunc(func(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
					originCalls++
					return pluginapi.RequestInterceptResponse{Body: append(req.Body, []byte("|origin-request")...)}, nil
				}),
				ResponseInterceptor: responseInterceptorFunc{
					interceptResponse: func(ctx context.Context, req pluginapi.ResponseInterceptRequest) (pluginapi.ResponseInterceptResponse, error) {
						originCalls++
						return pluginapi.ResponseInterceptResponse{Body: append(req.Body, []byte("|origin-response")...)}, nil
					},
				},
				StreamChunkInterceptor: responseInterceptorFunc{
					interceptStreamChunk: func(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) (pluginapi.StreamChunkInterceptResponse, error) {
						originCalls++
						return pluginapi.StreamChunkInterceptResponse{Body: append(req.Body, []byte("|origin-stream")...)}, nil
					},
				},
			}},
		},
		capabilityRecord{
			id:       "other",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				RequestInterceptor: requestInterceptorFunc(func(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
					otherCalls++
					return pluginapi.RequestInterceptResponse{Body: append(req.Body, []byte("|other-request")...)}, nil
				}),
				ResponseInterceptor: responseInterceptorFunc{
					interceptResponse: func(ctx context.Context, req pluginapi.ResponseInterceptRequest) (pluginapi.ResponseInterceptResponse, error) {
						otherCalls++
						return pluginapi.ResponseInterceptResponse{Body: append(req.Body, []byte("|other-response")...)}, nil
					},
				},
				StreamChunkInterceptor: responseInterceptorFunc{
					interceptStreamChunk: func(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) (pluginapi.StreamChunkInterceptResponse, error) {
						otherCalls++
						return pluginapi.StreamChunkInterceptResponse{Body: append(req.Body, []byte("|other-stream")...)}, nil
					},
				},
			}},
		},
	)

	reqOut := host.InterceptRequestBeforeAuthExcept(context.Background(), pluginapi.RequestInterceptRequest{Body: []byte("body")}, "origin")
	afterOut := host.InterceptRequestAfterAuthExcept(context.Background(), pluginapi.RequestInterceptRequest{Body: []byte("body")}, "origin")
	respOut := host.InterceptResponseExcept(context.Background(), pluginapi.ResponseInterceptRequest{Body: []byte("body")}, "origin")
	streamOut := host.InterceptStreamChunkExcept(context.Background(), pluginapi.StreamChunkInterceptRequest{Body: []byte("body")}, "origin")

	if originCalls != 0 {
		t.Fatalf("origin plugin calls = %d, want 0", originCalls)
	}
	if otherCalls != 4 {
		t.Fatalf("other plugin calls = %d, want 4", otherCalls)
	}
	if string(reqOut.Body) != "body|other-request" {
		t.Fatalf("request body = %q, want body|other-request", reqOut.Body)
	}
	if string(afterOut.Body) != "body|other-request" {
		t.Fatalf("after-auth request body = %q, want body|other-request", afterOut.Body)
	}
	if string(respOut.Body) != "body|other-response" {
		t.Fatalf("response body = %q, want body|other-response", respOut.Body)
	}
	if string(streamOut.Body) != "body|other-stream" {
		t.Fatalf("stream body = %q, want body|other-stream", streamOut.Body)
	}
}

func TestResponseInterceptorsChainAndStreamHistory(t *testing.T) {
	var seenHistory [][]byte
	var sawSecondResponse bool
	var sawSecondStream bool
	host := newHostWithRecords(
		capabilityRecord{
			id:       "high",
			priority: 20,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ResponseInterceptor: responseInterceptorFunc{
					interceptResponse: func(ctx context.Context, req pluginapi.ResponseInterceptRequest) (pluginapi.ResponseInterceptResponse, error) {
						return pluginapi.ResponseInterceptResponse{
							Headers: http.Header{"X-Response": []string{"high"}},
							Body:    append(req.Body, []byte("|high")...),
						}, nil
					},
				},
				StreamChunkInterceptor: responseInterceptorFunc{
					interceptStreamChunk: func(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) (pluginapi.StreamChunkInterceptResponse, error) {
						seenHistory = req.HistoryChunks
						return pluginapi.StreamChunkInterceptResponse{
							Headers: http.Header{"X-Stream": []string{"high"}},
							Body:    append(req.Body, []byte("|high")...),
						}, nil
					},
				},
			}},
		},
		capabilityRecord{
			id:       "low",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ResponseInterceptor: responseInterceptorFunc{
					interceptResponse: func(ctx context.Context, req pluginapi.ResponseInterceptRequest) (pluginapi.ResponseInterceptResponse, error) {
						if string(req.Body) != "body|high" {
							t.Fatalf("second response interceptor body = %q, want body|high", req.Body)
						}
						if req.ResponseHeaders.Get("X-Response") != "high" {
							t.Fatalf("second response interceptor headers = %#v, want high header", req.ResponseHeaders)
						}
						sawSecondResponse = true
						return pluginapi.ResponseInterceptResponse{
							Headers:      http.Header{"X-Response": []string{"low"}, "X-Low": []string{"1"}},
							ClearHeaders: []string{"X-Remove"},
							Body:         append(req.Body, []byte("|low")...),
						}, nil
					},
				},
				StreamChunkInterceptor: responseInterceptorFunc{
					interceptStreamChunk: func(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) (pluginapi.StreamChunkInterceptResponse, error) {
						if string(req.Body) != "chunk|high" {
							t.Fatalf("second stream interceptor body = %q, want chunk|high", req.Body)
						}
						if req.ResponseHeaders.Get("X-Stream") != "high" {
							t.Fatalf("second stream interceptor headers = %#v, want high header", req.ResponseHeaders)
						}
						if len(req.HistoryChunks) != 1 || string(req.HistoryChunks[0]) != "first" {
							t.Fatalf("second stream interceptor history = %#v", req.HistoryChunks)
						}
						seenHistory = req.HistoryChunks
						sawSecondStream = true
						return pluginapi.StreamChunkInterceptResponse{
							Headers:      http.Header{"X-Stream": []string{"low"}, "X-Low": []string{"1"}},
							ClearHeaders: []string{"X-Remove"},
							Body:         append(req.Body, []byte("|low")...),
						}, nil
					},
				},
			}},
		},
	)

	nonStream := host.InterceptResponse(context.Background(), pluginapi.ResponseInterceptRequest{
		SourceFormat:    "openai",
		Model:           "normalized",
		RequestedModel:  "requested",
		ResponseHeaders: http.Header{"Content-Type": []string{"application/json"}, "X-Remove": []string{"yes"}},
		Body:            []byte("body"),
		StatusCode:      http.StatusOK,
	})
	if string(nonStream.Body) != "body|high|low" || nonStream.Headers.Get("X-Response") != "low" || nonStream.Headers.Get("X-Low") != "1" {
		t.Fatalf("non-stream result = %#v", nonStream)
	}
	if nonStream.Headers.Get("X-Remove") != "" {
		t.Fatalf("non-stream headers kept cleared value: %#v", nonStream.Headers)
	}
	if !sawSecondResponse {
		t.Fatal("second response interceptor was not called")
	}

	stream := host.InterceptStreamChunk(context.Background(), pluginapi.StreamChunkInterceptRequest{
		SourceFormat:    "openai",
		Model:           "normalized",
		RequestedModel:  "requested",
		ResponseHeaders: http.Header{"Content-Type": []string{"text/event-stream"}, "X-Remove": []string{"yes"}},
		Body:            []byte("chunk"),
		HistoryChunks:   [][]byte{[]byte("first")},
		ChunkIndex:      1,
	})
	if string(stream.Body) != "chunk|high|low" || stream.Headers.Get("X-Stream") != "low" || stream.Headers.Get("X-Low") != "1" {
		t.Fatalf("stream result = %#v", stream)
	}
	if stream.Headers.Get("X-Remove") != "" {
		t.Fatalf("stream headers kept cleared value: %#v", stream.Headers)
	}
	if len(seenHistory) != 1 || string(seenHistory[0]) != "first" {
		t.Fatalf("history = %#v", seenHistory)
	}
	if !sawSecondStream {
		t.Fatal("second stream interceptor was not called")
	}
}

func TestInterceptorsSkipErrorsAndFusePanics(t *testing.T) {
	host := newHostWithRecords(
		capabilityRecord{
			id:       "error",
			priority: 30,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				RequestInterceptor: requestInterceptorFunc(func(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
					return pluginapi.RequestInterceptResponse{}, fmt.Errorf("request failed")
				}),
			}},
		},
		capabilityRecord{
			id:       "panic",
			priority: 20,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				RequestInterceptor: requestInterceptorFunc(func(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
					panic("request panic")
				}),
			}},
		},
		capabilityRecord{
			id:       "success",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				RequestInterceptor: requestInterceptorFunc(func(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
					return pluginapi.RequestInterceptResponse{Body: append(req.Body, []byte("|success")...)}, nil
				}),
			}},
		},
	)

	got := host.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{Body: []byte("body")})
	if string(got.Body) != "body|success" {
		t.Fatalf("body = %q, want body|success", got.Body)
	}
	if !host.isPluginFused("panic") {
		t.Fatal("panic plugin was not fused")
	}
}

func TestStreamInterceptorsDropChunkStopsChain(t *testing.T) {
	var lowCalled bool
	host := newHostWithRecords(
		capabilityRecord{
			id:       "high",
			priority: 20,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				StreamChunkInterceptor: responseInterceptorFunc{
					interceptStreamChunk: func(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) (pluginapi.StreamChunkInterceptResponse, error) {
						return pluginapi.StreamChunkInterceptResponse{
							Headers:      http.Header{"X-Stream": []string{"high"}},
							Body:         append(req.Body, []byte("|high")...),
							DropChunk:    true,
							ClearHeaders: nil,
						}, nil
					},
				},
			}},
		},
		capabilityRecord{
			id:       "low",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				StreamChunkInterceptor: responseInterceptorFunc{
					interceptStreamChunk: func(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) (pluginapi.StreamChunkInterceptResponse, error) {
						lowCalled = true
						return pluginapi.StreamChunkInterceptResponse{
							Headers: http.Header{"X-Stream": []string{"low"}},
							Body:    append(req.Body, []byte("|low")...),
						}, nil
					},
				},
			}},
		},
	)

	got := host.InterceptStreamChunk(context.Background(), pluginapi.StreamChunkInterceptRequest{
		SourceFormat:   "openai",
		Model:          "normalized",
		RequestedModel: "requested",
		Body:           []byte("chunk"),
	})
	if lowCalled {
		t.Fatal("low-priority stream interceptor should not be called after DropChunk")
	}
	if !got.DropChunk {
		t.Fatal("DropChunk = false, want true")
	}
	if string(got.Body) != "chunk|high" {
		t.Fatalf("body = %q, want chunk|high", got.Body)
	}
	if got.Headers.Get("X-Stream") != "high" {
		t.Fatalf("headers = %#v, want high header", got.Headers)
	}
}

func TestHasStreamInterceptorsReflectsActiveStreamInterceptors(t *testing.T) {
	requestOnly := newHostWithRecords(capabilityRecord{
		id: "request",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			RequestInterceptor: requestInterceptorFunc(func(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
				return pluginapi.RequestInterceptResponse{Body: req.Body}, nil
			}),
		}},
	})
	if requestOnly.HasStreamInterceptors() {
		t.Fatal("HasStreamInterceptors() = true, want false for request-only plugins")
	}

	responseOnly := newHostWithRecords(capabilityRecord{
		id: "response",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ResponseInterceptor: responseInterceptorFunc{
				interceptResponse: func(ctx context.Context, req pluginapi.ResponseInterceptRequest) (pluginapi.ResponseInterceptResponse, error) {
					return pluginapi.ResponseInterceptResponse{Body: req.Body}, nil
				},
			},
		}},
	})
	if responseOnly.HasStreamInterceptors() {
		t.Fatal("HasStreamInterceptors() = true, want false for response-only plugins")
	}

	streamHost := newHostWithRecords(capabilityRecord{
		id: "stream",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			StreamChunkInterceptor: responseInterceptorFunc{
				interceptStreamChunk: func(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) (pluginapi.StreamChunkInterceptResponse, error) {
					return pluginapi.StreamChunkInterceptResponse{Body: req.Body}, nil
				},
			},
		}},
	})
	if !streamHost.HasStreamInterceptors() {
		t.Fatal("HasStreamInterceptors() = false, want true for stream interceptors")
	}
	streamHost.mu.Lock()
	streamHost.fused["stream"] = "test fused"
	streamHost.mu.Unlock()
	if streamHost.HasStreamInterceptors() {
		t.Fatal("HasStreamInterceptors() = true, want false after interceptor plugin is fused")
	}
}

func TestHasRequestInterceptorsReflectsActiveRequestInterceptors(t *testing.T) {
	responseOnly := newHostWithRecords(capabilityRecord{
		id: "response",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ResponseInterceptor: responseInterceptorFunc{
				interceptResponse: func(ctx context.Context, req pluginapi.ResponseInterceptRequest) (pluginapi.ResponseInterceptResponse, error) {
					return pluginapi.ResponseInterceptResponse{Body: req.Body}, nil
				},
			},
		}},
	})
	if responseOnly.HasRequestInterceptors() {
		t.Fatal("HasRequestInterceptors() = true, want false for response-only plugins")
	}

	requestHost := newHostWithRecords(capabilityRecord{
		id: "request",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			RequestInterceptor: requestInterceptorFunc(func(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
				return pluginapi.RequestInterceptResponse{Body: req.Body}, nil
			}),
		}},
	})
	if !requestHost.HasRequestInterceptors() {
		t.Fatal("HasRequestInterceptors() = false, want true for request interceptors")
	}
	requestHost.mu.Lock()
	requestHost.fused["request"] = "test fused"
	requestHost.mu.Unlock()
	if requestHost.HasRequestInterceptors() {
		t.Fatal("HasRequestInterceptors() = true, want false after request plugin is fused")
	}
}

func TestInterceptorsDoNotMutateInputs(t *testing.T) {
	t.Run("request", func(t *testing.T) {
		headers := http.Header{"X-Request": []string{"input"}}
		metadata := map[string]any{
			"nested":    map[string]any{"value": "original"},
			"items":     []any{map[string]any{"value": "original"}},
			"strings":   []string{"original"},
			"bytes":     []byte("original"),
			"labels":    map[string]string{"name": "original"},
			"values":    url.Values{"name": []string{"original"}},
			"mapSlice":  map[string][]string{"name": []string{"original"}},
			"sliceMap":  []map[string]string{{"name": "original"}},
			"aliasMap":  stringSliceAlias{"original"},
			"aliasList": mapSliceAlias{{"name": "original"}},
			"key":       "value",
		}
		body := []byte("request-body")
		host := newHostWithRecords(capabilityRecord{
			id: "request",
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				RequestInterceptor: requestInterceptorFunc(func(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
					req.Headers.Set("X-Request", "mutated")
					req.Body[0] = 'R'
					req.Metadata["key"] = "mutated"
					req.Metadata["nested"].(map[string]any)["value"] = "mutated"
					req.Metadata["items"].([]any)[0].(map[string]any)["value"] = "mutated"
					req.Metadata["strings"].([]string)[0] = "mutated"
					req.Metadata["bytes"].([]byte)[0] = 'M'
					req.Metadata["labels"].(map[string]string)["name"] = "mutated"
					req.Metadata["values"].(url.Values)["name"][0] = "mutated"
					req.Metadata["mapSlice"].(map[string][]string)["name"][0] = "mutated"
					req.Metadata["sliceMap"].([]map[string]string)[0]["name"] = "mutated"
					req.Metadata["aliasMap"].(stringSliceAlias)[0] = "mutated"
					req.Metadata["aliasList"].(mapSliceAlias)[0]["name"] = "mutated"
					return pluginapi.RequestInterceptResponse{Body: append(req.Body, []byte("|ok")...)}, nil
				}),
			}},
		})

		got := host.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{
			Headers:  headers,
			Body:     body,
			Metadata: metadata,
		})
		if headers.Get("X-Request") != "input" {
			t.Fatalf("request headers mutated: %#v", headers)
		}
		if string(body) != "request-body" {
			t.Fatalf("request body mutated: %q", body)
		}
		if metadata["key"] != "value" {
			t.Fatalf("request metadata mutated: %#v", metadata)
		}
		if metadata["nested"].(map[string]any)["value"] != "original" || metadata["items"].([]any)[0].(map[string]any)["value"] != "original" {
			t.Fatalf("request nested metadata mutated: %#v", metadata)
		}
		if metadata["strings"].([]string)[0] != "original" || string(metadata["bytes"].([]byte)) != "original" || metadata["labels"].(map[string]string)["name"] != "original" {
			t.Fatalf("request nested metadata aliases mutated: %#v", metadata)
		}
		if metadata["values"].(url.Values)["name"][0] != "original" || metadata["mapSlice"].(map[string][]string)["name"][0] != "original" {
			t.Fatalf("request map/slice metadata mutated: %#v", metadata)
		}
		if metadata["sliceMap"].([]map[string]string)[0]["name"] != "original" || metadata["aliasMap"].(stringSliceAlias)[0] != "original" || metadata["aliasList"].(mapSliceAlias)[0]["name"] != "original" {
			t.Fatalf("request alias metadata mutated: %#v", metadata)
		}
		if !strings.HasSuffix(string(got.Body), "|ok") {
			t.Fatalf("request result body = %q", got.Body)
		}
	})

	t.Run("response", func(t *testing.T) {
		requestHeaders := http.Header{"X-Request": []string{"input"}}
		responseHeaders := http.Header{"X-Response": []string{"input"}}
		originalRequest := []byte("original")
		requestBody := []byte("request")
		body := []byte("body")
		metadata := map[string]any{
			"nested":    map[string]any{"value": "original"},
			"items":     []any{map[string]any{"value": "original"}},
			"strings":   []string{"original"},
			"bytes":     []byte("original"),
			"labels":    map[string]string{"name": "original"},
			"values":    url.Values{"name": []string{"original"}},
			"mapSlice":  map[string][]string{"name": []string{"original"}},
			"sliceMap":  []map[string]string{{"name": "original"}},
			"aliasMap":  stringSliceAlias{"original"},
			"aliasList": mapSliceAlias{{"name": "original"}},
			"key":       "value",
		}
		host := newHostWithRecords(capabilityRecord{
			id: "response",
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ResponseInterceptor: responseInterceptorFunc{
					interceptResponse: func(ctx context.Context, req pluginapi.ResponseInterceptRequest) (pluginapi.ResponseInterceptResponse, error) {
						req.RequestHeaders.Set("X-Request", "mutated")
						req.ResponseHeaders.Set("X-Response", "mutated")
						req.OriginalRequest[0] = 'O'
						req.RequestBody[0] = 'R'
						req.Body[0] = 'B'
						req.Metadata["key"] = "mutated"
						req.Metadata["nested"].(map[string]any)["value"] = "mutated"
						req.Metadata["items"].([]any)[0].(map[string]any)["value"] = "mutated"
						req.Metadata["strings"].([]string)[0] = "mutated"
						req.Metadata["bytes"].([]byte)[0] = 'M'
						req.Metadata["labels"].(map[string]string)["name"] = "mutated"
						req.Metadata["values"].(url.Values)["name"][0] = "mutated"
						req.Metadata["mapSlice"].(map[string][]string)["name"][0] = "mutated"
						req.Metadata["sliceMap"].([]map[string]string)[0]["name"] = "mutated"
						req.Metadata["aliasMap"].(stringSliceAlias)[0] = "mutated"
						req.Metadata["aliasList"].(mapSliceAlias)[0]["name"] = "mutated"
						return pluginapi.ResponseInterceptResponse{Body: append(req.Body, []byte("|ok")...)}, nil
					},
				},
			}},
		})

		got := host.InterceptResponse(context.Background(), pluginapi.ResponseInterceptRequest{
			RequestHeaders:  requestHeaders,
			ResponseHeaders: responseHeaders,
			OriginalRequest: originalRequest,
			RequestBody:     requestBody,
			Body:            body,
			Metadata:        metadata,
		})
		if requestHeaders.Get("X-Request") != "input" {
			t.Fatalf("request headers mutated: %#v", requestHeaders)
		}
		if responseHeaders.Get("X-Response") != "input" {
			t.Fatalf("response headers mutated: %#v", responseHeaders)
		}
		if string(originalRequest) != "original" {
			t.Fatalf("original request mutated: %q", originalRequest)
		}
		if string(requestBody) != "request" {
			t.Fatalf("request body mutated: %q", requestBody)
		}
		if string(body) != "body" {
			t.Fatalf("response body mutated: %q", body)
		}
		if metadata["key"] != "value" {
			t.Fatalf("response metadata mutated: %#v", metadata)
		}
		if metadata["nested"].(map[string]any)["value"] != "original" || metadata["items"].([]any)[0].(map[string]any)["value"] != "original" {
			t.Fatalf("response nested metadata mutated: %#v", metadata)
		}
		if metadata["strings"].([]string)[0] != "original" || string(metadata["bytes"].([]byte)) != "original" || metadata["labels"].(map[string]string)["name"] != "original" {
			t.Fatalf("response nested metadata aliases mutated: %#v", metadata)
		}
		if metadata["values"].(url.Values)["name"][0] != "original" || metadata["mapSlice"].(map[string][]string)["name"][0] != "original" {
			t.Fatalf("response map/slice metadata mutated: %#v", metadata)
		}
		if metadata["sliceMap"].([]map[string]string)[0]["name"] != "original" || metadata["aliasMap"].(stringSliceAlias)[0] != "original" || metadata["aliasList"].(mapSliceAlias)[0]["name"] != "original" {
			t.Fatalf("response alias metadata mutated: %#v", metadata)
		}
		if !strings.HasSuffix(string(got.Body), "|ok") {
			t.Fatalf("response result body = %q", got.Body)
		}
	})

	t.Run("stream", func(t *testing.T) {
		requestHeaders := http.Header{"X-Request": []string{"input"}}
		responseHeaders := http.Header{"X-Response": []string{"input"}}
		originalRequest := []byte("original")
		requestBody := []byte("request")
		body := []byte("chunk")
		history := [][]byte{[]byte("first")}
		metadata := map[string]any{
			"nested":    map[string]any{"value": "original"},
			"items":     []any{map[string]any{"value": "original"}},
			"strings":   []string{"original"},
			"bytes":     []byte("original"),
			"labels":    map[string]string{"name": "original"},
			"values":    url.Values{"name": []string{"original"}},
			"mapSlice":  map[string][]string{"name": []string{"original"}},
			"sliceMap":  []map[string]string{{"name": "original"}},
			"aliasMap":  stringSliceAlias{"original"},
			"aliasList": mapSliceAlias{{"name": "original"}},
			"key":       "value",
		}
		host := newHostWithRecords(capabilityRecord{
			id: "stream",
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				StreamChunkInterceptor: responseInterceptorFunc{
					interceptStreamChunk: func(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) (pluginapi.StreamChunkInterceptResponse, error) {
						req.RequestHeaders.Set("X-Request", "mutated")
						req.ResponseHeaders.Set("X-Response", "mutated")
						req.OriginalRequest[0] = 'O'
						req.RequestBody[0] = 'R'
						req.Body[0] = 'C'
						req.HistoryChunks[0][0] = 'F'
						req.Metadata["key"] = "mutated"
						req.Metadata["nested"].(map[string]any)["value"] = "mutated"
						req.Metadata["items"].([]any)[0].(map[string]any)["value"] = "mutated"
						req.Metadata["strings"].([]string)[0] = "mutated"
						req.Metadata["bytes"].([]byte)[0] = 'M'
						req.Metadata["labels"].(map[string]string)["name"] = "mutated"
						req.Metadata["values"].(url.Values)["name"][0] = "mutated"
						req.Metadata["mapSlice"].(map[string][]string)["name"][0] = "mutated"
						req.Metadata["sliceMap"].([]map[string]string)[0]["name"] = "mutated"
						req.Metadata["aliasMap"].(stringSliceAlias)[0] = "mutated"
						req.Metadata["aliasList"].(mapSliceAlias)[0]["name"] = "mutated"
						return pluginapi.StreamChunkInterceptResponse{Body: append(req.Body, []byte("|ok")...)}, nil
					},
				},
			}},
		})

		got := host.InterceptStreamChunk(context.Background(), pluginapi.StreamChunkInterceptRequest{
			RequestHeaders:  requestHeaders,
			ResponseHeaders: responseHeaders,
			OriginalRequest: originalRequest,
			RequestBody:     requestBody,
			Body:            body,
			HistoryChunks:   history,
			Metadata:        metadata,
		})
		if requestHeaders.Get("X-Request") != "input" {
			t.Fatalf("request headers mutated: %#v", requestHeaders)
		}
		if responseHeaders.Get("X-Response") != "input" {
			t.Fatalf("response headers mutated: %#v", responseHeaders)
		}
		if string(originalRequest) != "original" {
			t.Fatalf("original request mutated: %q", originalRequest)
		}
		if string(requestBody) != "request" {
			t.Fatalf("request body mutated: %q", requestBody)
		}
		if string(body) != "chunk" {
			t.Fatalf("stream body mutated: %q", body)
		}
		if string(history[0]) != "first" {
			t.Fatalf("history mutated: %#v", history)
		}
		if metadata["key"] != "value" {
			t.Fatalf("stream metadata mutated: %#v", metadata)
		}
		if metadata["nested"].(map[string]any)["value"] != "original" || metadata["items"].([]any)[0].(map[string]any)["value"] != "original" {
			t.Fatalf("stream nested metadata mutated: %#v", metadata)
		}
		if metadata["strings"].([]string)[0] != "original" || string(metadata["bytes"].([]byte)) != "original" || metadata["labels"].(map[string]string)["name"] != "original" {
			t.Fatalf("stream nested metadata aliases mutated: %#v", metadata)
		}
		if metadata["values"].(url.Values)["name"][0] != "original" || metadata["mapSlice"].(map[string][]string)["name"][0] != "original" {
			t.Fatalf("stream map/slice metadata mutated: %#v", metadata)
		}
		if metadata["sliceMap"].([]map[string]string)[0]["name"] != "original" || metadata["aliasMap"].(stringSliceAlias)[0] != "original" || metadata["aliasList"].(mapSliceAlias)[0]["name"] != "original" {
			t.Fatalf("stream alias metadata mutated: %#v", metadata)
		}
		if !strings.HasSuffix(string(got.Body), "|ok") {
			t.Fatalf("stream result body = %q", got.Body)
		}
	})

	t.Run("pointers-and-cycle", func(t *testing.T) {
		type pointerMetadata struct {
			Value string
			Items []string
		}

		structValue := &pointerMetadata{Value: "original", Items: []string{"original"}}
		mapValue := &map[string][]string{"names": []string{"original"}}
		sliceValue := &[]string{"original"}
		aliasMapValue := &mapSliceAlias{{"name": "original"}}
		var ifaceValue any = &pointerMetadata{Value: "original", Items: []string{"original"}}
		cycle := map[string]any{}
		cycle["self"] = cycle

		metadata := map[string]any{
			"struct_ptr": structValue,
			"map_ptr":    mapValue,
			"slice_ptr":  sliceValue,
			"alias_ptr":  aliasMapValue,
			"iface_ptr":  ifaceValue,
			"cycle":      cycle,
		}

		host := newHostWithRecords(capabilityRecord{
			id: "pointer",
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				RequestInterceptor: requestInterceptorFunc(func(ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
					req.Metadata["struct_ptr"].(*pointerMetadata).Value = "mutated"
					req.Metadata["struct_ptr"].(*pointerMetadata).Items[0] = "mutated"
					(*req.Metadata["map_ptr"].(*map[string][]string))["names"][0] = "mutated"
					(*req.Metadata["slice_ptr"].(*[]string))[0] = "mutated"
					(*req.Metadata["alias_ptr"].(*mapSliceAlias))[0]["name"] = "mutated"
					req.Metadata["iface_ptr"].(*pointerMetadata).Value = "mutated"
					if clonedCycle, ok := req.Metadata["cycle"].(map[string]any); ok {
						clonedCycle["marker"] = "mutated"
						clonedCycle["self"] = "mutated"
					}
					return pluginapi.RequestInterceptResponse{Body: []byte("ok")}, nil
				}),
			}},
		})

		_ = host.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{Metadata: metadata})

		if structValue.Value != "original" || structValue.Items[0] != "original" {
			t.Fatalf("struct pointer metadata mutated: %#v", structValue)
		}
		if (*mapValue)["names"][0] != "original" {
			t.Fatalf("map pointer metadata mutated: %#v", mapValue)
		}
		if (*sliceValue)[0] != "original" {
			t.Fatalf("slice pointer metadata mutated: %#v", sliceValue)
		}
		if (*aliasMapValue)[0]["name"] != "original" {
			t.Fatalf("alias pointer metadata mutated: %#v", aliasMapValue)
		}
		if ifaceStruct, ok := ifaceValue.(*pointerMetadata); !ok || ifaceStruct.Value != "original" || ifaceStruct.Items[0] != "original" {
			t.Fatalf("interface pointer metadata mutated: %#v", ifaceValue)
		}
		if _, ok := cycle["self"].(map[string]any); !ok {
			t.Fatalf("cycle metadata structure changed unexpectedly: %#v", cycle)
		}
		if _, ok := cycle["marker"]; ok {
			t.Fatalf("cycle metadata mutated: %#v", cycle)
		}
	})
}

func TestResponseHooksKeepPayloadOrTryNextOnErrorAndEmptyBody(t *testing.T) {
	normalizerHost := newHostWithRecords(
		capabilityRecord{
			id:       "before-error",
			priority: 30,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ResponseBeforeTranslator: responseNormalizerFunc(func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
					return pluginapi.PayloadResponse{}, fmt.Errorf("before failed")
				}),
				ResponseAfterTranslator: responseNormalizerFunc(func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
					return pluginapi.PayloadResponse{}, fmt.Errorf("after failed")
				}),
			}},
		},
		capabilityRecord{
			id:       "before-empty",
			priority: 20,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ResponseBeforeTranslator: responseNormalizerFunc(func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
					return pluginapi.PayloadResponse{}, nil
				}),
				ResponseAfterTranslator: responseNormalizerFunc(func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
					return pluginapi.PayloadResponse{}, nil
				}),
			}},
		},
		capabilityRecord{
			id:       "before-success",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ResponseBeforeTranslator: responseNormalizerFunc(func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
					return pluginapi.PayloadResponse{Body: []byte("before-success")}, nil
				}),
				ResponseAfterTranslator: responseNormalizerFunc(func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
					return pluginapi.PayloadResponse{Body: []byte("after-success")}, nil
				}),
			}},
		},
	)

	before := normalizerHost.NormalizeResponseBefore(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, "model", nil, nil, []byte("original"), false)
	if string(before) != "before-success" {
		t.Fatalf("NormalizeResponseBefore() = %q, want %q", before, "before-success")
	}
	after := normalizerHost.NormalizeResponseAfter(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, "model", nil, nil, []byte("original"), false)
	if string(after) != "after-success" {
		t.Fatalf("NormalizeResponseAfter() = %q, want %q", after, "after-success")
	}

	translatorHost := newHostWithRecords(
		capabilityRecord{
			id:       "translator-error",
			priority: 30,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ResponseTranslator: responseTranslatorFunc(func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
					return pluginapi.PayloadResponse{}, fmt.Errorf("translate failed")
				}),
			}},
		},
		capabilityRecord{
			id:       "translator-empty",
			priority: 20,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ResponseTranslator: responseTranslatorFunc(func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
					return pluginapi.PayloadResponse{}, nil
				}),
			}},
		},
		capabilityRecord{
			id:       "translator-success",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ResponseTranslator: responseTranslatorFunc(func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
					return pluginapi.PayloadResponse{Body: []byte("response-translated")}, nil
				}),
			}},
		},
	)

	translated, ok := translatorHost.TranslateResponse(context.Background(), sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, "model", nil, nil, []byte("original"), false)
	if !ok {
		t.Fatal("TranslateResponse() ok = false, want true")
	}
	if string(translated) != "response-translated" {
		t.Fatalf("TranslateResponse() = %q, want %q", translated, "response-translated")
	}
}

func TestUsageAdapterPanicFusesPlugin(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "usage-panic",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			UsagePlugin: usagePluginFunc(func(ctx context.Context, record pluginapi.UsageRecord) {
				panic("usage panic")
			}),
		}},
	})
	adapter := &usageAdapter{
		host:     host,
		pluginID: "usage-panic",
	}

	adapter.HandleUsage(context.Background(), coreusage.Record{Provider: "plugin-provider"})
	if !host.isPluginFused("usage-panic") {
		t.Fatal("usage-panic was not fused")
	}
}

func TestUsageManagerRegisterNamedReplacesWithoutDuplicateDispatch(t *testing.T) {
	manager := coreusage.NewManager(0)
	defer manager.Stop()

	calls := make(chan string, 2)
	manager.RegisterNamed("plugin:alpha", coreUsagePluginFunc(func(ctx context.Context, record coreusage.Record) {
		calls <- "first"
	}))
	manager.RegisterNamed("plugin:alpha", coreUsagePluginFunc(func(ctx context.Context, record coreusage.Record) {
		calls <- "second"
	}))

	manager.Publish(context.Background(), coreusage.Record{Provider: "provider"})

	select {
	case got := <-calls:
		if got != "second" {
			t.Fatalf("first dispatch = %q, want second", got)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for usage dispatch")
	}
	select {
	case got := <-calls:
		t.Fatalf("unexpected duplicate dispatch from %q", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRegisterFrontendAuthProvidersPrunesStaleKeys(t *testing.T) {
	const key = "plugin:auth-active:custom-auth"
	sdkaccess.UnregisterProvider(key)
	defer sdkaccess.UnregisterProvider(key)

	host := newHostWithRecords(capabilityRecord{
		id: "auth-active",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			FrontendAuthProvider: frontendAuthProviderFunc{
				identifier: "custom-auth",
				authenticate: func(ctx context.Context, req pluginapi.FrontendAuthRequest) (pluginapi.FrontendAuthResponse, error) {
					return pluginapi.FrontendAuthResponse{Authenticated: true}, nil
				},
			},
		}},
	})

	host.RegisterFrontendAuthProviders()
	if !registeredProviderIdentifier(key) {
		t.Fatalf("registered providers did not include %q", key)
	}

	setHostSnapshotForTest(host, true)
	host.RegisterFrontendAuthProviders()
	if registeredProviderIdentifier(key) {
		t.Fatalf("registered providers still included stale key %q", key)
	}
}

func TestRegisterFrontendAuthProvidersIdentifierPanicFusesPlugin(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "auth-identifier-panic",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			FrontendAuthProvider: panicFrontendAuthProvider{},
		}},
	})

	host.RegisterFrontendAuthProviders()

	if !host.isPluginFused("auth-identifier-panic") {
		t.Fatal("auth-identifier-panic was not fused")
	}
}

func TestRegisterFrontendAuthProvidersSelectsHighestPriorityExclusiveProvider(t *testing.T) {
	lowKey := "plugin:exclusive-low:custom-auth"
	highKey := "plugin:exclusive-high:custom-auth"
	normalKey := "plugin:normal-auth:custom-auth"
	for _, key := range []string{lowKey, highKey, normalKey} {
		sdkaccess.UnregisterProvider(key)
		defer sdkaccess.UnregisterProvider(key)
	}
	sdkaccess.ClearExclusiveProvider()
	defer sdkaccess.ClearExclusiveProvider()

	host := newHostWithRecords(
		capabilityRecord{
			id:       "exclusive-low",
			priority: 1,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				FrontendAuthProvider:          frontendAuthProviderFunc{identifier: "custom-auth"},
				FrontendAuthProviderExclusive: true,
			}},
		},
		capabilityRecord{
			id:       "exclusive-high",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				FrontendAuthProvider:          frontendAuthProviderFunc{identifier: "custom-auth"},
				FrontendAuthProviderExclusive: true,
			}},
		},
		capabilityRecord{
			id:       "normal-auth",
			priority: 20,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				FrontendAuthProvider: frontendAuthProviderFunc{identifier: "custom-auth"},
			}},
		},
	)

	host.RegisterFrontendAuthProviders()

	providers := sdkaccess.RegisteredProviders()
	if len(providers) != 1 {
		t.Fatalf("RegisteredProviders() len = %d, want 1", len(providers))
	}
	if providers[0].Identifier() != highKey {
		t.Fatalf("exclusive provider = %q, want %q", providers[0].Identifier(), highKey)
	}
}

func TestRegisterFrontendAuthProvidersSelectsExclusiveProviderByPluginIDWhenPriorityTies(t *testing.T) {
	alphaKey := "plugin:alpha-auth:custom-auth"
	betaKey := "plugin:beta-auth:custom-auth"
	for _, key := range []string{alphaKey, betaKey} {
		sdkaccess.UnregisterProvider(key)
		defer sdkaccess.UnregisterProvider(key)
	}
	sdkaccess.ClearExclusiveProvider()
	defer sdkaccess.ClearExclusiveProvider()

	host := newHostWithRecords(
		capabilityRecord{
			id:       "beta-auth",
			priority: 5,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				FrontendAuthProvider:          frontendAuthProviderFunc{identifier: "custom-auth"},
				FrontendAuthProviderExclusive: true,
			}},
		},
		capabilityRecord{
			id:       "alpha-auth",
			priority: 5,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				FrontendAuthProvider:          frontendAuthProviderFunc{identifier: "custom-auth"},
				FrontendAuthProviderExclusive: true,
			}},
		},
	)

	host.RegisterFrontendAuthProviders()

	providers := sdkaccess.RegisteredProviders()
	if len(providers) != 1 {
		t.Fatalf("RegisteredProviders() len = %d, want 1", len(providers))
	}
	if providers[0].Identifier() != alphaKey {
		t.Fatalf("exclusive provider = %q, want %q", providers[0].Identifier(), alphaKey)
	}
}

func TestRegisterFrontendAuthProvidersClearsExclusiveProviderWhenExclusivePluginRemoved(t *testing.T) {
	exclusiveKey := "plugin:exclusive-auth:custom-auth"
	normalKey := "plugin:normal-auth:custom-auth"
	for _, key := range []string{exclusiveKey, normalKey} {
		sdkaccess.UnregisterProvider(key)
		defer sdkaccess.UnregisterProvider(key)
	}
	sdkaccess.ClearExclusiveProvider()
	defer sdkaccess.ClearExclusiveProvider()

	host := newHostWithRecords(
		capabilityRecord{
			id: "exclusive-auth",
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				FrontendAuthProvider:          frontendAuthProviderFunc{identifier: "custom-auth"},
				FrontendAuthProviderExclusive: true,
			}},
		},
		capabilityRecord{
			id: "normal-auth",
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				FrontendAuthProvider: frontendAuthProviderFunc{identifier: "custom-auth"},
			}},
		},
	)

	host.RegisterFrontendAuthProviders()
	if got := sdkaccess.RegisteredProviders(); len(got) != 1 || got[0].Identifier() != exclusiveKey {
		t.Fatalf("exclusive RegisteredProviders() = %#v, want only %q", got, exclusiveKey)
	}

	setHostSnapshotForTest(host, true, capabilityRecord{
		id: "normal-auth",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			FrontendAuthProvider: frontendAuthProviderFunc{identifier: "custom-auth"},
		}},
	})
	host.RegisterFrontendAuthProviders()

	providers := sdkaccess.RegisteredProviders()
	if len(providers) != 1 {
		t.Fatalf("RegisteredProviders() len = %d, want 1", len(providers))
	}
	if providers[0].Identifier() != normalKey {
		t.Fatalf("restored provider = %q, want %q", providers[0].Identifier(), normalKey)
	}
}

func TestRegisterFrontendAuthProvidersIgnoresExclusiveWithoutFrontendAuthProvider(t *testing.T) {
	normalKey := "plugin:normal-auth:custom-auth"
	sdkaccess.UnregisterProvider(normalKey)
	sdkaccess.ClearExclusiveProvider()
	defer sdkaccess.UnregisterProvider(normalKey)
	defer sdkaccess.ClearExclusiveProvider()

	host := newHostWithRecords(
		capabilityRecord{
			id: "exclusive-without-provider",
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				FrontendAuthProviderExclusive: true,
			}},
		},
		capabilityRecord{
			id: "normal-auth",
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				FrontendAuthProvider: frontendAuthProviderFunc{identifier: "custom-auth"},
			}},
		},
	)

	host.RegisterFrontendAuthProviders()

	providers := sdkaccess.RegisteredProviders()
	if len(providers) != 1 {
		t.Fatalf("RegisteredProviders() len = %d, want 1", len(providers))
	}
	if providers[0].Identifier() != normalKey {
		t.Fatalf("provider = %q, want %q", providers[0].Identifier(), normalKey)
	}
}

func TestUsageAdapterUsesCurrentSnapshotCapability(t *testing.T) {
	oldCalls := 0
	newCalls := 0
	oldPlugin := usagePluginFunc(func(ctx context.Context, record pluginapi.UsageRecord) {
		oldCalls++
	})
	newPlugin := usagePluginFunc(func(ctx context.Context, record pluginapi.UsageRecord) {
		newCalls++
	})
	host := newHostWithRecords(capabilityRecord{
		id: "usage-active",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			UsagePlugin: oldPlugin,
		}},
	})
	adapter := &usageAdapter{
		host:     host,
		pluginID: "usage-active",
		plugin:   oldPlugin,
	}
	setHostSnapshotForTest(host, true, capabilityRecord{
		id: "usage-active",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			UsagePlugin: newPlugin,
		}},
	})

	adapter.HandleUsage(context.Background(), coreusage.Record{Provider: "provider"})

	if oldCalls != 0 {
		t.Fatalf("old usage plugin calls = %d, want 0", oldCalls)
	}
	if newCalls != 1 {
		t.Fatalf("new usage plugin calls = %d, want 1", newCalls)
	}
}

func TestRegisterUsagePluginsStaleAdapterSkipsRemovedCapability(t *testing.T) {
	calls := 0
	plugin := usagePluginFunc(func(ctx context.Context, record pluginapi.UsageRecord) {
		calls++
	})
	host := newHostWithRecords(capabilityRecord{
		id: "usage-active",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			UsagePlugin: plugin,
		}},
	})

	host.RegisterUsagePlugins()
	adapter := &usageAdapter{
		host:     host,
		pluginID: "usage-active",
		plugin:   plugin,
	}
	setHostSnapshotForTest(host, true)
	adapter.HandleUsage(context.Background(), coreusage.Record{Provider: "provider"})

	if calls != 0 {
		t.Fatalf("usage plugin calls = %d, want 0 after capability removal", calls)
	}
}

func TestAccessAdapterAuthenticateFailures(t *testing.T) {
	tests := []struct {
		name             string
		pluginID         string
		method           string
		url              string
		body             io.ReadCloser
		authenticate     func(*testing.T, pluginapi.FrontendAuthRequest) (pluginapi.FrontendAuthResponse, error)
		wantCode         sdkaccess.AuthErrorCode
		wantCalled       bool
		wantFused        bool
		wantRestoredBody string
	}{
		{
			name:     "unauthenticated",
			pluginID: "auth-plugin",
			method:   http.MethodGet,
			url:      "http://example.test/v1/models",
			authenticate: func(t *testing.T, req pluginapi.FrontendAuthRequest) (pluginapi.FrontendAuthResponse, error) {
				return pluginapi.FrontendAuthResponse{Authenticated: false}, nil
			},
			wantCode:   sdkaccess.AuthErrorCodeNotHandled,
			wantCalled: true,
		},
		{
			name:     "panic",
			pluginID: "auth-panic",
			method:   http.MethodGet,
			url:      "http://example.test/v1/models",
			authenticate: func(t *testing.T, req pluginapi.FrontendAuthRequest) (pluginapi.FrontendAuthResponse, error) {
				panic("auth panic")
			},
			wantCode:   sdkaccess.AuthErrorCodeNotHandled,
			wantCalled: true,
			wantFused:  true,
		},
		{
			name:     "body read failure",
			pluginID: "auth-plugin",
			method:   http.MethodPost,
			url:      "http://example.test/v1/chat",
			body:     failingReadCloser{},
			authenticate: func(t *testing.T, req pluginapi.FrontendAuthRequest) (pluginapi.FrontendAuthResponse, error) {
				return pluginapi.FrontendAuthResponse{Authenticated: true}, nil
			},
			wantCode: sdkaccess.AuthErrorCodeInternal,
		},
		{
			name:     "provider error restores body",
			pluginID: "auth-plugin",
			method:   http.MethodPost,
			url:      "http://example.test/v1/chat?x=1",
			body:     io.NopCloser(bytes.NewBufferString("request-body")),
			authenticate: func(t *testing.T, req pluginapi.FrontendAuthRequest) (pluginapi.FrontendAuthResponse, error) {
				if string(req.Body) != "request-body" {
					t.Fatalf("plugin request body = %q, want %q", req.Body, "request-body")
				}
				return pluginapi.FrontendAuthResponse{}, fmt.Errorf("not mine")
			},
			wantCode:         sdkaccess.AuthErrorCodeNotHandled,
			wantCalled:       true,
			wantRestoredBody: "request-body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := New()
			called := false
			adapter := newAccessAdapterForTest(host, tt.pluginID, frontendAuthProviderFunc{
				identifier: "custom-auth",
				authenticate: func(ctx context.Context, req pluginapi.FrontendAuthRequest) (pluginapi.FrontendAuthResponse, error) {
					called = true
					return tt.authenticate(t, req)
				},
			})
			req, errNewRequest := http.NewRequest(tt.method, tt.url, tt.body)
			if errNewRequest != nil {
				t.Fatalf("NewRequest() error = %v", errNewRequest)
			}

			result, authErr := adapter.Authenticate(context.Background(), req)
			if result != nil {
				t.Fatalf("Authenticate() result = %#v, want nil", result)
			}
			if !sdkaccess.IsAuthErrorCode(authErr, tt.wantCode) {
				t.Fatalf("Authenticate() error = %v, want code %s", authErr, tt.wantCode)
			}
			if called != tt.wantCalled {
				t.Fatalf("provider called = %v, want %v", called, tt.wantCalled)
			}
			if tt.wantFused && !host.isPluginFused(tt.pluginID) {
				t.Fatalf("%s was not fused", tt.pluginID)
			}
			if tt.wantRestoredBody != "" {
				restored, errReadAll := io.ReadAll(req.Body)
				if errReadAll != nil {
					t.Fatalf("ReadAll(restored body) error = %v", errReadAll)
				}
				if string(restored) != tt.wantRestoredBody {
					t.Fatalf("restored body = %q, want %q", restored, tt.wantRestoredBody)
				}
			}
		})
	}
}

func TestExecutorAdapterMethods(t *testing.T) {
	streamChunks := make(chan pluginapi.ExecutorStreamChunk, 2)
	streamErr := errors.New("stream failed")
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("stream-1")}
	streamChunks <- pluginapi.ExecutorStreamChunk{Err: streamErr}
	close(streamChunks)

	pluginHTTPBody := []byte("http-response")
	pluginHTTPHeaders := http.Header{"X-Http": []string{"1"}}
	authProvider := fakeAuthProvider{
		identifier: "plugin-provider",
		refreshAuth: func(ctx context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
			if req.AuthID != "auth-1" || req.AuthProvider != "plugin-provider" || req.Metadata["old"] != "value" {
				t.Fatalf("refresh request = %#v, want auth metadata", req)
			}
			if req.HTTPClient == nil {
				t.Fatal("refresh request HTTPClient = nil, want host HTTP bridge")
			}
			return pluginapi.AuthRefreshResponse{
				Auth: pluginapi.AuthData{
					Metadata: map[string]any{"token": "new"},
				},
			}, nil
		},
	}
	executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin"})
	host := newHostWithRecords(
		capabilityRecord{
			id: "auth-plugin",
			plugin: pluginapi.Plugin{
				Capabilities: pluginapi.Capabilities{
					AuthProvider: authProvider,
				},
			},
		},
		executorRecord,
	)

	exec := &fakeExecutor{
		identifier: "ignored-by-adapter",
		execute: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			assertExecutorRequest(t, req)
			return pluginapi.ExecutorResponse{
				Payload: []byte("execute-response"),
				Headers: http.Header{"X-Execute": []string{"1"}},
				Metadata: map[string]any{
					"phase": "execute",
				},
			}, nil
		},
		executeStream: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			assertExecutorRequest(t, req)
			return pluginapi.ExecutorStreamResponse{
				Headers: http.Header{"X-Stream": []string{"1"}},
				Chunks:  streamChunks,
			}, nil
		},
		countTokens: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			assertExecutorRequest(t, req)
			return pluginapi.ExecutorResponse{Payload: []byte(`{"total_tokens":3}`)}, nil
		},
		httpRequest: func(ctx context.Context, req pluginapi.ExecutorHTTPRequest) (pluginapi.ExecutorHTTPResponse, error) {
			if req.AuthID != "auth-1" || req.AuthProvider != "plugin-provider" || req.Method != http.MethodPatch ||
				req.URL != "http://example.test/v1/raw?x=1" || req.Headers.Get("X-Raw") != "yes" || string(req.Body) != "raw-body" {
				t.Fatalf("http request = %#v, want mapped raw HTTP request", req)
			}
			if req.HTTPClient == nil {
				t.Fatal("http request HTTPClient = nil, want host HTTP bridge")
			}
			return pluginapi.ExecutorHTTPResponse{
				StatusCode: http.StatusAccepted,
				Headers:    pluginHTTPHeaders,
				Body:       pluginHTTPBody,
			}, nil
		},
	}
	adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	auth := &coreauth.Auth{
		ID:       "auth-1",
		Provider: "plugin-provider",
		Metadata: map[string]any{"old": "value"},
	}
	req := coreexecutor.Request{
		Model:   "model-1",
		Format:  sdktranslator.FormatOpenAI,
		Payload: []byte("payload"),
		Metadata: map[string]any{
			"req": "metadata",
		},
	}
	opts := coreexecutor.Options{
		Stream:          true,
		Alt:             "alt",
		Headers:         http.Header{"X-Request": []string{"yes"}},
		OriginalRequest: []byte("original"),
		SourceFormat:    sdktranslator.FormatOpenAI,
		Metadata: map[string]any{
			"opt": "metadata",
		},
	}

	if adapter.Identifier() != "plugin-provider" {
		t.Fatalf("Identifier() = %q, want %q", adapter.Identifier(), "plugin-provider")
	}
	resp, errExecute := adapter.Execute(context.Background(), auth, req, opts)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if string(resp.Payload) != "execute-response" || resp.Headers.Get("X-Execute") != "1" || resp.Metadata["phase"] != "execute" {
		t.Fatalf("Execute() = %#v, want mapped response", resp)
	}

	stream, errExecuteStream := adapter.ExecuteStream(context.Background(), auth, req, opts)
	if errExecuteStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecuteStream)
	}
	if stream.Headers.Get("X-Stream") != "1" {
		t.Fatalf("ExecuteStream() headers = %#v, want X-Stream", stream.Headers)
	}
	first := <-stream.Chunks
	if string(first.Payload) != "stream-1" || first.Err != nil {
		t.Fatalf("first stream chunk = %#v, want payload chunk", first)
	}
	second := <-stream.Chunks
	if second.Err != streamErr {
		t.Fatalf("second stream chunk err = %v, want %v", second.Err, streamErr)
	}
	if _, ok := <-stream.Chunks; ok {
		t.Fatal("stream chunks channel still open, want closed")
	}

	refreshed, errRefresh := adapter.Refresh(context.Background(), auth)
	if errRefresh != nil {
		t.Fatalf("Refresh() error = %v", errRefresh)
	}
	if refreshed == auth {
		t.Fatal("Refresh() returned original auth pointer, want clone")
	}
	if refreshed.Metadata["token"] != "new" {
		t.Fatalf("Refresh() metadata = %#v, want token=new", refreshed.Metadata)
	}

	count, errCountTokens := adapter.CountTokens(context.Background(), auth, req, opts)
	if errCountTokens != nil {
		t.Fatalf("CountTokens() error = %v", errCountTokens)
	}
	if string(count.Payload) != `{"total_tokens":3}` {
		t.Fatalf("CountTokens() payload = %q, want token payload", count.Payload)
	}

	rawReq, errNewRawRequest := http.NewRequest(http.MethodPatch, "http://example.test/v1/raw?x=1", bytes.NewBufferString("raw-body"))
	if errNewRawRequest != nil {
		t.Fatalf("NewRequest(raw) error = %v", errNewRawRequest)
	}
	rawReq.Header.Set("X-Raw", "yes")
	httpResp, errHTTPRequest := adapter.HttpRequest(context.Background(), auth, rawReq)
	if errHTTPRequest != nil {
		t.Fatalf("HttpRequest() error = %v", errHTTPRequest)
	}
	if httpResp.StatusCode != http.StatusAccepted || httpResp.Status != "202 Accepted" || httpResp.Header.Get("X-Http") != "1" {
		t.Fatalf("HttpRequest() response = %#v, want mapped status/header", httpResp)
	}
	pluginHTTPBody[0] = 'X'
	pluginHTTPHeaders.Set("X-Http", "mutated")
	body, errReadBody := io.ReadAll(httpResp.Body)
	if errReadBody != nil {
		t.Fatalf("ReadAll(HttpRequest body) error = %v", errReadBody)
	}
	if string(body) != "http-response" || httpResp.Header.Get("X-Http") != "1" {
		t.Fatalf("HttpRequest() response aliases plugin data: body=%q header=%q", body, httpResp.Header.Get("X-Http"))
	}
	restoredRawBody, errReadRawBody := io.ReadAll(rawReq.Body)
	if errReadRawBody != nil {
		t.Fatalf("ReadAll(restored raw request body) error = %v", errReadRawBody)
	}
	if string(restoredRawBody) != "raw-body" {
		t.Fatalf("restored raw request body = %q, want raw-body", restoredRawBody)
	}

	nilResp, errNilRequest := adapter.HttpRequest(context.Background(), auth, nil)
	if nilResp != nil {
		t.Fatalf("HttpRequest(nil) response = %#v, want nil", nilResp)
	}
	if errNilRequest == nil || !strings.Contains(errNilRequest.Error(), "nil HTTP request") {
		t.Fatalf("HttpRequest(nil) error = %v, want nil request error", errNilRequest)
	}
}

func TestExecutorAdapterUsesResponseFormatForOutputTranslation(t *testing.T) {
	claudeResponse := []byte(`{"id":"msg_1","type":"message","model":"claude-test","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	openAIRequest := []byte(`{"model":"model-1","messages":[{"role":"user","content":"hi"}]}`)

	var captured pluginapi.ExecutorRequest
	host := New()
	adapter := newCurrentExecutorAdapterForTest(host, "executor-plugin", &fakeExecutor{
		execute: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			captured = req
			return pluginapi.ExecutorResponse{Payload: claudeResponse}, nil
		},
	},
		[]sdktranslator.Format{sdktranslator.FormatClaude},
		[]sdktranslator.Format{sdktranslator.FormatClaude},
	)

	resp, errExecute := adapter.Execute(context.Background(), &coreauth.Auth{}, coreexecutor.Request{
		Model:   "model-1",
		Format:  sdktranslator.FormatOpenAI,
		Payload: openAIRequest,
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if captured.SourceFormat != sdktranslator.FormatClaude.String() {
		t.Fatalf("executor SourceFormat = %q, want %q", captured.SourceFormat, sdktranslator.FormatClaude)
	}
	if captured.Format != sdktranslator.FormatClaude.String() {
		t.Fatalf("executor Format = %q, want %q", captured.Format, sdktranslator.FormatClaude)
	}
	if bytes.Equal(captured.Payload, openAIRequest) || !bytes.Contains(captured.Payload, []byte(`"max_tokens":32000`)) {
		t.Fatalf("executor payload = %s, want translated Claude request", captured.Payload)
	}
	if !bytes.Equal(resp.Payload, claudeResponse) {
		t.Fatalf("Execute() payload = %s, want Claude response payload %s", resp.Payload, claudeResponse)
	}
}

func TestExecutorAdapterSelectsCustomOutputWithHostResponseTranslator(t *testing.T) {
	customOutputFormat := sdktranslator.Format("plugin-custom-output")
	requestedFormat := sdktranslator.FormatOpenAI
	body := []byte("plugin-body")
	translatedBody := []byte("translated-body")
	var captured pluginapi.ResponseTransformRequest

	executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin"})
	host := newHostWithRecords(
		capabilityRecord{
			id: "response-translator",
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ResponseTranslator: responseTranslatorFunc(func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
					captured = req
					return pluginapi.PayloadResponse{Body: translatedBody}, nil
				}),
			}},
		},
		executorRecord,
	)
	sdktranslator.SetPluginHooks(host)
	t.Cleanup(func() {
		sdktranslator.SetPluginHooks(nil)
	})

	adapter := newExecutorAdapterForRecordForTest(host, executorRecord, &fakeExecutor{
		execute: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			if req.Format != customOutputFormat.String() {
				t.Fatalf("executor Format = %q, want %q", req.Format, customOutputFormat)
			}
			return pluginapi.ExecutorResponse{Payload: body}, nil
		},
	},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{customOutputFormat},
	)

	resp, errExecute := adapter.Execute(context.Background(), &coreauth.Auth{}, coreexecutor.Request{
		Model:   "model-1",
		Format:  sdktranslator.FormatOpenAI,
		Payload: []byte(`{"model":"model-1"}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: requestedFormat,
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if !bytes.Equal(resp.Payload, translatedBody) {
		t.Fatalf("Execute() payload = %q, want %q", resp.Payload, translatedBody)
	}
	if captured.FromFormat != customOutputFormat.String() || captured.ToFormat != requestedFormat.String() {
		t.Fatalf("translator formats = %q -> %q, want %q -> %q", captured.FromFormat, captured.ToFormat, customOutputFormat, requestedFormat)
	}
	if captured.Stream {
		t.Fatal("translator Stream = true, want false")
	}
	if !bytes.Equal(captured.Body, body) {
		t.Fatalf("translator body = %q, want %q", captured.Body, body)
	}
}

func TestExecutorAdapterConsumesTranslatedStreamChunksWithoutOutput(t *testing.T) {
	adapter := &executorAdapter{}
	request := []byte(`{"model":"qmodel_latest","stream":true,"tool_choice":"auto","parallel_tool_calls":true}`)
	prepared := preparedExecutorCall{
		req: coreexecutor.Request{
			Model:   "qmodel_latest",
			Payload: request,
		},
		opts: coreexecutor.Options{
			OriginalRequest: request,
		},
		requestedFormat: sdktranslator.FormatOpenAIResponse,
		outputFormat:    sdktranslator.FormatOpenAI,
	}
	var param any

	startPayload := []byte(`{"choices":[{"delta":{"content":"","tool_calls":[{"function":{"arguments":"","name":"get_weather"},"id":"call_69755759d70640e3b7a42805","index":0,"type":"function"}]},"index":0}],"created":1780767281,"id":"chatcmpl-ba492ed2-2901-9d1f-80e7-b6dfe97fefaa","model":"auto","object":"chat.completion.chunk"}`)
	if got := adapter.translateExecutorStreamPayload(context.Background(), prepared, startPayload, &param); len(got) == 0 {
		t.Fatal("tool call start payload was not translated")
	}

	emptyArgumentsPayload := []byte(`{"choices":[{"delta":{"content":"","tool_calls":[{"function":{"arguments":""},"id":"","index":0,"type":"function"}]},"index":0}],"created":1780767281,"id":"chatcmpl-ba492ed2-2901-9d1f-80e7-b6dfe97fefaa","model":"auto","object":"chat.completion.chunk"}`)
	if got := adapter.translateExecutorStreamPayload(context.Background(), prepared, emptyArgumentsPayload, &param); len(got) != 0 {
		t.Fatalf("empty arguments payload leaked through translation fallback: %q", got[0])
	}

	finishPayload := []byte(`{"choices":[{"delta":{},"finish_reason":"tool_calls","index":0}],"created":1780767281,"id":"chatcmpl-ba492ed2-2901-9d1f-80e7-b6dfe97fefaa","model":"auto","object":"chat.completion.chunk"}`)
	if got := adapter.translateExecutorStreamPayload(context.Background(), prepared, finishPayload, &param); len(got) == 0 {
		t.Fatal("finish payload was not translated")
	}

	usagePayload := []byte(`{"choices":[],"created":1780767281,"id":"chatcmpl-ba492ed2-2901-9d1f-80e7-b6dfe97fefaa","model":"auto","object":"chat.completion.chunk","usage":{"completion_tokens":179,"completion_tokens_details":{"reasoning_tokens":121},"prompt_tokens":331,"prompt_tokens_details":{"cached_tokens":0},"total_tokens":510}}`)
	if got := adapter.translateExecutorStreamPayload(context.Background(), prepared, usagePayload, &param); len(got) != 0 {
		t.Fatalf("usage-only payload leaked through translation fallback: %q", got[0])
	}

	donePayload := []byte(`data: [DONE]`)
	doneFrames := adapter.translateExecutorStreamPayload(context.Background(), prepared, donePayload, &param)
	if len(doneFrames) != 1 {
		t.Fatalf("done payload translated to %d frames, want 1", len(doneFrames))
	}
	if !bytes.Contains(doneFrames[0], []byte("response.completed")) {
		t.Fatalf("done payload did not produce response.completed: %q", doneFrames[0])
	}
	if !bytes.Contains(doneFrames[0], []byte(`"input_tokens":331`)) ||
		!bytes.Contains(doneFrames[0], []byte(`"output_tokens":179`)) ||
		!bytes.Contains(doneFrames[0], []byte(`"reasoning_tokens":121`)) ||
		!bytes.Contains(doneFrames[0], []byte(`"total_tokens":510`)) {
		t.Fatalf("completed payload did not preserve usage: %q", doneFrames[0])
	}
}

func TestExecutorAdapterKeepsRawStreamFallbackWithOnlyHostResponseTranslator(t *testing.T) {
	customOutputFormat := sdktranslator.Format("plugin-custom-stream-output")
	requestedFormat := sdktranslator.FormatOpenAI
	payload := []byte(`{"custom":"chunk"}`)
	host := newHostWithRecords(capabilityRecord{
		id: "empty-response-translator",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ResponseTranslator: responseTranslatorFunc(func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
				return pluginapi.PayloadResponse{}, nil
			}),
		}},
	})
	sdktranslator.SetPluginHooks(host)
	t.Cleanup(func() {
		sdktranslator.SetPluginHooks(nil)
	})
	adapter := &executorAdapter{
		host: host,
	}
	prepared := preparedExecutorCall{
		req: coreexecutor.Request{
			Model:   "model-1",
			Payload: []byte(`{"model":"model-1"}`),
		},
		opts: coreexecutor.Options{
			OriginalRequest: []byte(`{"model":"model-1","stream":true}`),
		},
		requestedFormat: requestedFormat,
		outputFormat:    customOutputFormat,
	}
	var param any

	frames := adapter.translateExecutorStreamPayload(context.Background(), prepared, payload, &param)
	if len(frames) != 1 {
		t.Fatalf("translated stream frame count = %d, want 1", len(frames))
	}
	if !bytes.Equal(frames[0], payload) {
		t.Fatalf("translated stream frame = %q, want raw payload %q", frames[0], payload)
	}
}

func TestExecutorAdapterPanicFusesAndReturnsError(t *testing.T) {
	host := New()
	calls := 0
	adapter := newCurrentExecutorAdapterForTest(host, "executor-panic", &fakeExecutor{
		execute: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			calls++
			panic("execute panic")
		},
		countTokens: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			calls++
			return pluginapi.ExecutorResponse{Payload: []byte("should-not-run")}, nil
		},
	},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)

	resp, errExecute := adapter.Execute(context.Background(), &coreauth.Auth{}, coreexecutor.Request{}, coreexecutor.Options{})
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want panic converted to error")
	}
	if len(resp.Payload) != 0 {
		t.Fatalf("Execute() response = %#v, want zero response", resp)
	}
	if !host.isPluginFused("executor-panic") {
		t.Fatal("executor-panic was not fused")
	}
	if calls != 1 {
		t.Fatalf("plugin calls after first Execute() = %d, want 1", calls)
	}

	count, errCountTokens := adapter.CountTokens(context.Background(), &coreauth.Auth{}, coreexecutor.Request{}, coreexecutor.Options{})
	if errCountTokens == nil {
		t.Fatal("CountTokens() error after fuse = nil, want unavailable error")
	}
	if len(count.Payload) != 0 {
		t.Fatalf("CountTokens() response after fuse = %#v, want zero response", count)
	}
	if calls != 1 {
		t.Fatalf("plugin calls after fused CountTokens() = %d, want 1", calls)
	}
}

func TestMapExecutorStreamChunksExitsWhenContextCanceledWithoutDownstreamConsumer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	in := make(chan pluginapi.ExecutorStreamChunk)
	out := mapExecutorStreamChunks(ctx, in)
	sent := make(chan struct{})

	go func() {
		in <- pluginapi.ExecutorStreamChunk{Payload: []byte("chunk")}
		close(sent)
	}()

	select {
	case <-sent:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("input chunk was not accepted by bridge")
	}
	cancel()
	time.Sleep(10 * time.Millisecond)

	select {
	case chunk, ok := <-out:
		if ok {
			t.Fatalf("output channel produced chunk after cancel: %#v", chunk)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("output channel was not closed after context cancellation")
	}
}

func newHostWithRecords(records ...capabilityRecord) *Host {
	host := New()
	setHostSnapshotForTest(host, true, records...)
	return host
}

func setHostSnapshotForTest(host *Host, enabled bool, records ...capabilityRecord) {
	records = normalizeTestCapabilityRecords(records)
	sortRecords(records)
	host.mu.Lock()
	host.rebuildActivePluginMapsLocked(records)
	host.snapshot.Store(&Snapshot{enabled: enabled, records: records})
	host.mu.Unlock()
}

func newAccessAdapterForTest(host *Host, pluginID string, provider pluginapi.FrontendAuthProvider) *accessAdapter {
	record := normalizeTestCapabilityRecord(capabilityRecord{id: pluginID})
	setHostSnapshotForTest(host, true, record)
	return &accessAdapter{
		host:     host,
		pluginID: pluginID,
		path:     record.path,
		version:  record.version,
		provider: provider,
	}
}

func newCurrentExecutorAdapterForTest(host *Host, pluginID string, executor pluginapi.ProviderExecutor, inputFormats, outputFormats []sdktranslator.Format) *executorAdapter {
	record := normalizeTestCapabilityRecord(capabilityRecord{id: pluginID})
	setHostSnapshotForTest(host, true, record)
	return newExecutorAdapterForRecordForTest(host, record, executor, inputFormats, outputFormats)
}

func newExecutorAdapterForRecordForTest(host *Host, record capabilityRecord, executor pluginapi.ProviderExecutor, inputFormats, outputFormats []sdktranslator.Format) *executorAdapter {
	record = normalizeTestCapabilityRecord(record)
	return &executorAdapter{
		host:          host,
		pluginID:      record.id,
		path:          record.path,
		version:       record.version,
		provider:      "plugin-provider",
		executor:      executor,
		inputFormats:  inputFormats,
		outputFormats: outputFormats,
	}
}

func normalizeTestCapabilityRecord(record capabilityRecord) capabilityRecord {
	id := strings.TrimSpace(record.id)
	if id == "" {
		return record
	}
	if strings.TrimSpace(record.path) == "" {
		record.path = fmt.Sprintf("testdata/%s.plugin", id)
	}
	if strings.TrimSpace(record.version) == "" {
		version := strings.TrimSpace(record.meta.Version)
		if version == "" {
			version = "test-version"
		}
		record.version = version
	}
	return record
}

func normalizeTestCapabilityRecords(records []capabilityRecord) []capabilityRecord {
	out := make([]capabilityRecord, len(records))
	copy(out, records)
	for i := range out {
		out[i] = normalizeTestCapabilityRecord(out[i])
	}
	return out
}

type stringSliceAlias []string

type mapSliceAlias []map[string]string

type requestNormalizerFunc func(context.Context, pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error)

func (f requestNormalizerFunc) NormalizeRequest(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
	return f(ctx, req)
}

type requestTranslatorFunc func(context.Context, pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error)

func (f requestTranslatorFunc) TranslateRequest(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
	return f(ctx, req)
}

type responseNormalizerFunc func(context.Context, pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error)

func (f responseNormalizerFunc) NormalizeResponse(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
	return f(ctx, req)
}

type responseTranslatorFunc func(context.Context, pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error)

func (f responseTranslatorFunc) TranslateResponse(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
	return f(ctx, req)
}

type usagePluginFunc func(context.Context, pluginapi.UsageRecord)

func (f usagePluginFunc) HandleUsage(ctx context.Context, record pluginapi.UsageRecord) {
	f(ctx, record)
}

type coreUsagePluginFunc func(context.Context, coreusage.Record)

func (f coreUsagePluginFunc) HandleUsage(ctx context.Context, record coreusage.Record) {
	f(ctx, record)
}

type frontendAuthProviderFunc struct {
	identifier   string
	authenticate func(context.Context, pluginapi.FrontendAuthRequest) (pluginapi.FrontendAuthResponse, error)
}

func (f frontendAuthProviderFunc) Identifier() string {
	return f.identifier
}

func (f frontendAuthProviderFunc) Authenticate(ctx context.Context, req pluginapi.FrontendAuthRequest) (pluginapi.FrontendAuthResponse, error) {
	return f.authenticate(ctx, req)
}

type panicFrontendAuthProvider struct{}

func (panicFrontendAuthProvider) Identifier() string {
	panic("identifier panic")
}

func (panicFrontendAuthProvider) Authenticate(ctx context.Context, req pluginapi.FrontendAuthRequest) (pluginapi.FrontendAuthResponse, error) {
	return pluginapi.FrontendAuthResponse{}, nil
}

type fakeAuthProvider struct {
	identifier  string
	parseAuth   func(context.Context, pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error)
	startLogin  func(context.Context, pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error)
	pollLogin   func(context.Context, pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error)
	refreshAuth func(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error)
}

func (p fakeAuthProvider) Identifier() string {
	return p.identifier
}

func (p fakeAuthProvider) ParseAuth(ctx context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	if p.parseAuth == nil {
		return pluginapi.AuthParseResponse{}, nil
	}
	return p.parseAuth(ctx, req)
}

func (p fakeAuthProvider) StartLogin(ctx context.Context, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	if p.startLogin == nil {
		return pluginapi.AuthLoginStartResponse{}, nil
	}
	return p.startLogin(ctx, req)
}

func (p fakeAuthProvider) PollLogin(ctx context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
	if p.pollLogin == nil {
		return pluginapi.AuthLoginPollResponse{}, nil
	}
	return p.pollLogin(ctx, req)
}

func (p fakeAuthProvider) RefreshAuth(ctx context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	if p.refreshAuth == nil {
		return pluginapi.AuthRefreshResponse{}, nil
	}
	return p.refreshAuth(ctx, req)
}

type modelRegistrarFunc func(context.Context, pluginapi.ModelRegistrationRequest) (pluginapi.ModelRegistrationResponse, error)

func (f modelRegistrarFunc) RegisterModels(ctx context.Context, req pluginapi.ModelRegistrationRequest) (pluginapi.ModelRegistrationResponse, error) {
	return f(ctx, req)
}

type modelProviderFunc struct {
	staticModels  func(context.Context, pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error)
	modelsForAuth func(context.Context, pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error)
}

func (f modelProviderFunc) StaticModels(ctx context.Context, req pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
	if f.staticModels == nil {
		return pluginapi.ModelResponse{}, nil
	}
	return f.staticModels(ctx, req)
}

func (f modelProviderFunc) ModelsForAuth(ctx context.Context, req pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
	if f.modelsForAuth == nil {
		return pluginapi.ModelResponse{}, nil
	}
	return f.modelsForAuth(ctx, req)
}

func staticModelRegistrar(provider, modelID string) pluginapi.ModelRegistrar {
	return modelRegistrarFunc(func(ctx context.Context, req pluginapi.ModelRegistrationRequest) (pluginapi.ModelRegistrationResponse, error) {
		return pluginapi.ModelRegistrationResponse{
			Provider: provider,
			Models: []pluginapi.ModelInfo{{
				ID: modelID,
			}},
		}, nil
	})
}

func registeredProviderIdentifier(identifier string) bool {
	for _, provider := range sdkaccess.RegisteredProviders() {
		if provider != nil && provider.Identifier() == identifier {
			return true
		}
	}
	return false
}

type fakeModelRegistry struct {
	clients     map[string]*fakeModelClient
	unregisters []string
}

type fakeModelClient struct {
	provider string
	models   []*registry.ModelInfo
}

func newFakeModelRegistry() *fakeModelRegistry {
	return &fakeModelRegistry{
		clients: make(map[string]*fakeModelClient),
	}
}

func (r *fakeModelRegistry) RegisterClient(clientID, clientProvider string, models []*registry.ModelInfo) {
	r.clients[clientID] = &fakeModelClient{
		provider: clientProvider,
		models:   models,
	}
}

func (r *fakeModelRegistry) UnregisterClient(clientID string) {
	delete(r.clients, clientID)
	r.unregisters = append(r.unregisters, clientID)
}

func (r *fakeModelRegistry) GetModelProviders(modelID string) []string {
	counts := make(map[string]int)
	for _, client := range r.clients {
		if client == nil || client.provider == "" {
			continue
		}
		for _, model := range client.models {
			if model != nil && model.ID == modelID {
				counts[client.provider]++
			}
		}
	}
	providers := make([]string, 0, len(counts))
	for provider := range counts {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	return providers
}

type fakeExecutorManager struct {
	executors     map[string]coreauth.ProviderExecutor
	registerCalls int
	unregisters   []string
}

func newFakeExecutorManager() *fakeExecutorManager {
	return &fakeExecutorManager{
		executors: make(map[string]coreauth.ProviderExecutor),
	}
}

func (m *fakeExecutorManager) Executor(provider string) (coreauth.ProviderExecutor, bool) {
	executor, okExecutor := m.executors[provider]
	return executor, okExecutor
}

func (m *fakeExecutorManager) RegisterExecutor(executor coreauth.ProviderExecutor) {
	m.registerCalls++
	m.executors[executor.Identifier()] = executor
}

func (m *fakeExecutorManager) UnregisterExecutor(provider string) {
	delete(m.executors, provider)
	m.unregisters = append(m.unregisters, provider)
}

type fakeProviderExecutor struct {
	provider string
}

func (e *fakeProviderExecutor) Identifier() string {
	return e.provider
}

func (e *fakeProviderExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (e *fakeProviderExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, nil
}

func (e *fakeProviderExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *fakeProviderExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (e *fakeProviderExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

type fakeExecutor struct {
	identifier      string
	identifierFunc  func() string
	panicIdentifier bool
	execute         func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error)
	executeStream   func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error)
	countTokens     func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error)
	httpRequest     func(context.Context, pluginapi.ExecutorHTTPRequest) (pluginapi.ExecutorHTTPResponse, error)
}

func (e *fakeExecutor) Identifier() string {
	if e.panicIdentifier {
		panic("identifier panic")
	}
	if e.identifierFunc != nil {
		return e.identifierFunc()
	}
	return e.identifier
}

func (e *fakeExecutor) Execute(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	return e.execute(ctx, req)
}

func (e *fakeExecutor) ExecuteStream(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
	return e.executeStream(ctx, req)
}

func (e *fakeExecutor) CountTokens(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	return e.countTokens(ctx, req)
}

func (e *fakeExecutor) HttpRequest(ctx context.Context, req pluginapi.ExecutorHTTPRequest) (pluginapi.ExecutorHTTPResponse, error) {
	if e.httpRequest == nil {
		return pluginapi.ExecutorHTTPResponse{}, nil
	}
	return e.httpRequest(ctx, req)
}

func assertExecutorRequest(t *testing.T, req pluginapi.ExecutorRequest) {
	t.Helper()
	if req.AuthID != "auth-1" || req.AuthProvider != "plugin-provider" || req.Model != "model-1" || req.Format != sdktranslator.FormatOpenAI.String() ||
		!req.Stream || req.Alt != "alt" || req.Headers.Get("X-Request") != "yes" || string(req.OriginalRequest) != "original" ||
		req.SourceFormat != sdktranslator.FormatOpenAI.String() || string(req.Payload) != "payload" ||
		req.Metadata["req"] != "metadata" || req.Metadata["opt"] != "metadata" {
		t.Fatalf("executor request = %#v, want mapped request", req)
	}
}

type failingReadCloser struct{}

func (failingReadCloser) Read(p []byte) (int, error) {
	copy(p, []byte("partial"))
	return len("partial"), errors.New("read failed")
}

func (failingReadCloser) Close() error {
	return nil
}
