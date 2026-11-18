package auth

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func quotaBindingFixture(t *testing.T) (*Manager, *Auth, string, string) {
	t.Helper()
	withQuotaCooldownEnabled(t)
	m := NewManager(nil, nil, nil)
	route := t.Name() + "-selection"
	foreign := t.Name() + "-foreign"
	a, err := m.Register(context.Background(), &Auth{ID: t.Name(), Provider: "codex", Status: StatusActive, Metadata: map[string]any{"access_token": "token-a", "account_id": "account-a"}})
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: route}})
	reg.RegisterClient(a.ID+"-catalog", "codex", []*registry.ModelInfo{{ID: foreign, Config: &registry.ModelConfig{OverrideHeader: map[string]string{"Authorization": "Bearer token-b"}}}})
	t.Cleanup(func() { reg.UnregisterClient(a.ID); reg.UnregisterClient(a.ID + "-catalog") })
	return m, a, route, foreign
}

func quotaBindingFailure(m *Manager, a *Auth, route, actual string, d time.Duration, scope string, receipt bool) {
	ctx := context.Background()
	if receipt {
		ctx = withQuotaRecoveryBinding(ctx, a, route, cliproxyexecutor.Request{Model: actual}, cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: "original-" + actual}})
	}
	model := route
	if scope == "auth" {
		model = ""
	}
	m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: model, RouteModel: model, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached"}}`}, RetryAfter: &d, CredentialScope: scope == "credential"})
}

func TestQuotaRecoveryBindingCauseDeadline(t *testing.T) {
	for _, scope := range []string{"auth", "model", "credential"} {
		for _, hintless := range []bool{false, true} {
			name := "explicit"
			if hintless {
				name = "hintless"
			}
			t.Run(scope+"/"+name, func(t *testing.T) {
				m, a, route, foreign := quotaBindingFixture(t)
				quotaBindingFailure(m, a, route, route, 2*time.Hour, scope, true)
				start := time.Now()
				ctx := withQuotaRecoveryBinding(context.Background(), a, route, cliproxyexecutor.Request{Model: foreign}, cliproxyexecutor.Options{})
				d := time.Minute
				retry := &d
				if hintless {
					retry = nil
				}
				model := route
				if scope == "auth" {
					model = ""
				}
				m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: model, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached"}}`}, RetryAfter: retry, CredentialScope: scope == "credential"})
				before, _ := m.GetByID(a.ID)
				bindings := authQuotaRecoveryBindings(before, time.Now())
				found := false
				if len(bindings) != 2 {
					t.Fatal("distinct old and new request tuples were not retained")
				}
				expected := start.Add(time.Minute)
				if hintless {
					expected = independentRateLimitDeadline(QuotaState{}, nil, start)
				}
				for _, b := range bindings {
					if b.Execution == foreign {
						found = true
						if b.Until.Before(expected) || b.Until.After(expected.Add(time.Second)) {
							t.Fatalf("cause borrowed composed deadline: got=%v expected about%v", b.Until, expected)
						}
					}
				}
				if !found {
					t.Fatal("actual manager receipt missing")
				}
				got, _, err := m.ResetQuotaIfUnchanged(context.Background(), before)
				if err != nil || got != nil {
					t.Fatal("live foreign cause was reset")
				}
				// Emulate only the foreign cause expiring while the older matching quota stays live.
				m.mu.Lock()
				current := m.auths[a.ID]
				expire := func(q *QuotaState) {
					for i := range q.recoveryBindings {
						if q.recoveryBindings[i].Execution == foreign {
							q.recoveryBindings[i].Until = time.Now().Add(-time.Minute)
						}
					}
				}
				expire(&current.Quota)
				for _, s := range current.ModelStates {
					if s != nil {
						expire(&s.Quota)
						if s.credentialQuotaShadow != nil {
							expire(&s.credentialQuotaShadow.Quota)
						}
					}
				}
				m.mu.Unlock()
				// A healthy later matching receipt must not inherit the expired foreign binding.
				quotaBindingFailure(m, a, route, route, time.Hour, scope, true)
				before, _ = m.GetByID(a.ID)
				got, _, err = m.ResetQuotaIfUnchanged(context.Background(), before)
				if err != nil || got == nil {
					t.Fatal("expired foreign cause contaminated later matching quota")
				}
				after, _ := m.GetByID(a.ID)
				if len(authQuotaRecoveryBindings(after, time.Now())) != 0 {
					t.Fatal("successful clear retained request binding")
				}
			})
		}
	}
}

