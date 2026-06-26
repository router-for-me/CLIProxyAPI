package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func newRouteModelHostWithRecords(records ...capabilityRecord) *Host {
	for i := range records {
		caps := &records[i].plugin.Capabilities
		if caps.Executor == nil {
			continue
		}
		if len(caps.ExecutorInputFormats) == 0 {
			caps.ExecutorInputFormats = []string{"openai"}
		}
		if len(caps.ExecutorOutputFormats) == 0 {
			caps.ExecutorOutputFormats = []string{"openai"}
		}
	}
	return newHostWithRecords(records...)
}

func TestHostRouteModelUsesHighestPriorityFirstMatch(t *testing.T) {
	var lowCalled bool
	host := newRouteModelHostWithRecords(
		capabilityRecord{
			id:       "low",
			priority: 1,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor: &fakeExecutor{identifier: "fake-provider"},
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					lowCalled = true
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf}, nil
				}),
			}},
		},
		capabilityRecord{
			id:       "high",
			priority: 10,
			meta:     pluginapi.Metadata{Name: "High Router"},
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor: &fakeExecutor{identifier: "fake-provider"},
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					if req.Plugin.Name != "High Router" {
						t.Fatalf("Plugin metadata = %#v, want High Router", req.Plugin)
					}
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf, Reason: "match"}, nil
				}),
			}},
		},
	)

	resp, ok := host.RouteModel(context.Background(), pluginapi.ModelRouteRequest{RequestedModel: "original-model"})
	if !ok || !resp.Handled || resp.Target != "high" || resp.Reason != "match" {
		t.Fatalf("RouteModel() = %#v, %v; want high executor handled", resp, ok)
	}
	if lowCalled {
		t.Fatal("low priority router was called after high priority match")
	}
}

func TestHostRouteModelContinuesAfterUnhandled(t *testing.T) {
	var lowCalled bool
	host := newRouteModelHostWithRecords(
		capabilityRecord{
			id:       "low",
			priority: 1,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor: &fakeExecutor{identifier: "fake-provider"},
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					lowCalled = true
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf}, nil
				}),
			}},
		},
		capabilityRecord{
			id:       "high",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor: &fakeExecutor{identifier: "fake-provider"},
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					return pluginapi.ModelRouteResponse{Handled: false}, nil
				}),
			}},
		},
	)

	resp, ok := host.RouteModel(context.Background(), pluginapi.ModelRouteRequest{RequestedModel: "original-model"})
	if !lowCalled {
		t.Fatal("low priority router was not called after unhandled high priority router")
	}
	if !ok || resp.Target != "low" {
		t.Fatalf("RouteModel() = %#v, %v; want low executor handled", resp, ok)
	}
}

func TestHostRouteModelAllowsExplicitExecutorPluginTarget(t *testing.T) {
	host := newRouteModelHostWithRecords(
		capabilityRecord{
			id: "executor",
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor: &fakeExecutor{identifier: "fake-provider"},
			}},
		},
		capabilityRecord{
			id:       "router",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					if req.PluginID != "router" {
						t.Fatalf("PluginID = %q, want router", req.PluginID)
					}
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: "executor"}, nil
				}),
			}},
		},
	)

	resp, ok := host.RouteModel(context.Background(), pluginapi.ModelRouteRequest{RequestedModel: "original-model"})
	if !ok || !resp.Handled || resp.Target != "executor" {
		t.Fatalf("RouteModel() = %#v, %v; want executor target handled", resp, ok)
	}
}

func TestHostExecutePluginExecutorByPluginIDPreservesModel(t *testing.T) {
	var gotReq pluginapi.ExecutorRequest
	executor := &fakeExecutor{
		identifier: "plugin-provider",
		execute: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			gotReq = req
			return pluginapi.ExecutorResponse{Payload: []byte("plugin-ok")}, nil
		},
	}
	host := newRouteModelHostWithRecords(capabilityRecord{
		id: "executor",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			Executor:              executor,
			ExecutorInputFormats:  []string{"openai"},
			ExecutorOutputFormats: []string{"openai"},
		}},
	})

	resp, errExecute := host.ExecutePluginExecutor(context.Background(), "executor", coreexecutor.Request{Model: "client-model", Payload: []byte(`{"model":"client-model"}`)}, coreexecutor.Options{OriginalRequest: []byte(`{"model":"client-model"}`)})
	if errExecute != nil {
		t.Fatalf("ExecutePluginExecutor() error = %v", errExecute)
	}
	if string(resp.Payload) != "plugin-ok" {
		t.Fatalf("payload = %q, want plugin-ok", resp.Payload)
	}
	if gotReq.AuthID != "" || gotReq.AuthProvider != "" {
		t.Fatalf("auth fields = %q/%q, want empty static executor auth", gotReq.AuthID, gotReq.AuthProvider)
	}
	if gotReq.Model != "client-model" {
		t.Fatalf("executor request model = %q, want client-model", gotReq.Model)
	}
}

