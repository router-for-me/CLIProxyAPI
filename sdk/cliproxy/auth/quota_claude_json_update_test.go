package auth

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestQuotaRecoveryClaudeJSONModelStateUpdate(t *testing.T) {
	for _, credentialScope := range []bool{false, true} {
		for _, rotate := range []bool{false, true} {
			t.Run(fmt.Sprintf("credential_scope=%v/rotate=%v", credentialScope, rotate), func(t *testing.T) {
				ctx := context.Background()
				prior := quotaCooldownDisabled.Load()
				quotaCooldownDisabled.Store(false)
				t.Cleanup(func() { quotaCooldownDisabled.Store(prior) })
				cfg := &internalconfig.Config{}
				cfg.Claude.ModelLevelCooling = true
				store := NewFileCooldownStateStore(t.TempDir())
				m := NewManager(nil, nil, nil)
				m.SetConfig(cfg)
				m.SetCooldownStateStore(store)
				a, err := m.Register(ctx, &Auth{ID: t.Name(), Provider: "claude", Status: StatusActive, Metadata: map[string]any{"account_uuid": "same", "access_token": "token", "refresh_token": "refresh"}})
				if err != nil {
					t.Fatal(err)
				}
				const model = "claude-opus-5-5"
				rate := 30 * time.Minute
				m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: model, RetryAfter: &rate, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"rate_limit_error","message":"This request would exceed the requests per minute rate limit. Please try again later."}}`}})
				a, _ = m.GetByID(a.ID)
				deadline := a.ModelStates[model].nonQuotaCooldownUntil
				if !deadline.After(time.Now()) {
					t.Fatal("Claude minute result did not establish a live independent deadline")
				}
				quota := 2 * time.Hour
				markSubscription := func() {
					m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: model, CredentialScope: credentialScope, RetryAfter: &quota, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"rate_limit_error","message":"Number of requests has exceeded your 5-hour rate limit."}}`}})
				}
				markSubscription()
				a, _ = m.GetByID(a.ID)
				shadow := a.ModelStates[model].credentialQuotaShadow.Clone()
				if credentialScope && (shadow == nil || !shadow.nonQuotaCooldownUntil.Equal(deadline)) {
					t.Fatal("credential quota did not wrap the independent Claude minute block")
				}
				if QuotaRecoveryCandidate(a) {
					t.Fatal("fixture lost active Claude minute before JSON update")
				}
				data, err := json.Marshal(a)
				if err != nil {
					t.Fatal(err)
				}
				var public Auth
				if err = json.Unmarshal(data, &public); err != nil {
					t.Fatal(err)
				}
				state := public.ModelStates[model]
				if state == nil || !state.nonQuotaCooldownUntil.IsZero() || state.credentialQuotaShadow != nil || state.credentialQuotaPropagated {
					t.Fatal("JSON round trip did not retain public model state while dropping private provenance")
				}
				if rotate {
					public.Metadata["access_token"] = "rotated"
				}
				if a, err = m.Update(ctx, &public); err != nil {
					t.Fatal(err)
				}
				state = a.ModelStates[model]
				if !a.ModelStates[model].nonQuotaCooldownUntil.Equal(deadline) {
					t.Error("JSON model-state update lost independent Claude minute deadline")
				}
				if credentialScope {
					if !reflect.DeepEqual(state.credentialQuotaShadow, shadow) || !state.credentialQuotaPropagated {
						t.Error("JSON model-state update lost hidden independent layer or propagation marker")
					}
				}
				if QuotaRecoveryCandidate(a) {
					t.Error("JSON model-state update made active Claude minute restriction recoverable")
				}
				savedBefore, err := store.Load(ctx)
				if err != nil {
					t.Fatal(err)
				}
				got, _, err := m.ResetQuotaIfUnchanged(ctx, a)
				if err != nil {
					t.Fatal(err)
				}
				after, _ := m.GetByID(a.ID)
				savedAfter, err := store.Load(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if got != nil || !reflect.DeepEqual(a, after) || !reflect.DeepEqual(savedBefore, savedAfter) {
					t.Fatal("healthy usage recovery cleared independent Claude minute restriction after JSON update")
				}
				restored := NewManager(nil, nil, nil)
				restored.SetConfig(cfg)
				if _, err = restored.Register(ctx, &Auth{ID: a.ID, Provider: a.Provider, Status: StatusActive, Metadata: a.Clone().Metadata}); err != nil {
					t.Fatal(err)
				}
				restored.SetCooldownStateStore(store)
				if err = restored.RestoreCooldownStates(ctx); err != nil {
					t.Fatal(err)
				}
				reloaded, _ := restored.GetByID(a.ID)
				if !reloaded.ModelStates[model].nonQuotaCooldownUntil.Equal(deadline) || QuotaRecoveryCandidate(reloaded) {
					t.Error("JSON update persisted loss of Claude minute provenance")
				}
				if credentialScope {
					markSubscription()
					a, _ = m.GetByID(a.ID)
					if !reflect.DeepEqual(a.ModelStates[model].credentialQuotaShadow, shadow) {
						t.Error("repeated subscription report replaced the independent layer with propagated quota")
					}
				}
				data, err = json.Marshal(a)
				if err != nil {
					t.Fatal(err)
				}
				var replacement Auth
				if err = json.Unmarshal(data, &replacement); err != nil {
					t.Fatal(err)
				}
				replacement.Metadata = map[string]any{"account_uuid": "different", "access_token": "other", "refresh_token": "other"}
				replaced, err := m.Update(ctx, &replacement)
				if err != nil {
					t.Fatal(err)
				}
				if !replaced.ModelStates[model].nonQuotaCooldownUntil.IsZero() || replaced.ModelStates[model].credentialQuotaShadow != nil {
					t.Fatal("different Claude account inherited private minute state")
				}
			})
		}
	}
}

