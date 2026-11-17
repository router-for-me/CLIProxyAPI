package auth

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestQuotaRecoveryForceJSONUpdate(t *testing.T) {
	for _, scope := range []string{"auth", "model", "wrapped", "credential"} {
		t.Run(scope, func(t *testing.T) {
			ctx := context.Background()
			m := quotaProviderForceManager(t)
			cfg := &internalconfig.Config{}
			cfg.Codex.ModelLevelCooling = true
			m.SetConfig(cfg)
			store := NewFileCooldownStateStore(t.TempDir())
			m.SetCooldownStateStore(store)
			a, err := m.Register(ctx, &Auth{ID: t.Name(), Provider: "codex", Status: StatusActive, Metadata: map[string]any{"account_id": "same", "access_token": "token", "refresh_token": "refresh"}})
			if err != nil {
				t.Fatal(err)
			}
			model := "gpt-6.1-sol"
			if scope == "auth" {
				model = ""
			}
			forced := 30 * time.Minute
			m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: model, CredentialScope: scope == "credential", RetryAfter: &forced, Error: &Error{HTTPStatus: 429, Code: ErrorCodeForceCooldown, Message: "operator forced cooldown"}})
			quota := 2 * time.Hour
			m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: model, CredentialScope: scope == "wrapped" || scope == "credential", RetryAfter: &quota, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached"}}`}})
			before, _ := m.GetByID(a.ID)
			if QuotaRecoveryCandidate(before) {
				t.Fatal("fixture lost force restriction before JSON update")
			}
			var old *ModelState
			if model != "" {
				old = before.ModelStates[model].Clone()
			}
			data, err := json.Marshal(before)
			if err != nil {
				t.Fatal(err)
			}
			var public Auth
			if err = json.Unmarshal(data, &public); err != nil {
				t.Fatal(err)
			}
			if public.forceCooldownSeeded || !public.forceCooldownUntil.IsZero() || !public.credQuotaForceUntil.IsZero() {
				t.Fatal("JSON auth retained private force state")
			}
			if model != "" && (public.ModelStates[model].forceCooldownSeeded || !public.ModelStates[model].forceCooldownUntil.IsZero()) {
				t.Fatal("JSON model retained private force state")
			}
			after, err := m.Update(ctx, &public)
			if err != nil {
				t.Fatal(err)
			}
			if !after.forceCooldownUntil.Equal(before.forceCooldownUntil) || !after.quotaForceCooldownUntil.Equal(before.quotaForceCooldownUntil) || !after.credQuotaForceUntil.Equal(before.credQuotaForceUntil) {
				t.Error("JSON auth update lost retained force deadlines")
			}
			if model != "" {
				state := after.ModelStates[model]
				if !state.forceCooldownUntil.Equal(old.forceCooldownUntil) {
					t.Error("JSON model update lost retained force deadline")
				}
				if !state.quotaForceCooldownUntil.Equal(old.quotaForceCooldownUntil) {
					t.Error("JSON model update lost quota force attribution")
				}
				if !reflect.DeepEqual(state.credentialQuotaShadow, old.credentialQuotaShadow) || state.credentialQuotaPropagated != old.credentialQuotaPropagated {
					t.Error("JSON force update lost independent shadow provenance")
				}
			}
			if QuotaRecoveryCandidate(after) {
				t.Error("JSON update made active operator force recoverable")
			}
			savedBefore, err := store.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := m.ResetQuotaIfUnchanged(ctx, after)
			if err != nil {
				t.Fatal(err)
			}
			unchanged, _ := m.GetByID(a.ID)
			savedAfter, err := store.Load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got != nil || !reflect.DeepEqual(after, unchanged) || !reflect.DeepEqual(savedBefore, savedAfter) {
				t.Fatal("healthy usage cleared active force after JSON update")
			}
			reset, _, err := m.ResetQuota(ctx, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !effectiveAuthForceUntil(reset).IsZero() || !effectiveCredQuotaForceUntil(reset).IsZero() {
				t.Fatal("manual reset retained auth force after JSON update")
			}
			if model != "" && (!effectiveModelForceUntil(reset.ModelStates[model]).IsZero() || !reset.ModelStates[model].quotaForceCooldownUntil.IsZero()) {
				t.Fatal("manual reset retained model force after JSON update")
			}
		})
	}
}

func TestForceJSONUpdateKeepsOwnDeadline(t *testing.T) {
	for _, scope := range []string{"auth", "model"} {
		t.Run(scope, func(t *testing.T) {
			ctx := context.Background()
			m := quotaProviderForceManager(t)
			cfg := &internalconfig.Config{}
			cfg.Codex.ModelLevelCooling = true
			m.SetConfig(cfg)
			a, err := m.Register(ctx, &Auth{ID: t.Name(), Provider: "codex", Status: StatusActive, Metadata: map[string]any{"access_token": "token"}})
			if err != nil {
				t.Fatal(err)
			}
			model := "gpt-6.1-sol"
			if scope == "auth" {
				model = ""
			}
			quota := 2 * time.Hour
			m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: model, RetryAfter: &quota, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached"}}`}})
			forced := 30 * time.Minute
			m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: model, RetryAfter: &forced, Error: &Error{HTTPStatus: 429, Code: ErrorCodeForceCooldown, Message: "operator forced cooldown"}})
			before, _ := m.GetByID(a.ID)
			deadline := before.forceCooldownUntil
			retry := before.NextRetryAfter
			if model != "" {
				deadline = before.ModelStates[model].forceCooldownUntil
				retry = before.ModelStates[model].NextRetryAfter
			}
			if !deadline.After(time.Now()) || !retry.After(deadline) {
				t.Fatal("fixture did not isolate shorter own force beneath longer visible retry")
			}
			data, err := json.Marshal(before)
			if err != nil {
				t.Fatal(err)
			}
			var public Auth
			if err = json.Unmarshal(data, &public); err != nil {
				t.Fatal(err)
			}
			after, err := m.Update(ctx, &public)
			if err != nil {
				t.Fatal(err)
			}
			actual := after.forceCooldownUntil
			actualQuota := after.quotaForceCooldownUntil
			if model != "" {
				actual = after.ModelStates[model].forceCooldownUntil
				actualQuota = after.ModelStates[model].quotaForceCooldownUntil
			}
			if !actual.Equal(deadline) {
				t.Error("JSON update reseeded force from longer visible retry")
			}
			if !actualQuota.Equal(deadline) {
				t.Error("JSON update lost own quota force deadline")
			}
		})
	}
}
