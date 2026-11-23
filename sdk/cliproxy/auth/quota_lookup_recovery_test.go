package auth

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func observeAvailableQuota(t *testing.T, manager *Manager, auth *Auth, source string) *Auth {
	t.Helper()
	lookupAt := auth.UpdatedAt.Add(time.Second)
	if source == "windows" {
		available := 5.0
		recorded, err := manager.ObserveQuotaLookupWindows(t.Context(), auth.ID, []QuotaWindow{
			{Key: "weekly", UsedPercent: &available}, {Key: "scoped", Scoped: true, Status: QuotaWindowStatusRejected},
		}, lookupAt, nil)
		if !recorded || err != nil {
			t.Fatalf("lookup recorded = %v, err = %v", recorded, err)
		}
	} else {
		headers := recoveryLookupHeaders(auth.Provider, false)
		headers.Set("Anthropic-Ratelimit-Unified-7d_oi-Utilization", "1")
		if !manager.ObserveQuotaLookup(auth.ID, headers, lookupAt) {
			t.Fatal("lookup not recorded")
		}
	}
	after, _ := manager.GetByID(auth.ID)
	return after
}

func TestLookupRecoveryPreservesIndependentModelQuota(t *testing.T) {
	for _, source := range []string{"windows", "signals"} {
		t.Run(source, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			now := time.Now()
			credQuota := QuotaState{Exceeded: true, Reason: credentialQuotaReason, NextRecoverAt: now.Add(2 * time.Hour)}
			quotaError := &Error{HTTPStatus: http.StatusTooManyRequests}
			independent := &ModelState{
				Status: StatusError, Unavailable: true, LastError: quotaError, UpdatedAt: now,
				NextRetryAfter: now.Add(time.Hour), cooldownRecordedSeq: 1,
				Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(time.Hour)},
			}
			shadow := independent.cloneShallow()
			shadow.credentialQuotaPropagated = true
			auth := &Auth{
				ID: "lookup-model-layers", Provider: "claude", UpdatedAt: now,
				Status: StatusError, Unavailable: true, LastError: quotaError, NextRetryAfter: credQuota.NextRecoverAt,
				Quota: credQuota, credQuotaForceUntil: credQuota.NextRecoverAt, forceCooldownSeeded: true,
				ModelStates: map[string]*ModelState{
					"independent": independent,
					"wrapped": {
						Status: StatusError, Unavailable: true, Quota: credQuota, NextRetryAfter: credQuota.NextRecoverAt,
						UpdatedAt: now, credentialQuotaPropagated: true, credentialQuotaShadow: shadow,
					},
					"reporting": {
						Status: StatusError, Unavailable: true, NextRetryAfter: credQuota.NextRecoverAt, LastError: quotaError,
						Quota:     QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: credQuota.NextRecoverAt},
						UpdatedAt: now, credentialQuotaPropagated: true,
					},
					"legacy-wrap": {Status: StatusError, Unavailable: true, Quota: credQuota, UpdatedAt: now},
				},
			}
			if source == "windows" {
				auth.Provider = "antigravity"
			}
			manager.auths[auth.ID] = auth
			wantIndependent := independent.cloneShallow()
			after := observeAvailableQuota(t, manager, auth, source)
			if after.Unavailable || !after.NextRetryAfter.IsZero() || !after.credQuotaForceUntil.IsZero() ||
				after.Quota.Reason != "quota" || !after.Quota.NextRecoverAt.Equal(wantIndependent.Quota.NextRecoverAt) {
				t.Fatalf("auth availability did not reflect only the remaining model quota: %+v", after)
			}
			for _, model := range []string{"wrapped", "independent"} {
				if state := after.ModelStates[model]; !reflect.DeepEqual(state, wantIndependent) {
					t.Errorf("%s = %+v, want independent quota %+v", model, state, wantIndependent)
				}
			}
			for _, model := range []string{"reporting", "legacy-wrap"} {
				if state := after.ModelStates[model]; !modelStateIsClean(state) || state.credentialQuotaPropagated {
					t.Errorf("%s kept its credential quota cooldown: %+v", model, state)
				}
			}
		})
	}
}