func TestHostRouteModelDefaultsHandledRouterToOwnExecutor(t *testing.T) {
	host := newRouteModelHostWithRecords(capabilityRecord{
		id: "router",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			Executor: &fakeExecutor{identifier: "fake-provider"},
			ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
				return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf}, nil
			}),
		}},
	})

	resp, ok := host.RouteModel(context.Background(), pluginapi.ModelRouteRequest{RequestedModel: "original-model"})
	if !ok || resp.Target != "router" {
		t.Fatalf("RouteModel() = %#v, %v; want router executor handled", resp, ok)
	}
}

func TestHostRouteModelSkipsUnavailableExecutorTargets(t *testing.T) {
	calls := 0
	host := newRouteModelHostWithRecords(
		capabilityRecord{
			id:       "fallback",
			priority: 1,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor: &fakeExecutor{identifier: "fake-provider"},
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					calls++
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf}, nil
				}),
			}},
		},
		capabilityRecord{
			id:       "missing-target",
			priority: 20,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor: &fakeExecutor{identifier: "fake-provider"},
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					calls++
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: "missing"}, nil
				}),
			}},
		},
		capabilityRecord{
			id:       "no-executor",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					calls++
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf}, nil
				}),
			}},
		},
	)

	resp, ok := host.RouteModel(context.Background(), pluginapi.ModelRouteRequest{RequestedModel: "original-model"})
	if calls != 3 {
		t.Fatalf("router calls = %d, want all routers tried", calls)
	}
	if !ok || resp.Target != "fallback" {
		t.Fatalf("RouteModel() = %#v, %v; want fallback executor handled", resp, ok)
	}
}

func TestHostRouteModelErrorAndPanicDoNotBreakFallback(t *testing.T) {
	host := newRouteModelHostWithRecords(
		capabilityRecord{
			id:       "fallback",
			priority: 1,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor: &fakeExecutor{identifier: "fake-provider"},
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf}, nil
				}),
			}},
		},
		capabilityRecord{
			id:       "panic",
			priority: 20,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor: &fakeExecutor{identifier: "fake-provider"},
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					panic("router panic")
				}),
			}},
		},
		capabilityRecord{
			id:       "error",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor: &fakeExecutor{identifier: "fake-provider"},
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					return pluginapi.ModelRouteResponse{}, errors.New("temporary route failure")
				}),
			}},
		},
	)

	resp, ok := host.RouteModel(context.Background(), pluginapi.ModelRouteRequest{RequestedModel: "original-model"})
	if !ok || resp.Target != "fallback" {
		t.Fatalf("RouteModel() = %#v, %v; want fallback executor handled", resp, ok)
	}
	if !host.isPluginFused("panic") {
		t.Fatal("panic router was not fused")
	}
}

func TestHostHasModelRoutersReportsAvailableRouters(t *testing.T) {
	host := newRouteModelHostWithRecords(
		capabilityRecord{
			id: "router",
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					return pluginapi.ModelRouteResponse{}, nil
				}),
			}},
		},
		capabilityRecord{id: "other"},
	)

	if !host.HasModelRouters() {
		t.Fatal("HasModelRouters() = false, want true")
	}
	if host.HasModelRoutersExcept("router") {
		t.Fatal("HasModelRoutersExcept(router) = true, want false")
	}
}

func TestHostRouteModelClonesPluginMetadata(t *testing.T) {
	host := newRouteModelHostWithRecords(capabilityRecord{
		id: "router",
		meta: pluginapi.Metadata{
			Name: "Router",
			ConfigFields: []pluginapi.ConfigField{{
				Name:       "mode",
				EnumValues: []string{"safe", "fast"},
			}},
		},
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			Executor: &fakeExecutor{identifier: "fake-provider"},
			ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
				req.Plugin.ConfigFields[0].Name = "mutated"
				req.Plugin.ConfigFields[0].EnumValues[0] = "mutated"
				return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf}, nil
			}),
		}},
	})

	resp, ok := host.RouteModel(context.Background(), pluginapi.ModelRouteRequest{RequestedModel: "original"})
	if !ok || resp.Target != "router" {
		t.Fatalf("RouteModel() = %#v, %v; want router executor handled", resp, ok)
	}
	meta := host.Snapshot().records[0].meta
	if meta.ConfigFields[0].Name != "mode" || meta.ConfigFields[0].EnumValues[0] != "safe" {
		t.Fatalf("snapshot metadata was mutated: %#v", meta.ConfigFields[0])
	}
}