func TestQuotaRecoveryBindingLifetime(t *testing.T) {
	for _, scope := range []string{"auth", "model", "shadow"} {
		for _, restored := range []bool{false, true} {
			name := "memory"
			if restored {
				name = "restore"
			}
			t.Run(scope+"/"+name, func(t *testing.T) {
				m, a, route, foreign := quotaBindingFixture(t)
				store := NewFileCooldownStateStore(t.TempDir())
				m.SetCooldownStateStore(store)
				actualScope := scope
				if scope == "shadow" {
					actualScope = "model"
				}
				quotaBindingFailure(m, a, route, foreign, time.Hour, actualScope, true)
				if scope == "shadow" {
					quotaBindingFailure(m, a, route+"-reporter", route+"-reporter", 2*time.Hour, "credential", true)
				}
				before, _ := m.GetByID(a.ID)
				want := authQuotaRecoveryBindings(before, time.Now())
				if len(want) == 0 {
					t.Fatal("missing receipt binding")
				}
				if scope == "shadow" {
					s := before.ModelStates[route]
					if s == nil || s.credentialQuotaShadow == nil || len(s.credentialQuotaShadow.Quota.recoveryBindings) != 1 {
						t.Fatal("independent quota binding was not shadowed")
					}
				}
				clone := before.Clone()
				for i := range clone.Quota.recoveryBindings {
					clone.Quota.recoveryBindings[i].Execution = "changed-auth"
				}
				for _, s := range clone.ModelStates {
					if s != nil {
						for i := range s.Quota.recoveryBindings {
							s.Quota.recoveryBindings[i].Execution = "changed"
						}
						if s.credentialQuotaShadow != nil {
							for i := range s.credentialQuotaShadow.Quota.recoveryBindings {
								s.credentialQuotaShadow.Quota.recoveryBindings[i].Execution = "changed"
							}
						}
					}
				}
				unchanged, _ := m.GetByID(a.ID)
				if !reflect.DeepEqual(authQuotaRecoveryBindings(unchanged, time.Now()), want) {
					t.Fatal("clone aliased private binding storage")
				}
				if restored {
					m = NewManager(nil, nil, nil)
					if _, err := m.Register(context.Background(), &Auth{ID: a.ID, Provider: a.Provider, Status: StatusActive, Metadata: a.Metadata}); err != nil {
						t.Fatal(err)
					}
					m.SetCooldownStateStore(store)
					if err := m.RestoreCooldownStates(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
				current, _ := m.GetByID(a.ID)
				if !reflect.DeepEqual(authQuotaRecoveryBindings(current, time.Now()), want) {
					t.Fatal("restore lost quota-owned binding")
				}
				raw, err := json.Marshal(current)
				if err != nil {
					t.Fatal(err)
				}
				public := &Auth{}
				if err = json.Unmarshal(raw, public); err != nil {
					t.Fatal(err)
				}
				if len(authQuotaRecoveryBindings(public, time.Now())) != 0 {
					t.Fatal("private binding leaked into public Auth JSON")
				}
				merged := MergeRefreshedAuth(current, current, public)
				if !reflect.DeepEqual(authQuotaRecoveryBindings(merged, time.Now()), want) {
					t.Fatal("refresh/import merge erased live private binding")
				}
				if _, err = m.Update(context.Background(), public); err != nil {
					t.Fatal(err)
				}
				current, _ = m.GetByID(a.ID)
				if !reflect.DeepEqual(authQuotaRecoveryBindings(current, time.Now()), want) {
					t.Fatal("public JSON omission erased live binding")
				}
				// A real credential replacement must not inherit request identity from old tokens.
				raw, err = json.Marshal(current)
				if err != nil {
					t.Fatal(err)
				}
				replacement := &Auth{}
				if err = json.Unmarshal(raw, replacement); err != nil {
					t.Fatal(err)
				}
				replacement.Metadata["access_token"] = "replacement-token"
				replacement.Metadata["account_id"] = "replacement-account"
				if _, err = m.Update(context.Background(), replacement); err != nil {
					t.Fatal(err)
				}
				after, _ := m.GetByID(a.ID)
				if len(authQuotaRecoveryBindings(after, time.Now())) != 0 {
					t.Fatal("replacement inherited old request binding")
				}
			})
		}
	}
}

type quotaBindingSaveStore struct {
	Store
	change func(*Auth)
}

func (s *quotaBindingSaveStore) Save(_ context.Context, a *Auth) (string, error) {
	s.change(a)
	return "", nil
}

func TestQuotaRecoveryBindingSaveRace(t *testing.T) {
	t.Run("staged", func(t *testing.T) {
		m, a, route, _ := quotaBindingFixture(t)
		quotaBindingFailure(m, a, route, route, time.Hour, "model", true)
		before, _ := m.GetByID(a.ID)
		// Store.Save may replace exported opaque Quota/ModelStates, but must not
		// synchronously mutate the manager. Build those values using a real manager.
		donor := NewManager(nil, nil, nil)
		donorAuth, err := donor.Register(context.Background(), before.Clone())
		if err != nil {
			t.Fatal(err)
		}
		ctx := withQuotaRecoveryBinding(context.Background(), donorAuth, route, cliproxyexecutor.Request{Model: route}, cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: "new-during-save"}})
		d := 2 * time.Hour
		donor.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: route, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached"}}`}, RetryAfter: &d})
		imported, _ := donor.GetByID(a.ID)
		if len(authQuotaRecoveryBindings(imported, time.Now())) < 2 {
			t.Fatal("donor manager did not record a distinct request binding")
		}
		changed := false
		m.SetStore(&quotaBindingSaveStore{change: func(staged *Auth) {
			changed = true
			staged.Quota = imported.Quota.Clone()
			staged.ModelStates = imported.Clone().ModelStates
		}})
		got, _, err := m.ResetQuotaIfUnchanged(context.Background(), before)
		if err != nil || got != nil || !changed {
			t.Fatal("supported save enrichment published a stale quota reset")
		}
		after, _ := m.GetByID(a.ID)
		if !after.ModelStates[route].Quota.Exceeded {
			t.Fatal("failed guarded publication cleared live quota")
		}
	})
}

