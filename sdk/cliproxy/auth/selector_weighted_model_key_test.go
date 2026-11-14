package auth

import (
	"fmt"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestWeightedSelectorsShareEquivalentModelKeys(t *testing.T) {
	for _, firstAffinity := range []bool{false, true} {
		for _, secondAffinity := range []bool{false, true} {
			for _, routeOverride := range []bool{false, true} {
				t.Run(fmt.Sprintf("affinity=%t/%t/route=%t", firstAffinity, secondAffinity, routeOverride), func(t *testing.T) {
					weighted := &WeightedRoundRobinSelector{}
					affinity := NewSessionAffinitySelector(weighted)
					t.Cleanup(affinity.Stop)
					auths := []*Auth{{ID: "a"}, {ID: "b"}}
					for i, useAffinity := range []bool{firstAffinity, secondAffinity} {
						ctx := t.Context()
						model := []string{" Model(high) ", "model(low)"}[i]
						if routeOverride {
							ctx = withWeightedSelectorStateModel(ctx, weighted, model)
							model = []string{"upstream-a", "upstream-b"}[i]
						}
						var selector Selector = weighted
						if useAffinity {
							selector = affinity
						}
						picked, errPick := selector.Pick(ctx, "codex", model, cliproxyexecutor.Options{}, auths)
						if errPick != nil || picked == nil || picked.ID != auths[i].ID {
							t.Fatalf("request %d: pick=%v err=%v, want auth %s", i, picked, errPick, auths[i].ID)
						}
					}
				})
			}
		}
	}
}
