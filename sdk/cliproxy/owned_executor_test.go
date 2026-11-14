package cliproxy

import (
	"context"
	"math"
	"sync/atomic"
	"testing"
	"time"

	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestOwnedProviderExecutorValidatesBeforeManagerMutation(t *testing.T) {
	var typedNil *ownedTestExecutor
	cases := []struct {
		name, provider string
		executor       coreauth.ProviderExecutor
	}{
		{"empty", "", &ownedTestExecutor{provider: ownedTestProvider}},
		{"nil", ownedTestProvider, nil},
		{"typed_nil", ownedTestProvider, typedNil},
		{"non_comparable", ownedTestProvider, ownedNonComparable{}},
		{"nested_slice", ownedTestProvider, ownedNestedInterface{value: []int{1}}},
		{"nested_map", ownedTestProvider, ownedNestedInterface{value: map[string]int{"fixture": 1}}},
		{"nil_channel", ownedTestProvider, ownedChannel(nil)},
		{"non_reflexive", ownedTestProvider, ownedNonReflexive{value: math.NaN(), closes: new(atomic.Int32)}},
		{"mismatch", ownedTestProvider, &ownedTestExecutor{provider: "another-provider"}},
		{"noncanonical_identifier", ownedTestProvider, &ownedTestExecutor{provider: "EXTERNAL-PROVIDER"}},
		{"compat_namespace", "openai-compatible-example", &ownedTestExecutor{provider: "openai-compatible-example"}},
		{"kimi_alias", "kimi.com", &ownedTestExecutor{provider: "kimi.com"}},
	}
	for _, auth := range baselineExecutorAuths() {
		cases = append(cases, struct {
			name, provider string
			executor       coreauth.ProviderExecutor
		}{"native_" + auth.Provider, auth.Provider, &ownedTestExecutor{provider: auth.Provider}})
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			previous := &ownedTestExecutor{provider: "previous-provider"}
			manager.RegisterExecutor(previous)
			valid := &ownedTestExecutor{provider: "valid-provider"}
			service, errBuild := ownedTestBuilder(t, manager).WithOwnedProviderExecutor(valid.provider, valid).WithOwnedProviderExecutor(test.provider, test.executor).Build()
			if service != nil {
				t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
			}
			if errBuild == nil {
				t.Fatal("invalid owned registration accepted")
			}
			if got, ok := manager.Executor(previous.provider); !ok || got != previous {
				t.Fatal("invalid registration changed existing executor")
			}
			if _, ok := manager.Executor(valid.provider); ok {
				t.Fatal("invalid registration partially installed owners")
			}
		})
	}
	t.Run("duplicate_normalized_key", func(t *testing.T) {
		manager := coreauth.NewManager(nil, nil, nil)
		owner := &ownedTestExecutor{provider: ownedTestProvider}
		service, errBuild := ownedTestBuilder(t, manager).WithOwnedProviderExecutor(owner.provider, owner).WithOwnedProviderExecutor(" EXTERNAL-PROVIDER ", owner).Build()
		if service != nil {
			t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
		}
		if errBuild == nil {
			t.Fatal("duplicate owned registration accepted")
		}
		if _, ok := manager.Executor(owner.provider); ok {
			t.Fatal("duplicate registration mutated manager")
		}
	})
}

func TestOwnedProviderExecutorAcceptsStableValueIdentity(t *testing.T) {
	for _, test := range []struct {
		name     string
		executor coreauth.ProviderExecutor
	}{
		{"value", ownedNestedInterface{value: "fixture"}},
		{"channel", make(ownedChannel)},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			service, errBuild := ownedTestBuilder(t, manager).WithOwnedProviderExecutor(ownedTestProvider, test.executor).Build()
			if service != nil {
				t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
			}
			if errBuild != nil {
				t.Fatal("stable value identity rejected")
			}
			service.registerExecutorForAuth(&coreauth.Auth{ID: "stable", Provider: ownedTestProvider}, true)
			if got, _ := manager.Executor(ownedTestProvider); got != test.executor {
				t.Fatal("stable value owner replaced")
			}
		})
	}
}

func TestOwnedProviderExecutorSurvivesRegistrationPaths(t *testing.T) {
	for _, path := range []string{"forced", "runtime_reload", "plugin_sync", "missing_binding"} {
		t.Run(path, func(t *testing.T) {
			owner := &ownedTestExecutor{provider: ownedTestProvider}
			service, manager, auth := ownedTestService(t, owner)
			switch path {
			case "runtime_reload":
				if !service.applyConfigUpdateWithAuthSynthesis(context.Background(), &config.Config{}, false) {
					t.Fatal("config runtime reload failed")
				}
			case "plugin_sync":
				service.syncPluginModelRuntime(context.Background())
			case "missing_binding":
				manager.UnregisterExecutor(owner.provider)
				service.ensureExecutorsForAuth(auth)
			default:
				service.registerExecutorForAuth(auth, true)
			}
			if got, ok := manager.Executor(owner.provider); !ok || got != owner {
				t.Fatal("service replaced owned executor")
			}
			_, _, closes, _ := owner.counts()
			if closes != 0 {
				t.Fatal("service retired owned execution sessions")
			}
		})
	}
}

