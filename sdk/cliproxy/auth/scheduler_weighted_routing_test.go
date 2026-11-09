package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestWeightedWebsocketPreferenceAcrossPriorities(t *testing.T) {
	for _, provider := range []string{"codex", "xai"} {
		for _, mixed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mixed=%t", provider, mixed), func(t *testing.T) {
				manager := NewManager(nil, &WeightedRoundRobinSelector{}, nil)
				manager.RegisterExecutor(schedulerTestExecutor{provider: provider})
				ctx := cliproxyexecutor.WithDownstreamWebsocket(t.Context())
				const model = "weighted-ws-priority"
				for _, auth := range []*Auth{
					{ID: t.Name() + "-http", Provider: provider, Attributes: map[string]string{"priority": "10"}},
					{ID: t.Name() + "-ws", Provider: provider, Attributes: map[string]string{"priority": "0", "websockets": "true"}},
				} {
					registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
					if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
						t.Fatal(errRegister)
					}
				}
				for _, routeModel := range []string{"", model} {
					var got *Auth
					var errPick error
					if mixed {
						got, _, _, errPick = manager.pickNextMixed(ctx, []string{provider}, routeModel, cliproxyexecutor.Options{}, nil)
					} else {
						got, _, errPick = manager.pickNext(ctx, provider, routeModel, cliproxyexecutor.Options{}, nil)
					}
					if errPick != nil || got == nil {
						t.Fatalf("model=%q: selection failed: auth=%v err=%v", routeModel, got, errPick)
					}
					if got.ID != t.Name()+"-ws" {
						t.Fatalf("model=%q: got %q, want lower-priority WebSocket auth", routeModel, got.ID)
					}
				}
			})
		}
	}
}

func TestWeightedSchedulerAndLegacyShareCredits(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		for _, pinViaLegacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("mixed=%t/pinViaLegacy=%t", mixed, pinViaLegacy), func(t *testing.T) {
				weighted := &WeightedRoundRobinSelector{}
				manager := NewManager(nil, weighted, nil)
				const provider, model = "weighted-shared", "weighted-shared-model"
				manager.RegisterExecutor(schedulerTestExecutor{provider: provider})
				for _, auth := range []*Auth{
					{ID: t.Name() + "-a", Provider: provider, Attributes: map[string]string{AttributeWeight: "3"}},
					{ID: t.Name() + "-b", Provider: provider, Attributes: map[string]string{AttributeWeight: "1"}},
				} {
					registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
					if _, errRegister := manager.Register(WithSkipPersist(t.Context()), auth); errRegister != nil {
						t.Fatal(errRegister)
					}
				}
				if !manager.useSchedulerFastPath() {
					t.Fatal("bare weighted routing must retain the scheduler fast path")
				}
				// Normalize the four entrypoints to verify accounting across boundaries.
				pick := func(ctx context.Context, legacy bool, opts cliproxyexecutor.Options) (*Auth, error) {
					if mixed {
						if legacy {
							auth, _, _, errPick := manager.pickNextMixedLegacy(ctx, []string{provider}, model, opts, nil)
							return auth, errPick
						}
						auth, _, _, errPick := manager.pickNextMixed(ctx, []string{provider}, model, opts, nil)
						return auth, errPick
					}
					if legacy {
						auth, _, errPick := manager.pickNextLegacy(ctx, provider, model, opts, nil)
						return auth, errPick
					}
					auth, _, errPick := manager.pickNext(ctx, provider, model, opts, nil)
					return auth, errPick
				}
				for range 4 {
					opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: t.Name() + "-b"}}
					auth, errPick := pick(t.Context(), pinViaLegacy, opts)
					if errPick != nil || auth == nil || auth.ID != t.Name()+"-b" {
						t.Fatalf("pin failed: auth=%v err=%v", auth, errPick)
					}
				}
				canceled, cancel := context.WithCancel(t.Context())
				cancel()
				if auth, errPick := pick(canceled, false, cliproxyexecutor.Options{}); auth != nil || !errors.Is(errPick, context.Canceled) {
					t.Fatalf("canceled selection: auth=%v err=%v", auth, errPick)
				}
				for i := range 12 {
					auth, errPick := pick(t.Context(), !pinViaLegacy, cliproxyexecutor.Options{})
					if errPick != nil || auth == nil || auth.ID != t.Name()+"-a" {
						t.Fatalf("repayment %d used a different ledger: auth=%v err=%v", i, auth, errPick)
					}
				}
			})
		}
	}
}

func TestWeightedSchedulerSeesPendingReservations(t *testing.T) {
	weighted := &WeightedRoundRobinSelector{}
	auths := []*Auth{{ID: "pending-a", Provider: "weighted-pending"}, {ID: "pending-b", Provider: "weighted-pending"}}
	scheduler := newSchedulerForTest(weighted, auths...)
	_, reservation := weighted.reserve("weighted-pending:", auths, auths[0].ID)
	t.Cleanup(func() { reservation.finish(false) })
	picked, errPick := scheduler.pickSingle(t.Context(), "weighted-pending", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil || picked == nil || picked.ID != auths[1].ID {
		t.Fatalf("scheduler ignored pending credit: auth=%v err=%v", picked, errPick)
	}
	reservation.finish(false)
	picked, errPick = scheduler.pickSingle(t.Context(), "weighted-pending", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil || picked == nil || picked.ID != auths[0].ID {
		t.Fatalf("rollback lost committed credit: auth=%v err=%v", picked, errPick)
	}
	state := weightedStateForTest(t, weighted, "weighted-pending:", auths[0].ID, auths[1].ID)
	if state.inFlight != 0 || len(state.pendingForTest()) != 0 || state.currentForTest()[auths[0].ID] != 0 || state.currentForTest()[auths[1].ID] != 0 {
		t.Fatalf("pool did not settle: current=%v pending=%v inFlight=%d", state.currentForTest(), state.pendingForTest(), state.inFlight)
	}
}

func BenchmarkManagerWeighted1000(b *testing.B) {
	for _, mixed := range []bool{false, true} {
		b.Run(fmt.Sprintf("mixed=%t", mixed), func(b *testing.B) {
			manager, providers, model := benchmarkManagerSetup(b, 1000, mixed, false)
			manager.SetSelector(&WeightedRoundRobinSelector{})
			ctx := b.Context()
			opts := cliproxyexecutor.Options{}
			// Warm both the shard indexes and the pool state outside the timed loop.
			if mixed {
				if _, _, _, errWarm := manager.pickNextMixed(ctx, providers, model, opts, nil); errWarm != nil {
					b.Fatal(errWarm)
				}
			} else if _, _, errWarm := manager.pickNext(ctx, providers[0], model, opts, nil); errWarm != nil {
				b.Fatal(errWarm)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if mixed {
					if _, _, _, errPick := manager.pickNextMixed(ctx, providers, model, opts, nil); errPick != nil {
						b.Fatal(errPick)
					}
				} else if _, _, errPick := manager.pickNext(ctx, providers[0], model, opts, nil); errPick != nil {
					b.Fatal(errPick)
				}
			}
		})
	}
}