func TestQuotaRecoveryBindingSnapshot(t *testing.T) {
	m, a, route, _ := quotaBindingFixture(t)
	quotaBindingFailure(m, a, route, route, time.Hour, "model", true)
	expected, _ := m.GetByID(a.ID)
	current := expected.Clone()
	// A binding's layer association is part of its inspected opaque snapshot.
	current.Quota.recoveryBindings = append([]quotaRecoveryBinding(nil), current.ModelStates[route].Quota.recoveryBindings...)
	current.ModelStates[route].Quota.recoveryBindings = nil
	if authQuotaRecoveryBindingsEqual(current, expected, time.Now()) {
		t.Fatal("snapshot comparison discarded auth/model layer association")
	}
}

func TestQuotaRecoveryBindingLegacyMarkResult(t *testing.T) {
	m, a, route, foreign := quotaBindingFixture(t)
	quotaBindingFailure(m, a, route, foreign, time.Hour, "model", false)
	before, _ := m.GetByID(a.ID)
	if len(authQuotaRecoveryBindings(before, time.Now())) != 0 {
		t.Fatal("direct MarkResult fabricated dispatch evidence")
	}
	got, _, err := m.ResetQuotaIfUnchanged(context.Background(), before)
	if err != nil || got == nil {
		t.Fatal("legacy direct caller lost its existing supported-route behavior")
	}
}

