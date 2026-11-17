package auth

import (
	"context"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestQuotaRecoveryJSONPropagatedOnlyUpdate(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			ctx := context.Background()
			m := quotaProviderForceManager(t)
			cfg := &internalconfig.Config{}
			cfg.Codex.ModelLevelCooling = true
			cfg.Claude.ModelLevelCooling = true
			m.SetConfig(cfg)
			a, err := m.Register(ctx, &Auth{ID: t.Name(), Provider: provider, Status: StatusActive, Metadata: map[string]any{"access_token": "token", "account_id": "same"}})
			if err != nil {
				t.Fatal(err)
			}
			const model = "gpt-6.1-sol"
			quota := 2 * time.Hour
			mark := func() {
				m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: model, CredentialScope: true, RetryAfter: &quota, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached"}}`}})
			}
			mark()
			before, _ := m.GetByID(a.ID)
			if !QuotaRecoveryCandidate(before) || !before.ModelStates[model].credentialQuotaPropagated || before.ModelStates[model].credentialQuotaShadow != nil {
				t.Fatal("fixture did not isolate quota-only propagated state")
			}
			data, err := json.Marshal(before)
			if err != nil {
				t.Fatal(err)
			}
			var public Auth
			if err = json.Unmarshal(data, &public); err != nil {
				t.Fatal(err)
			}
			if _, err = m.Update(ctx, &public); err != nil {
				t.Fatal(err)
			}
			mark()
			after, _ := m.GetByID(a.ID)
			if after.ModelStates[model].credentialQuotaShadow != nil {
				t.Error("JSON update made repeated credential quota invent an independent shadow")
			}
			if !QuotaRecoveryCandidate(after) {
				t.Error("JSON update made quota-only repeated report unrecoverable")
			}
			got, _, err := m.ResetQuotaIfUnchanged(ctx, after)
			if err != nil {
				t.Fatal(err)
			}
			if got == nil || got.Unavailable {
				t.Fatal("healthy usage could not clear repeated propagated quota after JSON update")
			}
			mark()
			current, _ := m.GetByID(a.ID)
			data, err = json.Marshal(current)
			if err != nil {
				t.Fatal(err)
			}
			var replacement Auth
			if err = json.Unmarshal(data, &replacement); err != nil {
				t.Fatal(err)
			}
			replacement.Metadata = map[string]any{"account_id": "different", "access_token": "other"}
			replaced, err := m.Update(ctx, &replacement)
			if err != nil {
				t.Fatal(err)
			}
			if replaced.ModelStates[model].credentialQuotaPropagated || replaced.ModelStates[model].credentialQuotaShadow != nil {
				t.Fatal("replacement credential inherited private propagation state")
			}
		})
	}
}
