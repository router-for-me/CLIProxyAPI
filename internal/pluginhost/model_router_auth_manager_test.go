package pluginhost

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestHostBuiltinProviderAuthManagerControls(t *testing.T) {
	for _, name := range []string{"nil_receiver", "nil_manager", "cleared_manager"} {
		t.Run(name, func(t *testing.T) {
			var host *Host
			if name != "nil_receiver" {
				host = &Host{}
			}
			if name == "cleared_manager" {
				host.SetAuthManager(builtinLookupManager(t, "claude"))
			}
			host.SetAuthManager(nil)
			if host.BuiltinProviders() != nil || host.HasBuiltinProvider("claude") {
				t.Fatal("absent auth manager exposed a provider")
			}
		})
	}
	t.Run("stable_manager", func(t *testing.T) {
		host := &Host{}
		host.SetAuthManager(builtinLookupManager(t, "Claude", "codex"))
		providers := host.BuiltinProviders()
		if len(providers) != 2 || providers[0] != "claude" || providers[1] != "codex" || !host.HasBuiltinProvider("claude") || !host.HasBuiltinProvider("codex") || host.HasBuiltinProvider("missing") {
			t.Fatal("stable auth manager provider lookup changed")
		}
		providers[0] = "changed"
		if host.BuiltinProviders()[0] != "claude" {
			t.Fatal("provider result mutation changed manager state")
		}
	})
}

func TestHostBuiltinProviderAuthManagerConcurrentReplacement(t *testing.T) {
	for _, name := range []string{"list", "membership"} {
		t.Run(name, func(t *testing.T) {
			first := builtinLookupManager(t, "claude")
			second := builtinLookupManager(t, "claude", "codex")
			host := &Host{}
			host.SetAuthManager(first)
			start := make(chan struct{})
			var workers sync.WaitGroup
			var valid atomic.Bool
			valid.Store(true)
			workers.Add(2)
			go func() {
				defer workers.Done()
				<-start
				for i := 0; i < 4096; i++ {
					manager := first
					if i%2 != 0 {
						manager = second
					}
					host.SetAuthManager(manager)
					runtime.Gosched()
				}
			}()
			go func() {
				defer workers.Done()
				<-start
				for i := 0; i < 4096; i++ {
					if name == "list" {
						providers := host.BuiltinProviders()
						if len(providers) < 1 || len(providers) > 2 || providers[0] != "claude" || (len(providers) == 2 && providers[1] != "codex") {
							valid.Store(false)
						}
					} else if !host.HasBuiltinProvider("claude") || host.HasBuiltinProvider("missing") {
						valid.Store(false)
					}
					runtime.Gosched()
				}
			}()
			close(start)
			workers.Wait()
			if !valid.Load() || !host.HasBuiltinProvider("codex") {
				t.Fatal("concurrent auth manager lookup changed provider semantics")
			}
			providers := host.BuiltinProviders()
			if len(providers) != 2 || providers[0] != "claude" || providers[1] != "codex" {
				t.Fatal("settled provider list did not use the latest manager")
			}
		})
	}
}

func builtinLookupManager(t *testing.T, providers ...string) *coreauth.Manager {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	for _, provider := range providers {
		if _, err := manager.Register(coreauth.WithSkipPersist(context.Background()), &coreauth.Auth{ID: provider, Provider: provider, Status: coreauth.StatusActive}); err != nil {
			t.Fatal("builtin provider fixture registration failed")
		}
	}
	return manager
}