func TestQuotaRecoveryBindingDiagnosticOverwrite(t *testing.T) {
	for _, scope := range []string{"auth", "model"} {
		t.Run(scope, func(t *testing.T) {
			m, a, route, foreign := quotaBindingFixture(t)
			quotaBindingFailure(m, a, route, foreign, time.Hour, scope, true)
			model := route
			if scope == "auth" {
				model = ""
			}
			retry := time.Second
			m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: a.Provider, Model: model, Error: &Error{HTTPStatus: 404, Message: "later route diagnostic"}, RetryAfter: &retry})
			diagnostic, _ := m.GetByID(a.ID)
			if len(authQuotaRecoveryBindings(diagnostic, time.Now())) != 1 {
				t.Fatal("diagnostic overwrite erased earlier live quota binding")
			}
			m.mu.Lock()
			live := m.auths[a.ID]
			live.nonQuotaCooldownUntil = time.Now().Add(-time.Minute)
			for _, s := range live.ModelStates {
				if s != nil {
					s.nonQuotaCooldownUntil = time.Now().Add(-time.Minute)
				}
			}
			m.mu.Unlock()
			quotaBindingFailure(m, a, route, route, time.Minute, scope, true)
			before, _ := m.GetByID(a.ID)
			if !QuotaRecoveryCandidate(before) {
				t.Fatal("later matching quota is not an otherwise recoverable candidate")
			}
			got, _, err := m.ResetQuotaIfUnchanged(context.Background(), before)
			if err != nil || got != nil {
				t.Fatal("later matching quota forgot the older foreign execution identity")
			}
		})
	}
}

func TestQuotaRecoveryBindingCorrelation(t *testing.T) {
	m, a, route, _ := quotaBindingFixture(t)
	for _, pair := range []struct{ actual, original string }{{route + "-first", "Original(first)"}, {route + "-second", "Original(second)"}, {route + "-first", "Original(third)"}, {route + "-third", "Original(first)"}} {
		ctx := withQuotaRecoveryBinding(context.Background(), a, route, cliproxyexecutor.Request{Model: pair.actual}, cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.RequestedModelMetadataKey: pair.original}})
		d := time.Hour
		m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: route, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached"}}`}, RetryAfter: &d})
	}
	before, _ := m.GetByID(a.ID)
	bindings := authQuotaRecoveryBindings(before, time.Now())
	if len(bindings) != 4 {
		t.Fatal("request associations collapsed")
	}
	for _, b := range bindings {
		if b.Selection != route || b.Execution == route+"-first" && b.Requested != "Original(first)" && b.Requested != "Original(third)" || b.Execution == route+"-second" && b.Requested != "Original(second)" {
			t.Fatal("request names formed an unrelated cross-product")
		}
	}
}

func TestQuotaRecoveryBindingSaveEnrichment(t *testing.T) {
	m, a, route, foreign := quotaBindingFixture(t)
	quotaBindingFailure(m, a, route, foreign, time.Hour, "model", true)
	before, _ := m.GetByID(a.ID)
	want := authQuotaRecoveryBindings(before, time.Now())
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
		saved.Quota = public.Quota
		saved.ModelStates = public.ModelStates
	}})
	raw, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	public := &Auth{}
	if err = json.Unmarshal(raw, public); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Update(context.Background(), public); err != nil {
		t.Fatal(err)
	}
	after, _ := m.GetByID(a.ID)
	if calls == 0 || !reflect.DeepEqual(authQuotaRecoveryBindings(after, time.Now()), want) {
		t.Fatal("supported exported save enrichment erased live private binding")
	}
	got, _, err := m.ResetQuotaIfUnchanged(context.Background(), after)
	if err != nil || got != nil {
		t.Fatal("save-enriched snapshot allowed foreign cooldown reset")
	}
}

