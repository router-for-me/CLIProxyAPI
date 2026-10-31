package auth

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestRequestRefreshManagedSelectorPriorityAndAffinity(t *testing.T) {
	for _, mode := range []string{"round-robin", "weighted", "fill-first", "affinity"} {
		t.Run(mode, func(t *testing.T) {
			m, a, _, e := demandManager(t, time.Now().Add(-time.Hour))
			a.Attributes = map[string]string{"priority": "10", "auth_kind": "oauth"}
			_, _ = m.Update(context.Background(), a)
			lower := a.Clone()
			lower.ID = a.ID + "-lower"
			lower.Attributes = map[string]string{"priority": "1", "auth_kind": "oauth"}
			_, _ = m.Register(context.Background(), lower)
			registry.GetGlobalRegistry().RegisterClient(lower.ID, lower.Provider, []*registry.ModelInfo{{ID: "demand-model"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(lower.ID) })
			var selector Selector
			switch mode {
			case "round-robin":
				selector = &RoundRobinSelector{}
			case "weighted":
				selector = &WeightedRoundRobinSelector{}
			case "fill-first":
				selector = &FillFirstSelector{}
			case "affinity":
				affinity := NewSessionAffinitySelector(nil)
				selector = affinity
				t.Cleanup(affinity.Stop)
			}
			m.SetSelector(selector)
			opts := cliproxyexecutor.Options{Headers: map[string][]string{"X-Session-Id": {"synthetic-affinity"}}}
			for i := 0; i < 2; i++ {
				picked, _, _, err := m.pickNextMixedForExecution(withRequiredAuthKind(context.Background(), AuthKindOAuth), []string{a.Provider}, "demand-model", opts, nil)
				if err != nil || picked.ID != a.ID {
					t.Fatalf("priority/affinity pick=%v err=%v", picked, err)
				}
				if _, err = m.EnsureRequestAuth(context.Background(), picked, "demand-model"); err != nil {
					t.Fatal(err)
				}
			}
			if e.refreshes.Load() != 1 {
				t.Fatalf("selected auth refreshes=%d", e.refreshes.Load())
			}
			wrong := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: "missing"}}
			if _, _, _, err := m.pickNextMixedForExecution(context.Background(), []string{a.Provider}, "demand-model", wrong, nil); err == nil {
				t.Fatal("pinning widened")
			}
		})
	}
}

func TestRequestRefreshLegacyPolicyRemainsStrict(t *testing.T) {
	m, a, _, e := demandManager(t, time.Now().Add(-time.Hour))
	a.RefreshPolicy = pluginapi.RefreshDefault
	_, _ = m.Update(context.Background(), a)
	if _, _, _, err := m.pickNextMixedForExecution(context.Background(), []string{a.Provider}, "demand-model", cliproxyexecutor.Options{}, nil); err == nil {
		t.Fatal("opt-out execution widened")
	}
	if e.refreshes.Load() != 0 {
		t.Fatal("opt-out demand refreshed")
	}
}