func TestLookupRecoveryPreservesIndependentAuthCooldown(t *testing.T) {
	for _, source := range []string{"windows", "signals"} {
		for _, scenario := range []string{"ordinary-quota", "quota-force", "transient-429", "longer-force", "shorter-force"} {
			t.Run(source+"/"+scenario, func(t *testing.T) {
				withQuotaCooldownEnabled(t)
				manager := NewManager(nil, nil, nil)
				auth, err := manager.Register(t.Context(), &Auth{ID: "lookup-auth-only", Provider: "claude", Status: StatusActive})
				if err != nil {
					t.Fatal(err)
				}
				quotaRetry := time.Hour
				quotaError := &Error{HTTPStatus: http.StatusTooManyRequests}
				credentialScope := scenario != "ordinary-quota"
				if credentialScope {
					quotaError.Code = ErrorCodeForceCooldown
				}
				manager.MarkResult(t.Context(), Result{AuthID: auth.ID, Provider: auth.Provider,
					CredentialScope: credentialScope, RetryAfter: &quotaRetry, Error: quotaError})
				switch scenario {
				case "transient-429":
					retry := time.Minute
					manager.MarkResult(t.Context(), Result{AuthID: auth.ID, Provider: auth.Provider, CredentialScope: true,
						RetryAfter: &retry, Error: &Error{HTTPStatus: http.StatusTooManyRequests, Code: transientRateLimitErrorCode, Message: "capacity"}})
				case "longer-force", "shorter-force":
					retry := 2 * time.Hour
					if scenario == "shorter-force" {
						retry = time.Minute
					}
					manager.MarkResult(t.Context(), Result{AuthID: auth.ID, Provider: auth.Provider,
						RetryAfter: &retry, Error: &Error{HTTPStatus: http.StatusServiceUnavailable, Code: ErrorCodeForceCooldown, Message: "service unavailable"}})
				}
				before, _ := manager.GetByID(auth.ID)
				if len(before.ModelStates) != 0 || !before.Quota.Exceeded || !before.Unavailable ||
					(credentialScope && (before.credQuotaForceUntil.IsZero() || before.quotaForceCooldownUntil.IsZero())) {
					t.Fatalf("expected auth-only quota cooldown: %+v", before)
				}
				after := observeAvailableQuota(t, manager, before, source)
				if after.Quota.Exceeded || !after.credQuotaForceUntil.IsZero() || !after.quotaForceCooldownUntil.IsZero() {
					t.Fatalf("auth quota layer remains: %+v", after)
				}
				wantRetry, wantForce := before.nonQuotaCooldownUntil, time.Time{}
				if scenario == "longer-force" || scenario == "shorter-force" {
					wantForce = wantRetry
				}
				if !after.NextRetryAfter.Equal(wantRetry) || !after.forceCooldownUntil.Equal(wantForce) ||
					!after.credentialRetryAfter.Equal(before.credentialRetryAfter) || !after.nonQuotaCooldownUntil.Equal(wantRetry) {
					t.Fatalf("retry or force deadline changed incorrectly: retry=%v, force=%v, want retry=%v, force=%v",
						after.NextRetryAfter, after.forceCooldownUntil, wantRetry, wantForce)
				}
				if wantRetry.IsZero() {
					if after.Unavailable || after.Status != StatusActive || after.LastError != nil || after.StatusMessage != "" {
						t.Fatalf("released auth remained unavailable: %+v", after)
					}
				} else if !after.Unavailable || after.Status != before.Status || after.StatusMessage != before.StatusMessage ||
					!cooldownErrorEqual(after.LastError, before.LastError) {
					t.Fatalf("non-quota failure changed: before=%+v after=%+v", before, after)
				}
			})
		}
	}
}

func TestObserveQuotaLookupLiftsQuotaCooldown(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			store := &recordingCooldownStateStore{}
			manager.SetCooldownStateStore(store)
			id, model := "lookup-recovery-"+provider, "lookup-model-"+provider
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { reg.UnregisterClient(id) })
			independent := &ModelState{Status: StatusError, Unavailable: true, NextRetryAfter: time.Now().Add(time.Hour), LastError: &Error{HTTPStatus: 503}, UpdatedAt: time.Now().Add(-time.Minute)}
			if _, err := manager.Register(context.Background(), &Auth{ID: id, Provider: provider, Status: StatusActive, ModelStates: map[string]*ModelState{"other-model": independent}}); err != nil {
				t.Fatal(err)
			}
			retry := 56 * time.Hour
			manager.MarkResult(context.Background(), Result{AuthID: id, Provider: provider, Model: model, CredentialScope: true, RetryAfter: &retry, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached"}}`}})
			before, _ := manager.GetByID(id)
			if !before.Quota.Exceeded || len(store.savedRecords()) == 0 {
				t.Fatal("expected persisted quota cooldown")
			}
			lookupAt := before.UpdatedAt.Add(time.Second)
			if !manager.ObserveQuotaLookup(id, recoveryLookupHeaders(provider, false), lookupAt) {
				t.Fatal("lookup not recorded")
			}
			after, _ := manager.GetByID(id)
			if after.Quota.Exceeded || after.Unavailable || !after.NextRetryAfter.IsZero() || after.Status != StatusActive {
				t.Fatalf("cooldown remains: %+v", after)
			}
			state := after.ModelStates[model]
			if state.Quota.Exceeded || state.Unavailable || state.LastError != nil {
				t.Fatalf("model cooldown remains: %+v", state)
			}
			other := after.ModelStates["other-model"]
			if other.LastError == nil || other.LastError.HTTPStatus != 503 || !other.NextRetryAfter.Equal(independent.NextRetryAfter) || !other.Unavailable {
				t.Fatalf("independent model error changed: %+v", other)
			}
			for _, record := range store.savedRecords() {
				if record.Model != "other-model" {
					t.Fatalf("persisted quota cooldown remains: %+v", record)
				}
			}
			if reg.GetModelCount(model) != 1 {
				t.Fatal("registry routing not resumed")
			}
		})
	}
}