func TestQuotaRecoveryBindingIntentionalClear(t *testing.T) {
	for _, mode := range []string{"quota_reset", "replacement", "disabled", "cooling_disabled", "cooling_disabled_force"} {
		t.Run(mode, func(t *testing.T) {
			m, a, route, _ := quotaBindingFixture(t)
			quotaBindingFailure(m, a, route, route, time.Hour, "model", true)
			// Add a real independent binding beneath a credential-wide layer so a
			// public JSON save cannot reintroduce its private shadow after a clear.
			if mode != "quota_reset" {
				quotaBindingFailure(m, a, route+"-reporter", route+"-reporter", 2*time.Hour, "credential", true)
			}
			if mode == "cooling_disabled_force" {
				ctx := withQuotaRecoveryBinding(context.Background(), a, route, cliproxyexecutor.Request{Model: route}, cliproxyexecutor.Options{})
				d := time.Hour
				m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: route, Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached"}}`}, RetryAfter: &d, CredentialScope: true})
			}

			before, _ := m.GetByID(a.ID)
			if len(authQuotaRecoveryBindings(before, time.Now())) == 0 {
				t.Fatal("fixture lacks live manager binding")
			}
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
				saved.Quota = public.Quota
				saved.ModelStates = public.ModelStates
			}})
			if mode == "quota_reset" {

				if got, _, err := m.ResetQuotaIfUnchanged(context.Background(), before); err != nil || got == nil {
					t.Fatal("ordinary matching reset did not publish")
				}
			} else {
				raw, err := json.Marshal(before)
				if err != nil {
					t.Fatal(err)
				}
				public := &Auth{}
				if err = json.Unmarshal(raw, public); err != nil {
					t.Fatal(err)
				}
				if mode == "replacement" {
					public.Metadata["access_token"] = "replacement-token"
					public.Metadata["account_id"] = "replacement-account"
				} else if mode == "disabled" {
					public.Disabled = true
					public.Status = StatusDisabled
				} else {
					SetQuotaCooldownDisabled(true)
				}
				if _, err = m.Update(context.Background(), public); err != nil {
					t.Fatal(err)
				}
			}
			after, _ := m.GetByID(a.ID)
			if calls == 0 {
				t.Fatal("public save path was not consumed")
			}
			if mode == "cooling_disabled_force" {
				if QuotaRecoveryCandidate(after) {
					t.Fatal("force control was released")
				}
				if after.Quota.Exceeded && len(authQuotaRecoveryBindings(after, time.Now())) == 0 {
					t.Fatal("retained force quota lost its request binding")
				}
				return
			}

			if len(authQuotaRecoveryBindings(after, time.Now())) != 0 {
				t.Fatal("supported JSON save reintroduced deliberately cleared request binding")
			}
		})
	}
}

