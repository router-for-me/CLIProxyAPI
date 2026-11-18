package auth

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestGreedySelectorTransitionRefreshesExistingShards(t *testing.T) {
	for _, tc := range []struct {
		name     string
		selector Selector
	}{
		{"round-robin", &RoundRobinSelector{}}, {"fill-first", &FillFirstSelector{}}, {"weighted-round-robin", &WeightedRoundRobinSelector{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, tc.selector, nil)
			reg := registry.GetGlobalRegistry()
			now := time.Now().UTC().Truncate(time.Second)
			a := &Auth{ID: "transition-a-" + tc.name, Provider: "claude", Status: StatusActive, QuotaResetSchedule: QuotaResetSchedule{WeeklyResetAt: now.Add(2 * time.Hour), ObservedAt: now.Add(-time.Hour)}}
			b := &Auth{ID: "transition-b-" + tc.name, Provider: "claude", Status: StatusActive, QuotaResetSchedule: QuotaResetSchedule{WeeklyResetAt: now.Add(3 * time.Hour), ObservedAt: now.Add(-time.Hour)}}
			for _, auth := range []*Auth{a, b} {
				reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: "transition-model-a"}, {ID: "transition-model-b"}})
				id := auth.ID
				t.Cleanup(func() { reg.UnregisterClient(id) })
				if _, err := manager.Register(WithSkipPersist(context.Background()), auth); err != nil {
					t.Fatal(err)
				}
			}
			pick := func(model string) *Auth {
				t.Helper()
				got, err := manager.scheduler.pickSingle(context.Background(), "claude", model, cliproxyexecutor.Options{}, nil)
				if err != nil || got == nil {
					t.Fatalf("pick %s: %v %v", model, got, err)
				}
				return got
			}
			pick("transition-model-a")
			pick("transition-model-b")
			manager.syncScheduler()
			ctx := internallogging.WithResponseHeadersHolder(WithSkipPersist(context.Background()))
			headers := http.Header{}
			headers.Set("Anthropic-Ratelimit-Unified-7d-Reset", strconv.FormatInt(now.Add(4*time.Hour).Unix(), 10))
			internallogging.SetResponseHeaders(ctx, headers)
			manager.MarkResult(ctx, Result{AuthID: a.ID, Provider: "claude", Model: "transition-model-a", Success: true})
			manager.SetSelector(&GreedySelector{})
			for _, model := range []string{"transition-model-a", "transition-model-b"} {
				if got := pick(model); got.ID != b.ID {
					t.Fatalf("hot greedy selection for %s kept stale account rank: got=%s want=%s", model, got.ID, b.ID)
				}
			}
		})
	}
}