func TestOwnedProviderExecutorKeepsCompatibilityNamespaceSeparate(t *testing.T) {
	for _, provider := range []string{"openai-compatibility", ownedTestProvider} {
		t.Run(provider, func(t *testing.T) {
			owner := &ownedTestExecutor{provider: ownedTestProvider}
			service, manager, auth := ownedTestService(t, owner)
			compat := &coreauth.Auth{ID: "compat-fixture-auth", Provider: provider, Label: owner.provider, Attributes: map[string]string{"compat_name": owner.provider, "provider_key": owner.provider}}
			service.registerExecutorsForAuths([]*coreauth.Auth{auth, compat}, true)
			if got, _ := manager.Executor(owner.provider); got != owner {
				t.Fatal("compat registration replaced custom owner")
			}
			compatExecutor, ok := manager.Executor("openai-compatible-" + owner.provider)
			if !ok {
				t.Fatal("explicit compat executor missing")
			}
			if _, ok = compatExecutor.(*runtimeexecutor.OpenAICompatExecutor); !ok {
				t.Fatal("explicit compat executor not stock")
			}
		})
	}
}

func TestOwnedProviderExecutorBaseURLRemainsOwned(t *testing.T) {
	owner := &ownedTestExecutor{provider: ownedTestProvider}
	service, manager, auth := ownedTestService(t, owner)
	auth.Attributes = map[string]string{"base_url": "https://example.invalid"}
	service.registerExecutorForAuth(auth, true)
	if got, _ := manager.Executor(owner.provider); got != owner {
		t.Fatal("base URL metadata replaced custom owner")
	}
}

func TestOwnedProviderExecutorBuilderSnapshot(t *testing.T) {
	owner := &ownedTestExecutor{provider: ownedTestProvider}
	manager := coreauth.NewManager(nil, nil, nil)
	builder := ownedTestBuilder(t, manager).WithOwnedProviderExecutor(" EXTERNAL-PROVIDER ", owner)
	service, errBuild := builder.Build()
	if errBuild != nil {
		t.Fatal("canonical owned registration rejected")
	}
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	later := &ownedTestExecutor{provider: "later-provider"}
	builder.WithOwnedProviderExecutor(later.provider, later)
	service.registerExecutorForAuth(&coreauth.Auth{ID: "later", Provider: later.provider}, true)
	if got, _ := manager.Executor(later.provider); got == later {
		t.Fatal("builder mutation changed built ownership")
	}
	if got, _ := manager.Executor(owner.provider); got != owner {
		t.Fatal("builder lost original executor")
	}
}

func TestOwnedProviderExecutorManagerDispatchAfterForcedRebind(t *testing.T) {
	for _, mode := range []string{"unary", "count", "stream"} {
		for _, refresh := range []bool{false, true} {
			name := mode
			if refresh {
				name += "_refresh"
			}
			t.Run(name, func(t *testing.T) {
				owner := &ownedTestExecutor{provider: ownedTestProvider, unauthorized: refresh}
				service, manager, auth := ownedTestService(t, owner)
				service.registerExecutorForAuth(auth, true)
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if errCall := ownedTestCall(ctx, manager, mode); errCall != nil {
					t.Fatal("owned manager execution failed")
				}
				calls, refreshes, closes, seen := owner.counts()
				wantCalls, wantRefresh := 1, 0
				if refresh {
					wantCalls, wantRefresh = 2, 1
				}
				if calls != wantCalls || refreshes != wantRefresh || closes != 0 || !seen {
					t.Fatal("owned execution or refresh accounting changed")
				}
			})
		}
	}
}

func TestOwnedProviderExecutorAdmittedCallSurvivesForcedRebind(t *testing.T) {
	owner := &ownedTestExecutor{provider: ownedTestProvider, entered: make(chan struct{}), release: make(chan struct{})}
	service, manager, auth := ownedTestService(t, owner)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ownedTestCall(ctx, manager, "unary") }()
	select {
	case <-owner.entered:
	case <-ctx.Done():
		close(owner.release)
		<-done
		t.Fatal("owned execution did not enter")
	}
	service.registerExecutorForAuth(auth, true)
	_, _, closes, _ := owner.counts()
	close(owner.release)
	if errCall := <-done; errCall != nil {
		t.Fatal("admitted owned execution failed")
	}
	if closes != 0 {
		t.Fatal("forced rebind retired admitted owner")
	}
	if got, _ := manager.Executor(owner.provider); got != owner {
		t.Fatal("next dispatch lost admitted owner")
	}
}
