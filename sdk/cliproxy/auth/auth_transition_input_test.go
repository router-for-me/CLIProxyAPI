package auth

import (
	"context"
	"testing"
)

func TestAuthTransitionCapturesInputBeforeProviderMutation(t *testing.T) {
	for _, kind := range []AuthUseKind{AuthUsePrepare, AuthUseRefresh} {
		t.Run(string(kind), func(t *testing.T) {
			f := newAuthUseFixture(t)
			_, _, err := f.manager.Modify(f.ctx, f.auth.ID, 0, func(a *Auth) error {
				a.Metadata["array"] = []any{map[string]any{"value": "original"}}
				return nil
			})
			if err != nil {
				t.Fatal("provider input fixture failed")
			}
			mutate := func(a *Auth) *Auth {
				a.Metadata["array"].([]any)[0].(map[string]any)["value"] = "provider-mutated"
				return a
			}
			f.exec.prepare = true
			f.exec.prepared = mutate
			e := &authTransitionExecutor{authUseExecutor: f.exec, refresh: func(_ context.Context, a *Auth) (*Auth, error) { return mutate(a), nil }}
			f.manager.RegisterExecutor(e)
			events := 0
			f.manager.SetAuthExecutionPolicy(&authTransitionPolicy{observe: func(_ context.Context, event AuthTransition) {
				events++
				if event.Before == nil || event.Merged == nil ||
					event.Before.Metadata["array"].([]any)[0].(map[string]any)["value"] != "original" ||
					event.Merged.Metadata["array"].([]any)[0].(map[string]any)["value"] != "provider-mutated" {
					t.Error("transition input was captured after provider mutation")
				}
			}})
			if kind == AuthUsePrepare {
				current, _ := f.manager.GetByID(f.auth.ID)
				_, err = f.manager.PrepareRequestAuth(f.ctx, e, current)
			} else {
				_, err = f.manager.ForceRefreshAuth(f.ctx, f.auth.ID)
			}
			if err != nil || events != 1 {
				t.Fatal("provider mutation did not produce a trusted transition")
			}
		})
	}
}