func TestQuotaRecoveryClaudeJSONAuthUpdate(t *testing.T) {
	ctx := context.Background()
	prior := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prior) })
	m := NewManager(nil, nil, nil)
	store := NewFileCooldownStateStore(t.TempDir())
	m.SetCooldownStateStore(store)
	a, err := m.Register(ctx, &Auth{ID: t.Name(), Provider: "claude", Status: StatusActive, Metadata: map[string]any{"account_uuid": "same", "access_token": "token", "refresh_token": "refresh"}})
	if err != nil {
		t.Fatal(err)
	}
	minute := 30 * time.Minute
	m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, RetryAfter: &minute, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"rate_limit_error","message":"This request would exceed the requests per minute rate limit. Please try again later."}}`}})
	a, _ = m.GetByID(a.ID)
	deadline := a.nonQuotaCooldownUntil
	if !deadline.After(time.Now()) {
		t.Fatal("fixture missing active auth minute")
	}
	quota := 2 * time.Hour
	m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, RetryAfter: &quota, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"rate_limit_error","message":"Number of requests has exceeded your 5-hour rate limit."}}`}})
	a, _ = m.GetByID(a.ID)
	if QuotaRecoveryCandidate(a) {
		t.Fatal("fixture lost active auth minute before update")
	}
	data, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var public Auth
	if err = json.Unmarshal(data, &public); err != nil {
		t.Fatal(err)
	}
	if !public.nonQuotaCooldownUntil.IsZero() {
		t.Fatal("public auth retained private minute field")
	}
	public.Metadata["access_token"] = "rotated"
	a, err = m.Update(ctx, &public)
	if err != nil {
		t.Fatal(err)
	}
	if !a.nonQuotaCooldownUntil.Equal(deadline) {
		t.Error("JSON auth update lost active Claude minute deadline")
	}
	if QuotaRecoveryCandidate(a) {
		t.Error("JSON auth update made active Claude minute recoverable")
	}
	savedBefore, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := m.ResetQuotaIfUnchanged(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := m.GetByID(a.ID)
	savedAfter, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil || !reflect.DeepEqual(a, after) || !reflect.DeepEqual(savedBefore, savedAfter) {
		t.Fatal("healthy usage cleared active Claude auth minute after JSON update")
	}
}
