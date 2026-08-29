package auth

import (
	"context"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestRoutingPrioritySupportsPreferredRingEntryPoints(t *testing.T) {
	const (
		provider = "codex"
		upstream = "ring-native-model"
		routeA   = "ring-route-a"
		routeB   = "ring-route-b"
	)

	manager := NewManager(nil, nil, nil)
	manager.SetSelector(&FillFirstSelector{})
	manager.RegisterExecutor(schedulerTestExecutor{provider: provider})

	routes := []string{routeA, routeB}
	auths := []struct {
		id              string
		globalPriority  string
		routingPriority []int
	}{
		{id: "account-a", globalPriority: "300", routingPriority: []int{300, 200}},
		{id: "account-b", globalPriority: "200", routingPriority: []int{200, 300}},
		{id: "ark", globalPriority: "100", routingPriority: []int{100, 100}},
	}

	ctx := context.Background()
	for _, authSpec := range auths {
		auth := &Auth{
			ID:         authSpec.id,
			Provider:   provider,
			Status:     StatusActive,
			Attributes: map[string]string{"priority": authSpec.globalPriority},
		}
		aliases := make([]internalconfig.OAuthModelAlias, 0, len(routes))
		for index, route := range routes {
			priority := authSpec.routingPriority[index]
			aliases = append(aliases, internalconfig.OAuthModelAlias{
				Name:            upstream,
				Alias:           route,
				RoutingPriority: &priority,
			})
		}
		SetOAuthModelAliasesAttribute(auth, aliases)
		if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
			t.Fatalf("Register(%s): %v", auth.ID, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: upstream}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}

	for _, testCase := range []struct {
		name  string
		model string
		want  []string
	}{
		{name: "route A starts at A", model: routeA, want: []string{"account-a", "account-b", "ark"}},
		{name: "route B starts at B", model: routeB, want: []string{"account-b", "account-a", "ark"}},
		{name: "no override keeps global order", model: upstream, want: []string{"account-a", "account-b", "ark"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			tried := make(map[string]struct{}, len(testCase.want))
			for index, wantID := range testCase.want {
				selected, _, errPick := manager.pickNext(ctx, provider, testCase.model, cliproxyexecutor.Options{}, tried)
				if errPick != nil {
					t.Fatalf("pick #%d: %v", index, errPick)
				}
				if selected == nil || selected.ID != wantID {
					t.Fatalf("pick #%d = %#v, want %q", index, selected, wantID)
				}
				if _, duplicate := tried[selected.ID]; duplicate {
					t.Fatalf("pick #%d repeated credential %q", index, selected.ID)
				}
				tried[selected.ID] = struct{}{}
			}

			if selected, _, errPick := manager.pickNext(ctx, provider, testCase.model, cliproxyexecutor.Options{}, tried); errPick == nil || selected != nil {
				t.Fatalf("pick after one traversal = %#v, %v; want exhausted ring", selected, errPick)
			}
		})
	}
}