func TestQuotaRecoveryBindingForceOwnerWindow(t *testing.T) {
	for _, scope := range []string{"model", "auth", "credential"} {
		for _, phase := range []string{"owner_expired", "extend_before_owner_expiry"} {
			name := phase
			if scope != "model" {
				name = scope + "/" + phase
			}
			t.Run(name, func(t *testing.T) {
				m, a, route, foreign := quotaBindingFixture(t)
				quotaBindingFailure(m, a, route, foreign, 2*time.Hour, scope, true)
				first, _ := m.GetByID(a.ID)
				foreignUntil := authQuotaRecoveryBindings(first, time.Now())[0].Until
				ctx := withQuotaRecoveryBinding(context.Background(), a, route, cliproxyexecutor.Request{Model: route}, cliproxyexecutor.Options{})
				model := route
				if scope == "auth" {
					model = ""
				}
				force := time.Minute
				m.MarkResult(ctx, Result{AuthID: a.ID, Provider: a.Provider, Model: model, Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached"}}`}, RetryAfter: &force, CredentialScope: scope == "credential"})
				quotaBindingFailure(m, a, route, route, 30*time.Second, scope, true)
				SetQuotaCooldownDisabled(true)
				snapshot, _ := m.GetByID(a.ID)
				raw, err := json.Marshal(snapshot)
				if err != nil {
					t.Fatal(err)
				}
				public := &Auth{}
				if err = json.Unmarshal(raw, public); err != nil {
					t.Fatal(err)
				}
				if _, err = m.Update(context.Background(), public); err != nil {
					t.Fatal(err)
				}
				clamped, _ := m.GetByID(a.ID)
				retained := clamped.Quota.NextRecoverAt
				forced := effectiveAuthForceUntil(clamped)
				if scope != "auth" {
					retained = clamped.ModelStates[route].Quota.NextRecoverAt
					forced = effectiveModelForceUntil(clamped.ModelStates[route])
				}
				if scope == "credential" {
					forced = effectiveCredQuotaForceUntil(clamped)
				}
				if !retained.After(time.Now()) || !retained.Before(foreignUntil) || !retained.Equal(forced) {
					t.Fatal("public disable-cooling update did not retain only its real force window")
				}
				if QuotaRecoveryCandidate(clamped) {
					t.Fatal("live force owner control was released")
				}
				if phase == "extend_before_owner_expiry" {
					SetQuotaCooldownDisabled(false)
					if !retained.After(time.Now()) {
						t.Fatal("fixture missed the before-expiry extension")
					}
					quotaBindingFailure(m, a, route, route, time.Hour, scope, true)
					extended, _ := m.GetByID(a.ID)
					next := extended.Quota.NextRecoverAt
					if scope != "auth" {
						next = extended.ModelStates[route].Quota.NextRecoverAt
					}
					if !next.After(retained) {
						t.Fatal("new matching ordinary cause did not extend the owning quota")
					}
				}
				// Move only the elapsed force window and causes bounded by it into the
				// past. A genuinely new matching-hour cause stays live. No wall-clock sleep.
				m.mu.Lock()
				live := m.auths[a.ID]
				past := time.Now().Add(-time.Minute)
				expire := func(q *QuotaState) {
					for i := range q.recoveryBindings {
						if !q.recoveryBindings[i].Until.After(retained) {
							q.recoveryBindings[i].Until = past
						}
					}
					if phase == "owner_expired" {
						q.NextRecoverAt = past
					}
				}
				expire(&live.Quota)
				for _, d := range []*time.Time{&live.forceCooldownUntil, &live.quotaForceCooldownUntil, &live.credQuotaForceUntil, &live.nonQuotaCooldownUntil} {
					if !d.IsZero() {
						*d = past
					}
				}
				if phase == "owner_expired" {
					live.NextRetryAfter = past
				}
				for _, state := range live.ModelStates {
					if state == nil {
						continue
					}
					expire(&state.Quota)
					for _, d := range []*time.Time{&state.forceCooldownUntil, &state.quotaForceCooldownUntil, &state.nonQuotaCooldownUntil} {
						if !d.IsZero() {
							*d = past
						}
					}
					if phase == "owner_expired" {
						state.NextRetryAfter = past
					}
				}
				m.mu.Unlock()
				before, _ := m.GetByID(a.ID)
				if !QuotaRecoveryCandidate(before) {
					t.Fatal("elapsed force leaves an otherwise recoverable ordinary quota")
				}
				got, _, err := m.ResetQuotaIfUnchanged(context.Background(), before)
				if err != nil || got == nil {
					t.Fatal("an intentionally truncated old foreign binding survived or revived beyond its retained owner window")
				}
			})
		}
	}
}

func TestQuotaRecoveryBindingCredentialContinuity(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		known, rotate, retain bool
	}{
		{"known_rotation", true, true, true},
		{"known_refresh", true, false, true},
		{"unknown_refresh", false, false, true},
		{"unknown_rotation", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, a, route, foreign := quotaBindingFixture(t)
			if !tc.known {
				delete(a.Metadata, "account_id")
				var err error
				a, err = m.Update(context.Background(), a)
				if err != nil {
					t.Fatal(err)
				}
			}
			quotaBindingFailure(m, a, route, foreign, time.Hour, "model", true)
			before, _ := m.GetByID(a.ID)
			want := authQuotaRecoveryBindings(before, time.Now())
			if len(want) != 1 {
				t.Fatal("fixture lacks one real quota binding")
			}
			raw, err := json.Marshal(before)
			if err != nil {
				t.Fatal(err)
			}
			public := &Auth{}
			if err = json.Unmarshal(raw, public); err != nil {
				t.Fatal(err)
			}
			public.Metadata["refresh_token"] = "new-refresh-token"
			if tc.rotate {
				public.Metadata["access_token"] = "new-access-token"
			}
			if _, err = m.Update(context.Background(), public); err != nil {
				t.Fatal(err)
			}
			after, _ := m.GetByID(a.ID)
			if tc.retain {
				if !reflect.DeepEqual(authQuotaRecoveryBindings(after, time.Now()), want) {
					t.Fatal("supported same-identity grant update erased retained causal binding")
				}
				if got, _, err := m.ResetQuotaIfUnchanged(context.Background(), after); err != nil || got != nil {
					t.Fatal("fresh post-update snapshot released foreign request quota")
				}
			} else if len(authQuotaRecoveryBindings(after, time.Now())) != 0 {
				t.Fatal("unknown-account token replacement inherited old binding")
			}
		})
	}
}
