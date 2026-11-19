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

func codexRateDeadline(auth *Auth, model string) time.Time {
	if model != "" {
		return auth.ModelStates[model].nonQuotaCooldownUntil
	}
	return auth.nonQuotaCooldownUntil
}

func TestQuotaRecoveryCodexRetainedRate(t *testing.T) {
	for _, scope := range []string{"model", "auth"} {
		for _, restored := range []bool{false, true} {
			for _, refreshed := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/restored=%v/refreshed=%v", scope, restored, refreshed), func(t *testing.T) {
					for _, code := range []string{"", ErrorCodeTransientRateLimit} {
						ctx := context.Background()
						prior := quotaCooldownDisabled.Load()
						quotaCooldownDisabled.Store(false)
						t.Cleanup(func() { quotaCooldownDisabled.Store(prior) })
						cfg := &internalconfig.Config{}
						cfg.Codex.ModelLevelCooling = true
						store := NewFileCooldownStateStore(t.TempDir())
						m := NewManager(nil, nil, nil)
						m.SetConfig(cfg)
						m.SetCooldownStateStore(store)
						a, err := m.Register(ctx, &Auth{ID: t.Name() + code, Provider: "codex", Status: StatusActive, Metadata: map[string]any{"account_id": "same", "access_token": "old", "refresh_token": "refresh"}})
						if err != nil {
							t.Fatal(err)
						}
						model := ""
						if scope == "model" {
							model = "gpt-5"
						}
						rate := 45 * time.Second
						markRate := func() {
							m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: model, Error: &Error{Code: code, HTTPStatus: 429, Message: `{"error":{"type":"rate_limit_exceeded","message":"usage_limit_reached is not this error type"}}`}, RetryAfter: &rate})
						}
						start := time.Now()
						markRate()
						a, _ = m.GetByID(a.ID)
						deadline := codexRateDeadline(a, model)
						if deadline.Before(start.Add(44*time.Second)) || deadline.After(time.Now().Add(46*time.Second)) {
							t.Fatalf("own Codex rate deadline=%v; want45s", deadline)
						}
						if QuotaRecoveryCandidate(a) {
							t.Fatal("generic Codex429 already eligible for subscription recovery")
						}
						quota := time.Hour
						m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: model, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached"}}`}, RetryAfter: &quota})
						if restored {
							m = NewManager(nil, nil, nil)
							m.SetConfig(cfg)
							if _, err = m.Register(ctx, &Auth{ID: a.ID, Provider: a.Provider, Status: StatusActive, Metadata: map[string]any{"account_id": "same", "access_token": "old", "refresh_token": "refresh"}}); err != nil {
								t.Fatal(err)
							}
							m.SetCooldownStateStore(store)
							if err = m.RestoreCooldownStates(ctx); err != nil {
								t.Fatal(err)
							}
						}
						a, _ = m.GetByID(a.ID)
						if refreshed {
							fresh := &Auth{ID: a.ID, Provider: a.Provider, Status: StatusActive, Metadata: map[string]any{"account_id": "same", "access_token": "rotated", "refresh_token": "refresh"}}
							if a, err = m.UpdateRefreshedAuth(ctx, a, fresh); err != nil {
								t.Fatal(err)
							}
						}
						// A public same-account replacement omits runtime-only provenance;
						// model states are absent as on ordinary credential content updates.
						public := &Auth{ID: a.ID, Provider: a.Provider, Status: a.Status, StatusMessage: a.StatusMessage, Unavailable: a.Unavailable, NextRetryAfter: a.NextRetryAfter, Quota: a.Quota.Clone(), LastError: cloneError(a.LastError), Metadata: a.Clone().Metadata}
						if !public.nonQuotaCooldownUntil.IsZero() {
							t.Fatal("public replacement unexpectedly carried private provenance")
						}
						if a, err = m.Update(ctx, public); err != nil {
							t.Fatal(err)
						}
						if !codexRateDeadline(a, model).Equal(deadline) {
							t.Fatal("later subscription, persistence or same-account update lost own Codex rate deadline")
						}
						savedBefore, err := store.Load(ctx)
						if err != nil {
							t.Fatal(err)
						}
						if QuotaRecoveryCandidate(a) {
							t.Error("later subscription429 erased active independent Codex rate deadline")
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
							t.Fatal("healthy subscription recovery released independent Codex rate restriction")
						}
						// Expire only the independently attributed layer, leaving the later
						// subscription cooldown active, without sleeping or seeding initial state.
						m.mu.Lock()
						if model != "" {
							m.auths[a.ID].ModelStates[model].nonQuotaCooldownUntil = time.Now().Add(-time.Minute)
						} else {
							m.auths[a.ID].nonQuotaCooldownUntil = time.Now().Add(-time.Minute)
						}
						m.mu.Unlock()
						a, _ = m.GetByID(a.ID)
						if !QuotaRecoveryCandidate(a) {
							t.Fatal("expired independent Codex rate restriction prevented subscription recovery")
						}
						got, _, err = m.ResetQuotaIfUnchanged(ctx, a)
						if err != nil {
							t.Fatal(err)
						}
						a, _ = m.GetByID(a.ID)
						records, err := store.Load(ctx)
						if err != nil {
							t.Fatal(err)
						}
						if got == nil || a.Unavailable || !codexRateDeadline(a, model).IsZero() || len(records) != 0 {
							t.Fatal("expired independent Codex restriction did not recover and retire persisted cooldown")
						}
						markRate()
						m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: model, Success: true})
						a, _ = m.GetByID(a.ID)
						if !codexRateDeadline(a, model).IsZero() {
							t.Fatal("success retained Codex rate restriction")
						}
						markRate()
						if a, _, err = m.ResetQuota(ctx, a.ID); err != nil {
							t.Fatal(err)
						}
						if !codexRateDeadline(a, model).IsZero() {
							t.Fatal("manual reset retained Codex rate restriction")
						}
					}
				})
			}
		}
	}
}

