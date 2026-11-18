package auth

import (
	"context"
	"github.com/MoeForge/moego/json"
	"reflect"
	"testing"
	"time"
)

func TestQuotaRecoveryBindingEmptyShadowSave(t *testing.T) {
	for _, operation := range []string{"update", "metadata"} {
		for _, diagnosis := range []string{"not_found", "legacy_quota"} {
			t.Run(operation+"/"+diagnosis, func(t *testing.T) {
				m, a, route, _ := quotaBindingFixture(t)
				own := time.Minute
				failure := &Error{HTTPStatus: 404, Message: "model not found"}
				if diagnosis == "legacy_quota" {
					failure = &Error{HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached"}}`}
				}
				m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: a.Provider, Model: route, Error: failure, RetryAfter: &own})
				quotaBindingFailure(m, a, route+"-reporter", route+"-reporter", time.Hour, "credential", false)
				before, _ := m.GetByID(a.ID)
				state := before.ModelStates[route]
				if state == nil || !isModelStateActiveCooldown(state.credentialQuotaShadow, time.Now()) || len(state.credentialQuotaShadow.Quota.recoveryBindings) != 0 || !state.Quota.NextRecoverAt.After(state.credentialQuotaShadow.NextRetryAfter) {
					t.Fatal("fixture lacks bindingless independent shadow under longer quota")
				}
				want := state.credentialQuotaShadow.Clone()
				calls := 0
				m.SetStore(&quotaBindingSaveStore{change: func(saved *Auth) {
					calls++
					raw, err := json.Marshal(saved)
					if err != nil {
						t.Fatal(err)
					}
					public := &Auth{}
					if err = json.Unmarshal(raw, public); err != nil {
						t.Fatal(err)
					}
					if public.ModelStates[route].credentialQuotaShadow != nil {
						t.Fatal("JSON unexpectedly retained private shadow")
					}
					saved.Quota, saved.ModelStates = public.Quota, public.ModelStates
				}})
				if operation == "update" {
					if _, err := m.Update(context.Background(), before.Clone()); err != nil {
						t.Fatal(err)
					}
				} else {
					okay, preempted := m.PersistMetadataEntry(context.Background(), a.ID, before.RegistrationEpoch, "save-sentinel", "bindingless-shadow", nil)
					if !okay || preempted {
						t.Fatalf("metadata save failed: okay=%v preempted=%v", okay, preempted)
					}
				}
				after, _ := m.GetByID(a.ID)
				got := after.ModelStates[route]
				if calls != 1 {
					t.Fatalf("expected exactly one public save, got%d", calls)
				}
				if !reflect.DeepEqual(got.credentialQuotaShadow, want) || !got.credentialQuotaPropagated || !got.Quota.NextRecoverAt.Equal(state.Quota.NextRecoverAt) {
					t.Fatal("JSON save lost bindingless independent restriction or changed its deadline")
				}
				if QuotaRecoveryCandidate(after) {
					t.Fatal("JSON save admitted active independent shadow for subscription recovery")
				}
			})
		}
	}
}