func TestObserveQuotaLookupLiftsQuotaAfterModelFailure(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			id := "lookup-after-model-failure-" + provider
			quotaModel, failedModel := "quota-model-"+provider, "failed-model-"+provider
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(id, provider, []*registry.ModelInfo{{ID: quotaModel}, {ID: failedModel}})
			t.Cleanup(func() { reg.UnregisterClient(id) })
			if _, err := manager.Register(context.Background(), &Auth{ID: id, Provider: provider, Status: StatusActive}); err != nil {
				t.Fatal(err)
			}
			retry := 56 * time.Hour
			manager.MarkResult(context.Background(), Result{AuthID: id, Provider: provider, Model: quotaModel, CredentialScope: true, RetryAfter: &retry, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached"}}`}})
			manager.MarkResult(context.Background(), Result{AuthID: id, Provider: provider, Model: failedModel, Error: &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "service unavailable"}})
			before, _ := manager.GetByID(id)
			if !before.Quota.Exceeded || before.LastError == nil || before.LastError.HTTPStatus != http.StatusServiceUnavailable {
				t.Fatalf("expected quota cooldown with aggregated model error: %+v", before)
			}
			independent := before.ModelStates[failedModel].credentialQuotaShadow
			if independent == nil || !independent.Unavailable || !independent.NextRetryAfter.After(time.Now()) {
				t.Fatalf("expected independent model cooldown: %+v", independent)
			}
			if !manager.ObserveQuotaLookup(id, recoveryLookupHeaders(provider, false), before.UpdatedAt.Add(time.Second)) {
				t.Fatal("lookup not recorded")
			}
			after, _ := manager.GetByID(id)
			if after.Quota.Exceeded || after.Unavailable || !after.NextRetryAfter.IsZero() {
				t.Fatalf("quota cooldown remains: %+v", after)
			}
			if state := after.ModelStates[quotaModel]; state.Quota.Exceeded || state.Unavailable || state.LastError != nil {
				t.Fatalf("quota model cooldown remains: %+v", state)
			}
			state := after.ModelStates[failedModel]
			if state.Quota.Exceeded || !state.Unavailable || state.Status != StatusError || state.LastError == nil || state.LastError.HTTPStatus != http.StatusServiceUnavailable ||
				state.LastError.Message != independent.LastError.Message || !state.NextRetryAfter.Equal(independent.NextRetryAfter) || !state.UpdatedAt.Equal(independent.UpdatedAt) {
				t.Fatalf("independent model error changed: %+v, want %+v", state, independent)
			}
		})
	}
}

func TestObserveQuotaLookupKeepsCooldownWithoutRecovery(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, scenario := range []string{"older", "same-time", "exhausted", "disabled", "unauthorized", "unauthorized-code"} {
			t.Run(provider+"/"+scenario, func(t *testing.T) {
				manager := NewManager(nil, nil, nil)
				now := time.Now()
				quota := QuotaState{Exceeded: true, Reason: credentialQuotaReason, NextRecoverAt: now.Add(56 * time.Hour)}
				auth := &Auth{ID: "lookup-keeps", Provider: provider, Status: StatusError, Unavailable: true, NextRetryAfter: quota.NextRecoverAt, Quota: quota, UpdatedAt: now,
					ModelStates: map[string]*ModelState{"model": {Status: StatusError, Unavailable: true, NextRetryAfter: quota.NextRecoverAt, Quota: quota, UpdatedAt: now}}}
				lookupAt := now.Add(time.Second)
				switch scenario {
				case "older":
					lookupAt = now.Add(-time.Second)
				case "same-time":
					lookupAt = now
				case "disabled":
					auth.Disabled = true
				case "unauthorized":
					auth.LastError = &Error{HTTPStatus: 401}
				case "unauthorized-code":
					auth.LastError = &Error{Code: "unauthorized"}
				}
				// Install an exact timestamp so ordering assertions do not depend on a clock tick.
				manager.auths[auth.ID] = auth
				if !manager.ObserveQuotaLookup(auth.ID, recoveryLookupHeaders(provider, scenario == "exhausted"), lookupAt) {
					t.Fatal("lookup not recorded")
				}
				after, _ := manager.GetByID(auth.ID)
				if !after.Quota.Exceeded || !after.Unavailable || !after.NextRetryAfter.Equal(quota.NextRecoverAt) {
					t.Fatalf("cooldown cleared: %+v", after)
				}
			})
		}
	}
}

func recoveryLookupHeaders(provider string, exhausted bool) http.Header {
	if provider == "claude" {
		used := "0.02"
		if exhausted {
			used = "1"
		}
		return http.Header{"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.01"}, "Anthropic-Ratelimit-Unified-7d-Utilization": {used}}
	}
	used := "2"
	if exhausted {
		used = "100"
	}
	return http.Header{"X-Codex-Primary-Used-Percent": {"1"}, "X-Codex-Secondary-Used-Percent": {used}}
}