func TestCodexRateLimitAttribution(t *testing.T) {
	prior := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prior) })
	cases := []struct {
		message, code string
		independent   bool
	}{
		{`{"error":{"type":"rate_limit_exceeded","message":"usage_limit_reached"}}`, "", true},
		{`{"error":{"type":"usage_limit_reached"}}`, "", false},
		{`{"type":"usage_limit_reached"}`, "", false},
		{`{"body":{"error":{"type":"usage_limit_reached"}}}`, "", false},
		{`{"response":{"error":{"type":"usage_limit_reached"}}}`, "", false},
		{`{"error":{"type":"rate_limit_exceeded"}}`, ErrorCodeCredentialQuota, false},
	}
	for _, provider := range []string{"codex", "openai"} {
		for _, model := range []string{"", "gpt-5"} {
			for i, tc := range cases {
				m := NewManager(nil, nil, nil)
				cfg := &internalconfig.Config{}
				cfg.Codex.ModelLevelCooling = true
				m.SetConfig(cfg)
				a, err := m.Register(context.Background(), &Auth{ID: fmt.Sprintf("%s/%s/%d", provider, model, i), Provider: provider, Status: StatusActive})
				if err != nil {
					t.Fatal(err)
				}
				d := 45 * time.Second
				m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: provider, Model: model, RetryAfter: &d, Error: &Error{HTTPStatus: 429, Message: tc.message, Code: tc.code}})
				a, _ = m.GetByID(a.ID)
				want := provider == "codex" && tc.independent
				if got := codexRateDeadline(a, model).After(time.Now()); got != want {
					t.Fatalf("provider=%s model=%s case%d independent=%v want%v", provider, model, i, got, want)
				}
			}
		}
	}
}

