package cliproxy

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type ownedRegistrationCounter struct {
	*ownedTestExecutor
	identifiers atomic.Int32
}

func (e *ownedRegistrationCounter) Identifier() string {
	e.identifiers.Add(1)
	return e.provider
}

func TestOwnedProviderExecutorSkipsUnchangedManagerRegistration(t *testing.T) {
	for _, path := range []string{"normal", "forced", "plugin_sync", "missing_binding", "different_binding"} {
		t.Run(path, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			owner := &ownedRegistrationCounter{ownedTestExecutor: &ownedTestExecutor{provider: ownedTestProvider}}
			service, errBuild := ownedTestBuilder(t, manager).WithOwnedProviderExecutor(ownedTestProvider, owner).Build()
			if service != nil {
				t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
			}
			if errBuild != nil {
				t.Fatal("owned registration fixture build failed")
			}
			const credentials = 32
			auths := make([]*coreauth.Auth, 0, credentials)
			for i := range credentials {
				auth := &coreauth.Auth{ID: fmt.Sprintf("owned-registration-%s-%d", path, i), Provider: ownedTestProvider, Status: coreauth.StatusActive}
				if _, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), auth); errRegister != nil {
					t.Fatal("owned registration fixture auth failed")
				}
				auths = append(auths, auth)
				t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
			}
			var replaced *ownedTestExecutor
			var wantRegistrations int32
			switch path {
			case "missing_binding":
				manager.UnregisterExecutor(ownedTestProvider)
				wantRegistrations = 1
			case "different_binding":
				replaced = &ownedTestExecutor{provider: ownedTestProvider}
				manager.RegisterExecutor(replaced)
				wantRegistrations = 1
			}
			_, _, priorCloses, _ := owner.counts()
			owner.identifiers.Store(0)
			if path == "plugin_sync" {
				service.syncPluginModelRuntime(context.Background())
			} else {
				service.registerExecutorsForAuths(auths, path == "forced")
			}
			// RegisterExecutor calls Identifier before scanning all auths and
			// rescheduling their refreshes. No-op ownership checks must bypass
			// that method entirely; elapsed-time thresholds cannot prove this.
			if owner.identifiers.Load() != wantRegistrations {
				t.Fatal("owned provider repeated unchanged manager registration")
			}
			if got, ok := manager.Executor(ownedTestProvider); !ok || got != owner {
				t.Fatal("owned registration did not restore the original executor")
			}
			_, _, closes, _ := owner.counts()
			if closes != priorCloses {
				t.Fatal("unchanged owner lost execution sessions")
			}
			if replaced != nil {
				_, _, closes, _ := replaced.counts()
				if closes != 1 {
					t.Fatal("restored ownership did not retire the replaced executor once")
				}
			}
			owner.identifiers.Store(0)
			service.registerExecutorsForAuths(auths, true)
			if owner.identifiers.Load() != 0 {
				t.Fatal("restored owner was registered again on the next pass")
			}
		})
	}
}

func TestOwnedProviderExecutorRestoresNonComparableExistingValue(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
	}{
		{"nested_slice", []int{1}},
		{"nested_map", map[string]int{"fixture": 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			owner := ownedNestedInterface{value: "fixture"}
			service, errBuild := ownedTestBuilder(t, manager).WithOwnedProviderExecutor(ownedTestProvider, owner).Build()
			if service != nil {
				t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
			}
			if errBuild != nil {
				t.Fatal("comparable owned registration rejected")
			}
			// The existing value has the same dynamic struct type as the owner,
			// but a noncomparable nested interface value. Comparison remains safe
			// because Build validated the owned value itself, not only its type.
			manager.RegisterExecutor(ownedNestedInterface{value: test.value})
			service.registerExecutorForAuth(&coreauth.Auth{ID: "restored-value", Provider: ownedTestProvider}, true)
			if got, ok := manager.Executor(ownedTestProvider); !ok || got != owner {
				t.Fatal("noncomparable existing value prevented owner restoration")
			}
		})
	}
}
