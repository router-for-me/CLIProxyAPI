package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestBuiltInSelectorCooldownErrorPreservesRouteModel(t *testing.T) {
	t.Parallel()

	const routeModel = "client-opus(high)"
	next := time.Now().Add(time.Hour)
	auth := &Auth{
		ID:             "cooling-auth",
		Unavailable:    true,
		NextRetryAfter: next,
		Quota: QuotaState{
			Exceeded:      true,
			NextRecoverAt: next,
		},
		ModelStates: map[string]*ModelState{
			"other-model": {Status: StatusActive},
		},
	}

	selectors := map[string]Selector{
		"round-robin":          &RoundRobinSelector{},
		"weighted-round-robin": &WeightedRoundRobinSelector{},
		"fill-first":           &FillFirstSelector{},
	}
	for name, selector := range selectors {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, errPick := selector.Pick(
				context.Background(),
				"mixed",
				selectionArgForSelector(selector, routeModel),
				cliproxyexecutor.Options{},
				[]*Auth{auth},
			)
			if errPick == nil {
				t.Fatal("Pick() error = nil, want model cooldown")
			}

			errPick = restoreModelCooldownErrorModel(errPick, routeModel)
			var cooldownErr *modelCooldownError
			if !errors.As(errPick, &cooldownErr) {
				t.Fatalf("Pick() error = %T, want *modelCooldownError", errPick)
			}
			if cooldownErr.model != routeModel {
				t.Fatalf("cooldown model = %q, want %q", cooldownErr.model, routeModel)
			}
		})
	}
}
