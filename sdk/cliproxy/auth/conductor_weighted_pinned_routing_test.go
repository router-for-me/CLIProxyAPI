package auth

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestManagerWeightedPinsShareCreditsWithoutAffinity(t *testing.T) {
	for _, scenario := range []string{"single", "mixed-single", "mixed-providers", "lower-priority", "websocket", "http-pin", "mixed-single-websocket", "xai-websocket", "mixed-single-xai"} {
		t.Run(scenario, func(t *testing.T) {
			weighted := &WeightedRoundRobinSelector{}
			manager := NewManager(nil, weighted, nil)
			const model = "weighted-pin-routing"
			providers := []string{"codex"}
			if scenario == "xai-websocket" || scenario == "mixed-single-xai" {
				providers[0] = "xai"
			}
			mixed := scenario == "mixed-single" || scenario == "mixed-providers" || scenario == "mixed-single-websocket" || scenario == "mixed-single-xai"
			if scenario == "mixed-providers" {
				providers = append(providers, "openai")
			}
			for _, provider := range providers {
				manager.RegisterExecutor(schedulerTestExecutor{provider: provider})
			}
			a := &Auth{ID: t.Name() + "-a", Provider: providers[0], Attributes: map[string]string{AttributeWeight: "3"}}
			b := &Auth{ID: t.Name() + "-b", Provider: providers[len(providers)-1], Attributes: map[string]string{AttributeWeight: "1"}}
			auths := []*Auth{a, b}
			// A zero-weight higher tier must not hide eligible positive-weight peers.
			auths = append(auths, &Auth{ID: t.Name() + "-zero", Provider: providers[0], Attributes: map[string]string{AttributeWeight: "0", "priority": "100"}})
			if scenario == "lower-priority" {
				auths = append(auths, &Auth{ID: t.Name() + "-higher", Provider: providers[0], Attributes: map[string]string{"priority": "1"}})
			}
			ctx := t.Context()
			if scenario == "websocket" || scenario == "http-pin" || scenario == "mixed-single-websocket" || scenario == "xai-websocket" || scenario == "mixed-single-xai" {
				ctx = cliproxyexecutor.WithDownstreamWebsocket(ctx)
				a.Attributes["websockets"] = "true"
				b.Attributes["websockets"] = "true"
				otherWebsocket := "false"
				if scenario == "http-pin" {
					a.Attributes["websockets"] = "false"
					b.Attributes["websockets"] = "false"
					otherWebsocket = "true"
				}
				auths = append(auths, &Auth{ID: t.Name() + "-other-transport", Provider: providers[0], Attributes: map[string]string{"websockets": otherWebsocket}})
			}
			for _, auth := range auths {
				auth.Status = StatusActive
				registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
				if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
					t.Fatal(errRegister)
				}
			}
			// Normalize single/mixed entrypoints while retaining their real routing decisions.
			pick := func(ctx context.Context, opts cliproxyexecutor.Options) *Auth {
				t.Helper()
				var auth *Auth
				var errPick error
				if mixed {
					auth, _, _, errPick = manager.pickNextMixed(ctx, append([]string{"missing-executor"}, providers...), model, opts, nil)
				} else {
					auth, _, errPick = manager.pickNext(ctx, providers[0], model, opts, nil)
				}
				if errPick != nil || auth == nil {
					t.Fatalf("selection failed: auth=%v err=%v", auth, errPick)
				}
				return auth
			}
			for range 4 {
				if auth := pick(ctx, cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: b.ID}}); auth.ID != b.ID {
					t.Fatalf("pin escaped: got %q, want %q", auth.ID, b.ID)
				}
			}
			namespace := providers[0]
			if mixed {
				namespace = "mixed"
			}
			state := weightedStateForTest(t, weighted, namespace+":"+model, a.ID, b.ID)
			if !maps.Equal(state.currentForTest(), map[string]int64{a.ID: 12, b.ID: -12}) || state.inFlight != 0 {
				t.Fatalf("wrong pin accounting: current=%v inFlight=%d", state.currentForTest(), state.inFlight)
			}
			if scenario == "lower-priority" || scenario == "http-pin" {
				// Traffic in a different priority/transport pool must not repay this pool.
				for range 4 {
					pick(ctx, cliproxyexecutor.Options{})
				}
				if !maps.Equal(state.currentForTest(), map[string]int64{a.ID: 12, b.ID: -12}) {
					t.Fatalf("unrelated pool consumed pin debt: %v", state.currentForTest())
				}
				if scenario == "lower-priority" {
					manager.Remove(WithSkipPersist(ctx), t.Name()+"-higher")
				} else {
					manager.Remove(WithSkipPersist(ctx), t.Name()+"-other-transport")
				}
			}
			for i := range 12 {
				// Headers must not enable sticky bindings when affinity is disabled.
				opts := cliproxyexecutor.Options{Headers: http.Header{"Session_id": {fmt.Sprintf("fresh-%d", i)}}}
				if auth := pick(ctx, opts); auth.ID != a.ID {
					t.Fatalf("request %d failed to repay pinned debt: got %q, want %q", i, auth.ID, a.ID)
				}
			}
			if !maps.Equal(state.currentForTest(), map[string]int64{a.ID: 0, b.ID: 0}) || len(state.pendingForTest()) != 0 || state.inFlight != 0 {
				t.Fatalf("unsettled compensation: current=%v pending=%v inFlight=%d", state.currentForTest(), state.pendingForTest(), state.inFlight)
			}
			counts := map[string]int{}
			for range 40 {
				counts[pick(ctx, cliproxyexecutor.Options{}).ID]++
			}
			if counts[a.ID] != 30 || counts[b.ID] != 10 {
				t.Fatalf("ordinary routing lost 3:1 ratio after repayment: %v", counts)
			}
		})
	}
}