func TestCodexRateLimitShadowAndMerge(t *testing.T) {
	prior := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prior) })
	ctx := context.Background()
	store := NewFileCooldownStateStore(t.TempDir())
	m := NewManager(nil, nil, nil)
	m.SetCooldownStateStore(store)
	a, err := m.Register(ctx, &Auth{ID: t.Name(), Provider: "codex", Status: StatusActive})
	if err != nil {
		t.Fatal(err)
	}
	d := 45 * time.Second
	m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: "gpt-5", RetryAfter: &d, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"rate_limit_exceeded"}}`}})
	a, _ = m.GetByID(a.ID)
	deadline := codexRateDeadline(a, "gpt-5")
	hour := time.Hour
	m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: "gpt-5", CredentialScope: true, RetryAfter: &hour, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached"}}`}})
	restored := NewManager(nil, nil, nil)
	if _, err = restored.Register(ctx, &Auth{ID: a.ID, Provider: a.Provider, Status: StatusActive}); err != nil {
		t.Fatal(err)
	}
	restored.SetCooldownStateStore(store)
	if err = restored.RestoreCooldownStates(ctx); err != nil {
		t.Fatal(err)
	}
	a, _ = restored.GetByID(a.ID)
	shadow := a.ModelStates["gpt-5"].credentialQuotaShadow
	if shadow == nil || !shadow.nonQuotaCooldownUntil.Equal(deadline) || !deadline.After(time.Now()) {
		t.Fatal("credential wrap or restart lost independent Codex rate deadline")
	}
	earlier := shadow.Clone()
	earlier.nonQuotaCooldownUntil = deadline.Add(-time.Second)
	earlier.UpdatedAt = shadow.UpdatedAt.Add(time.Minute)
	merged := mergeModelState(shadow.Clone(), earlier)
	if !merged.nonQuotaCooldownUntil.Equal(deadline) {
		t.Fatal("newer diagnostic shortened independent Codex rate deadline")
	}
}

func TestQuotaRecoveryCodexJSONModelStateUpdate(t *testing.T) {
	for _, credentialScope := range []bool{false, true} {
		t.Run(fmt.Sprintf("credential_scope=%v", credentialScope), func(t *testing.T) {
			ctx := context.Background()
			prior := quotaCooldownDisabled.Load()
			quotaCooldownDisabled.Store(false)
			t.Cleanup(func() { quotaCooldownDisabled.Store(prior) })
			cfg := &internalconfig.Config{}
			cfg.Codex.ModelLevelCooling = true
			store := NewFileCooldownStateStore(t.TempDir())
			m := NewManager(nil, nil, nil)
			m.SetConfig(cfg)
			m.SetCooldownStateStore(store)
			a, err := m.Register(ctx, &Auth{ID: t.Name(), Provider: "codex", Status: StatusActive, Metadata: map[string]any{"account_id": "same", "access_token": "token", "refresh_token": "refresh"}})
			if err != nil {
				t.Fatal(err)
			}
			const model = "gpt-5"
			rate := 30 * time.Minute
			m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: model, RetryAfter: &rate, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"rate_limit_exceeded"}}`}})
			a, _ = m.GetByID(a.ID)
			deadline := codexRateDeadline(a, model)
			if !deadline.After(time.Now()) {
				t.Fatal("request-rate result did not establish a live independent deadline")
			}
			quota := 2 * time.Hour
			markSubscription := func() {
				m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: model, CredentialScope: credentialScope, RetryAfter: &quota, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached"}}`}})
			}
			markSubscription()
			a, _ = m.GetByID(a.ID)
			shadow := a.ModelStates[model].credentialQuotaShadow.Clone()
			if credentialScope && (shadow == nil || !shadow.nonQuotaCooldownUntil.Equal(deadline)) {
				t.Fatal("credential quota did not wrap the independent request-rate block")
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
			if a, err = m.Update(ctx, &public); err != nil {
				t.Fatal(err)
			}
			state = a.ModelStates[model]
			if !codexRateDeadline(a, model).Equal(deadline) {
				t.Error("JSON model-state update lost independent request-rate deadline")
			}
			if credentialScope {
				if !reflect.DeepEqual(state.credentialQuotaShadow, shadow) || !state.credentialQuotaPropagated {
					t.Error("JSON model-state update lost hidden independent layer or propagation marker")
				}
			}
			if QuotaRecoveryCandidate(a) {
				t.Error("JSON model-state update made active request-rate restriction recoverable")
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
				t.Fatal("healthy usage recovery cleared independent request-rate restriction after JSON update")
			}
			if credentialScope {
				markSubscription()
				a, _ = m.GetByID(a.ID)
				if !reflect.DeepEqual(a.ModelStates[model].credentialQuotaShadow, shadow) {
					t.Error("repeated subscription report replaced the independent layer with propagated quota")
				}
			}
		})
	}
}
