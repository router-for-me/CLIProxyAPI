package auth

import (
	"context"
	"errors"
	"testing"
)

func TestAuthTransitionTrustedProviderCommit(t *testing.T) {
	for _, kind := range []AuthUseKind{AuthUsePrepare, AuthUseRefresh} {
		for _, storeChange := range []bool{false, true} {
			name := string(kind) + "/unchanged-store"
			if storeChange {
				name = string(kind) + "/store-replaces-credential"
			}
			t.Run(name, func(t *testing.T) {
				f := newAuthUseFixture(t)
				e := &authTransitionExecutor{authUseExecutor: f.exec, refresh: rotatedTransitionAuth}
				f.manager.RegisterExecutor(e)
				f.exec.prepare = true
				f.exec.prepared = func(a *Auth) *Auth { a.Metadata["access_token"] = "synthetic-rotated"; return a }
				if storeChange {
					f.manager.SetStore(&authTransitionStore{save: func(a *Auth) {
						a.Metadata["access_token"] = "synthetic-store"
						a.Attributes["source"] = "store"
					}})
				}
				var events []AuthTransition
				f.manager.SetAuthExecutionPolicy(&authTransitionPolicy{observe: func(ctx context.Context, event AuthTransition) {
					if ctx.Value(authUseContextKey{}) != "fixture-context" {
						t.Error("transition lost request context")
					}
					events = append(events, event)
				}})
				var err error
				if kind == AuthUsePrepare {
					_, err = f.manager.PrepareRequestAuth(f.ctx, e, f.auth)
				} else {
					_, err = f.manager.ForceRefreshAuth(f.ctx, f.auth.ID)
				}
				if err != nil || len(events) != 1 {
					t.Fatal("trusted provider commit did not emit one transition")
				}
				event := events[0]
				if event.Kind != kind || event.Before == nil || event.CommitBase == nil || event.Merged == nil || event.Committed == nil ||
					authAccessToken(event.Before) != "synthetic-a" || authAccessToken(event.CommitBase) != "synthetic-a" || authAccessToken(event.Merged) != "synthetic-rotated" {
					t.Fatal("transition does not describe actual provider merge")
				}
				want := "synthetic-rotated"
				if storeChange {
					want = "synthetic-store"
				}
				current, ok := f.manager.GetByID(f.auth.ID)
				if !ok || authAccessToken(event.Committed) != want || authAccessToken(current) != want ||
					event.Before.RegistrationEpoch != event.Committed.RegistrationEpoch || event.Merged.Attributes["source"] != "fixture" {
					t.Fatal("transition concealed saved-state changes")
				}
			})
		}
	}
}

func TestAuthTransitionPublicUpdatesDoNotProveProviderWork(t *testing.T) {
	for _, operation := range []string{"update", "modify", "prepare", "refresh"} {
		t.Run(operation, func(t *testing.T) {
			f := newAuthUseFixture(t)
			events := 0
			f.manager.SetAuthExecutionPolicy(&authTransitionPolicy{observe: func(context.Context, AuthTransition) { events++ }})
			updated := f.auth.Clone()
			updated.Metadata["access_token"] = "synthetic-manual"
			var err error
			switch operation {
			case "update":
				_, err = f.manager.Update(f.ctx, updated)
			case "modify":
				_, _, err = f.manager.Modify(f.ctx, f.auth.ID, 0, func(a *Auth) error { a.Metadata["access_token"] = "synthetic-manual"; return nil })
			case "prepare":
				_, err = f.manager.UpdatePreparedAuth(f.ctx, f.auth, updated)
			case "refresh":
				_, err = f.manager.UpdateRefreshedAuth(f.ctx, f.auth, updated)
			}
			if err != nil || events != 0 {
				t.Fatal("public update manufactured provider transition")
			}
		})
	}
}

func TestAuthTransitionAbsentWithoutTrustedCommit(t *testing.T) {
	for _, outcome := range []string{"failed", "replaced-during-refresh", "cached-token"} {
		t.Run(outcome, func(t *testing.T) {
			f := newAuthUseFixture(t)
			e := &authTransitionExecutor{authUseExecutor: f.exec, refresh: func(ctx context.Context, a *Auth) (*Auth, error) {
				if outcome == "failed" {
					return nil, errors.New("fixture refresh failed")
				}
				f.replace(t, "synthetic-manual")
				return rotatedTransitionAuth(ctx, a)
			}}
			f.manager.RegisterExecutor(e)
			events := 0
			f.manager.SetAuthExecutionPolicy(&authTransitionPolicy{observe: func(context.Context, AuthTransition) { events++ }})
			if outcome == "cached-token" {
				f.replace(t, "synthetic-manual")
				_, err := f.manager.refreshAuthForRequest(f.ctx, f.auth.ID, "synthetic-a")
				if err != nil || e.refreshCalls.Load() != 0 {
					t.Fatal("cached-token control invoked provider refresh")
				}
			} else {
				_, err := f.manager.ForceRefreshAuth(f.ctx, f.auth.ID)
				if (err != nil) != (outcome == "failed") || e.refreshCalls.Load() != 1 {
					t.Fatal("refresh control did not take intended path")
				}
			}
			if events != 0 {
				t.Fatal("transition emitted without a trusted committed provider result")
			}
		})
	}
}