func TestHostRouteModelSkipsOriginatingPlugin(t *testing.T) {
	var originCalled bool
	host := newRouteModelHostWithRecords(
		capabilityRecord{
			id:       "origin",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor: &fakeExecutor{identifier: "fake-provider"},
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					originCalled = true
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf}, nil
				}),
			}},
		},
		capabilityRecord{
			id:       "other",
			priority: 1,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor: &fakeExecutor{identifier: "fake-provider"},
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf}, nil
				}),
			}},
		},
	)

	resp, ok := host.RouteModelExcept(context.Background(), pluginapi.ModelRouteRequest{RequestedModel: "original-model"}, "origin")
	if originCalled {
		t.Fatal("origin router was called despite skip")
	}
	if !ok || resp.Target != "other" {
		t.Fatalf("RouteModelExcept() = %#v, %v; want other executor handled", resp, ok)
	}
}

// newHostWithAuthProviders builds a host whose AuthManager registers auths for the given
// provider keys, so built-in provider routing can be exercised.
func newHostWithAuthProviders(t *testing.T, providers []string, records ...capabilityRecord) *Host {
	t.Helper()
	host := newRouteModelHostWithRecords(records...)
	manager := coreauth.NewManager(nil, nil, nil)
	for i, provider := range providers {
		auth := &coreauth.Auth{ID: fmt.Sprintf("auth-%s-%d", provider, i), Provider: provider}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", provider, errRegister)
		}
	}
	host.authManager = manager
	return host
}

func TestHostRouteModelRoutesToBuiltinProvider(t *testing.T) {
	host := newHostWithAuthProviders(t, []string{"claude"}, capabilityRecord{
		id: "router",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
				return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetProvider, Target: "claude", TargetModel: "claude-sonnet-4"}, nil
			}),
		}},
	})

	resp, ok := host.RouteModel(context.Background(), pluginapi.ModelRouteRequest{RequestedModel: "original-model"})
	if !ok || !resp.Handled || resp.Target != "claude" {
		t.Fatalf("RouteModel() = %#v, %v; want claude provider handled", resp, ok)
	}
	if resp.TargetKind != pluginapi.ModelRouteTargetProvider {
		t.Fatalf("TargetKind = %q, want provider", resp.TargetKind)
	}
	if resp.TargetModel != "claude-sonnet-4" {
		t.Fatalf("TargetModel = %q, want claude-sonnet-4", resp.TargetModel)
	}
}

func TestHostRouteModelSkipsUnavailableBuiltinProvider(t *testing.T) {
	var fallbackCalled bool
	host := newHostWithAuthProviders(t, []string{"claude"},
		capabilityRecord{
			id:       "fallback",
			priority: 1,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor: &fakeExecutor{identifier: "fake-provider"},
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					fallbackCalled = true
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf}, nil
				}),
			}},
		},
		capabilityRecord{
			id:       "missing-provider",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetProvider, Target: "unknown-provider"}, nil
				}),
			}},
		},
	)

	resp, ok := host.RouteModel(context.Background(), pluginapi.ModelRouteRequest{RequestedModel: "original-model"})
	if !fallbackCalled {
		t.Fatal("fallback router was not called after unavailable provider target")
	}
	if !ok || resp.Target != "fallback" {
		t.Fatalf("RouteModel() = %#v, %v; want fallback executor handled", resp, ok)
	}
}

func TestHostRouteModelRejectsProviderAndExecutorBothSet(t *testing.T) {
	var fallbackCalled bool
	host := newHostWithAuthProviders(t, []string{"claude"},
		capabilityRecord{
			id:       "fallback",
			priority: 1,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor: &fakeExecutor{identifier: "fake-provider"},
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					fallbackCalled = true
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf}, nil
				}),
			}},
		},
		capabilityRecord{
			id:       "both",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor: &fakeExecutor{identifier: "fake-provider"},
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetKind("both"), Target: "claude"}, nil
				}),
			}},
		},
	)

	resp, ok := host.RouteModel(context.Background(), pluginapi.ModelRouteRequest{RequestedModel: "original-model"})
	if !fallbackCalled {
		t.Fatal("fallback router was not called after mutually exclusive targets")
	}
	if !ok || resp.Target != "fallback" {
		t.Fatalf("RouteModel() = %#v, %v; want fallback executor handled", resp, ok)
	}
}

