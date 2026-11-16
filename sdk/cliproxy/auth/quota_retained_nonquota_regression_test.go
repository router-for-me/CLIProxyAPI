package auth

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestQuotaRecoveryRetainedNonQuotaDeadline(t *testing.T) {
	for _, scope := range []string{"model", "auth"} {
		for _, expired := range []bool{false, true} {
			age := "active"
			if expired {
				age = "expired"
			}
			t.Run(scope+"/"+age, func(t *testing.T) {
				ctx := context.Background()
				m := quotaProviderForceManager(t)
				cfg := &internalconfig.Config{}
				cfg.Codex.ModelLevelCooling = true
				m.SetConfig(cfg)
				store := NewFileCooldownStateStore(t.TempDir())
				m.SetCooldownStateStore(store)
				a, err := m.Register(ctx, &Auth{ID: t.Name(), Provider: "codex", Status: StatusActive})
				if err != nil {
					t.Fatal(err)
				}
				model := ""
				if scope == "model" {
					model = "gpt-6.1-sol"
				}
				m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: model, Error: &Error{HTTPStatus: http.StatusForbidden, Message: "permission restriction"}})
				forbidden, _ := m.GetByID(a.ID)
				retained := forbidden.nonQuotaCooldownUntil
				if scope == "model" {
					retained = forbidden.ModelStates[model].nonQuotaCooldownUntil
				}
				if !retained.After(time.Now()) {
					t.Fatal("403 did not create an independent non-quota deadline")
				}
				quota := time.Hour
				m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: model, Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: `{"error":{"type":"usage_limit_reached"}}`}, RetryAfter: &quota})
				before, _ := m.GetByID(a.ID)
				if before.LastError == nil || before.LastError.HTTPStatus != http.StatusTooManyRequests || effectiveAuthForceUntil(before).After(time.Now()) || effectiveCredQuotaForceUntil(before).After(time.Now()) {
					t.Fatal("later subscription quota did not replace the visible error without a forced deadline")
				}
				if scope == "model" {
					state := before.ModelStates[model]
					if state == nil || !state.nonQuotaCooldownUntil.Equal(retained) || !state.Quota.NextRecoverAt.After(retained) || state.credentialQuotaShadow != nil || effectiveModelForceUntil(state).After(time.Now()) || !before.nonQuotaCooldownUntil.IsZero() {
						t.Fatal("fixture did not isolate the model's retained non-quota deadline under longer quota")
					}
				} else if !before.nonQuotaCooldownUntil.Equal(retained) || !before.Quota.NextRecoverAt.After(retained) || len(before.ModelStates) != 0 {
					t.Fatal("fixture did not isolate the auth's retained non-quota deadline under longer quota")
				}
				if expired {
					// Advance only the independent restriction past expiry without sleeping.
					m.mu.Lock()
					current := m.auths[a.ID]
					if scope == "model" {
						current.ModelStates[model].nonQuotaCooldownUntil = time.Now().Add(-time.Minute)
					} else {
						current.nonQuotaCooldownUntil = time.Now().Add(-time.Minute)
					}
					m.mu.Unlock()
					before, _ = m.GetByID(a.ID)
				}
				if got := QuotaRecoveryCandidate(before); got != expired {
					t.Errorf("candidate=%v want=%v: subscription usage cannot release an active permission restriction", got, expired)
				}
				savedBefore, err := store.Load(ctx)
				if err != nil {
					t.Fatal(err)
				}
				got, _, err := m.ResetQuotaIfUnchanged(ctx, before)
				if err != nil {
					t.Fatal(err)
				}
				after, _ := m.GetByID(a.ID)
				savedAfter, err := store.Load(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if expired {
					if got == nil || after.Unavailable || after.Quota.Exceeded || after.LastError != nil || len(savedAfter) != 0 {
						t.Fatal("expired independent restriction prevented confirmed quota recovery")
					}
					if scope == "model" && (after.ModelStates[model].Unavailable || !after.ModelStates[model].nonQuotaCooldownUntil.IsZero()) {
						t.Fatal("expired model restriction was not cleared with quota")
					}
				} else if got != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(savedBefore, savedAfter) {
					t.Fatal("quota recovery erased active non-quota state or persisted restriction")
				}
			})
		}
	}
}
