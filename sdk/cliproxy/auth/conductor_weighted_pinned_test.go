package auth

import (
	"fmt"
	"maps"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestManagerWeightedPinnedSessionCompensation(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		for _, mode := range []string{"sticky", "cold", "no-session", "lcp", "no-affinity", "no-affinity-legacy"} {
			t.Run(fmt.Sprintf("mixed=%t/%s", mixed, mode), func(t *testing.T) {
				weighted := &WeightedRoundRobinSelector{}
				selector := NewSessionAffinitySelector(weighted)
				t.Cleanup(selector.Stop)
				manager := NewManager(nil, selector, nil)
				if mode == "no-affinity" || mode == "no-affinity-legacy" {
					manager.SetSelector(weighted)
				}
				const provider, model = "weighted-pinned", "weighted-pinned-model"
				manager.RegisterExecutor(schedulerTestExecutor{provider: provider})
				ctx := withRequiredAuthKind(t.Context(), AuthKindAPIKey)
				aID, bID := t.Name()+"-a", t.Name()+"-b"
				triedID := t.Name() + "-tried"
				excluded := []*Auth{
					{ID: t.Name() + "-disabled", Disabled: true},
					{ID: t.Name() + "-zero", Attributes: map[string]string{AttributeWeight: "0"}},
					{ID: t.Name() + "-cooldown", ModelStates: map[string]*ModelState{
						model: {Unavailable: true, NextRetryAfter: time.Now().Add(time.Hour)},
					}},
					{ID: t.Name() + "-wrong-model"},
					{ID: t.Name() + "-wrong-kind", Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth}},
					{ID: t.Name() + "-wrong-provider", Provider: "other-provider"},
					{ID: t.Name() + "-no-executor", Provider: "no-executor"},
					{ID: t.Name() + "-lower-priority", Attributes: map[string]string{"priority": "0"}},
					{ID: triedID},
				}
				auths := append([]*Auth{
					{ID: aID, Attributes: map[string]string{AttributeWeight: "3", "priority": "1"}},
					{ID: bID, Attributes: map[string]string{AttributeWeight: "1", "priority": "1"}},
				}, excluded...)
				for _, auth := range auths {
					if auth.Provider == "" {
						auth.Provider = provider
					}
					auth.Status = StatusActive
					if auth.Attributes == nil {
						auth.Attributes = make(map[string]string)
					}
					if _, ok := auth.Attributes[AttributeWeight]; !ok {
						auth.Attributes[AttributeWeight] = "100"
					}
					if _, ok := auth.Attributes["priority"]; !ok {
						auth.Attributes["priority"] = "2"
					}
					if _, ok := auth.Attributes[AttributeAuthKind]; !ok {
						auth.Attributes[AttributeAuthKind] = AuthKindAPIKey
					}
					registeredModel := model
					if auth.ID == t.Name()+"-wrong-model" {
						registeredModel = "other-model"
					}
					registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: registeredModel}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
					if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
						t.Fatal(errRegister)
					}
				}
				namespace := provider
				if mixed {
					namespace = "mixed"
				}
				stickyHeaders := http.Header{"Session_id": {"pinned-session"}}
				id := CanonicalSessionID(stickyHeaders, nil, nil)
				bindingKey := namespace + "::" + id + "::" + model
				if mode == "sticky" {
					selector.cache.Set(bindingKey, bID)
				}
				tried := map[string]struct{}{triedID: {}}
				// Exercise the production entrypoints as well as direct legacy selection.
				pick := func(opts cliproxyexecutor.Options) (*Auth, error) {
					if mode == "no-affinity" {
						if mixed {
							auth, _, _, errPick := manager.pickNextMixed(ctx, []string{provider, "no-executor"}, model, opts, tried)
							return auth, errPick
						}
						auth, _, errPick := manager.pickNext(ctx, provider, model, opts, tried)
						return auth, errPick
					}
					if mixed {
						auth, _, _, errPick := manager.pickNextMixedLegacy(ctx, []string{provider, "no-executor"}, model, opts, tried)
						return auth, errPick
					}
					auth, _, errPick := manager.pickNextLegacy(ctx, provider, model, opts, tried)
					return auth, errPick
				}
				for i := range 4 {
					opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: bID}}
					switch mode {
					case "sticky", "cold":
						opts.Headers = stickyHeaders
					case "lcp":
						opts.SourceFormat = sdktranslator.FormatOpenAI
						opts.OriginalRequest = []byte(`{"messages":[{"role":"user","content":"pinned conversation"}]}`)
						opts.Metadata[cliproxyexecutor.CallerScopeMetadataKey] = "pinned-caller"
					}
					if auth, errPick := pick(opts); errPick != nil || auth == nil || auth.ID != bID {
						t.Fatalf("pinned request %d: auth=%v err=%v, want %q", i, auth, errPick, bID)
					}
				}
				state := weightedStateForTest(t, weighted, namespace+":"+model, aID, bID)
				if state == nil || !maps.Equal(state.currentForTest(), map[string]int64{aID: 12, bID: -12}) || len(state.pendingForTest()) != 0 {
					t.Fatalf("pinned traffic did not consume the eligible pool's credit: %#v", state)
				}
				counts := map[string]int{}
				for i := range 12 {
					auth, errPick := pick(cliproxyexecutor.Options{Headers: http.Header{"Session_id": {fmt.Sprintf("fresh-%d", i)}}})
					if errPick != nil || auth == nil {
						t.Fatalf("fresh request %d: auth=%v err=%v", i, auth, errPick)
					}
					counts[auth.ID]++
				}
				if counts[aID] != 12 || counts[bID] != 0 {
					t.Fatalf("fresh distribution = %v, want a=12 b=0", counts)
				}
				if !maps.Equal(state.currentForTest(), map[string]int64{aID: 0, bID: 0}) || len(state.pendingForTest()) != 0 {
					t.Fatalf("compensation did not balance: current=%v pending=%v", state.currentForTest(), state.pendingForTest())
				}
				if mode == "sticky" || mode == "cold" {
					if binding, ok := selector.cache.Get(bindingKey); !ok || binding != bID {
						t.Fatalf("compensation changed pinned binding: %q, found=%t", binding, ok)
					}
				}
				// A broader accounting pool must never make an invalid pin selectable.
				for _, pinnedID := range []string{excluded[0].ID, excluded[1].ID, excluded[2].ID, excluded[3].ID, excluded[4].ID, excluded[5].ID, excluded[6].ID, triedID, "missing"} {
					opts := cliproxyexecutor.Options{
						Headers:  stickyHeaders,
						Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: pinnedID},
					}
					if auth, errPick := pick(opts); errPick == nil || auth != nil {
						t.Fatalf("invalid pin %q escaped pinning checks: auth=%v err=%v", pinnedID, auth, errPick)
					}
					if !maps.Equal(state.currentForTest(), map[string]int64{aID: 0, bID: 0}) || len(state.pendingForTest()) != 0 {
						t.Fatalf("invalid pin %q changed accounting: current=%v pending=%v", pinnedID, state.currentForTest(), state.pendingForTest())
					}
				}
				// A valid lower-priority pin still wins without charging another tier.
				lowerID := t.Name() + "-lower-priority"
				lowerOpts := cliproxyexecutor.Options{
					Headers:  http.Header{"Session_id": {"lower-pinned-session"}},
					Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: lowerID},
				}
				if auth, errPick := pick(lowerOpts); errPick != nil || auth == nil || auth.ID != lowerID {
					t.Fatalf("lower-priority pin was not enforced: auth=%v err=%v", auth, errPick)
				}
				if !maps.Equal(state.currentForTest(), map[string]int64{aID: 0, bID: 0}) || len(state.pendingForTest()) != 0 {
					t.Fatalf("lower-priority pin changed higher-tier credits: current=%v pending=%v", state.currentForTest(), state.pendingForTest())
				}
				lowerState := weightedStateForTest(t, weighted, namespace+":"+model, lowerID)
				if !maps.Equal(lowerState.currentForTest(), map[string]int64{lowerID: 0}) || len(lowerState.pendingForTest()) != 0 {
					t.Fatalf("lower-priority pin accounted the wrong pool: %#v", lowerState)
				}
			})
		}
	}
}