func TestHostRouteModelPropagatesAvailableProviders(t *testing.T) {
	var gotProviders []string
	host := newHostWithAuthProviders(t, []string{"claude", "gemini"}, capabilityRecord{
		id: "router",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			Executor: &fakeExecutor{identifier: "fake-provider"},
			ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
				gotProviders = append([]string(nil), req.AvailableProviders...)
				return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf}, nil
			}),
		}},
	})

	if _, ok := host.RouteModel(context.Background(), pluginapi.ModelRouteRequest{RequestedModel: "original"}); !ok {
		t.Fatal("RouteModel() not handled")
	}
	want := []string{"claude", "gemini"}
	if fmt.Sprint(gotProviders) != fmt.Sprint(want) {
		t.Fatalf("AvailableProviders = %v, want %v", gotProviders, want)
	}
}

func TestHostBuiltinProviderLookup(t *testing.T) {
	host := newHostWithAuthProviders(t, []string{"Claude", "codex"})
	if !host.HasBuiltinProvider("claude") {
		t.Fatal("HasBuiltinProvider(claude) = false, want true")
	}
	if host.HasBuiltinProvider("missing") {
		t.Fatal("HasBuiltinProvider(missing) = true, want false")
	}
	providers := host.BuiltinProviders()
	if fmt.Sprint(providers) != fmt.Sprint([]string{"claude", "codex"}) {
		t.Fatalf("BuiltinProviders() = %v, want [claude codex]", providers)
	}
}

func TestHostRouteModelSkipsExecutorWithoutProviderIdentifier(t *testing.T) {
	var fallbackCalled bool
	host := newRouteModelHostWithRecords(
		capabilityRecord{
			id:       "fallback",
			priority: 1,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor: &fakeExecutor{identifier: "fallback-provider"},
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					fallbackCalled = true
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf}, nil
				}),
			}},
		},
		capabilityRecord{
			id:       "no-provider",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				// Executor is declared but resolves no provider identifier, so execution
				// would fail. Routing must skip it and fall through to the lower-priority router.
				Executor: &fakeExecutor{identifierFunc: func() string { return "" }},
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf}, nil
				}),
			}},
		},
	)

	resp, ok := host.RouteModel(context.Background(), pluginapi.ModelRouteRequest{RequestedModel: "original-model"})
	if !fallbackCalled {
		t.Fatal("fallback router was not called after executor without provider identifier was skipped")
	}
	if !ok || resp.Target != "fallback" {
		t.Fatalf("RouteModel() = %#v, %v; want fallback executor handled", resp, ok)
	}
}

func TestHostRouteModelSkipsExecutorWithUnsupportedFormats(t *testing.T) {
	var fallbackCalled bool
	host := newHostWithRecords(
		capabilityRecord{
			id:       "fallback",
			priority: 1,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor:              &fakeExecutor{identifier: "fallback-provider"},
				ExecutorInputFormats:  []string{"openai"},
				ExecutorOutputFormats: []string{"openai"},
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					fallbackCalled = true
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf}, nil
				}),
			}},
		},
		capabilityRecord{
			id:       "unsupported-formats",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor: &fakeExecutor{identifier: "unsupported-provider"},
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf}, nil
				}),
			}},
		},
	)

	resp, ok := host.RouteModel(context.Background(), pluginapi.ModelRouteRequest{RequestedModel: "original-model", SourceFormat: "openai"})
	if !fallbackCalled {
		t.Fatal("fallback router was not called after executor with unsupported formats was skipped")
	}
	if !ok || resp.Target != "fallback" {
		t.Fatalf("RouteModel() = %#v, %v; want fallback executor handled", resp, ok)
	}
}

func TestHostRouteModelSkipsOAuthOnlyExecutorTargets(t *testing.T) {
	var fallbackCalled bool
	host := newHostWithRecords(
		capabilityRecord{
			id:       "fallback",
			priority: 1,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor:              &fakeExecutor{identifier: "fallback-provider"},
				ExecutorModelScope:    pluginapi.ExecutorModelScopeStatic,
				ExecutorInputFormats:  []string{"openai"},
				ExecutorOutputFormats: []string{"openai"},
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					fallbackCalled = true
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf}, nil
				}),
			}},
		},
		capabilityRecord{
			id:       "oauth-only",
			priority: 10,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				Executor:              &fakeExecutor{identifier: "oauth-provider"},
				ExecutorModelScope:    pluginapi.ExecutorModelScopeOAuth,
				ExecutorInputFormats:  []string{"openai"},
				ExecutorOutputFormats: []string{"openai"},
				ModelRouter: modelRouterFunc(func(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
					return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf}, nil
				}),
			}},
		},
	)

	resp, ok := host.RouteModel(context.Background(), pluginapi.ModelRouteRequest{RequestedModel: "original-model", SourceFormat: "openai"})
	if !fallbackCalled {
		t.Fatal("fallback router was not called after OAuth-only executor target was skipped")
	}
	if !ok || resp.Target != "fallback" {
		t.Fatalf("RouteModel() = %#v, %v; want fallback executor handled", resp, ok)
	}
}
