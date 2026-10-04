package auth

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func quotaGuardFixture(t *testing.T) (*Manager, *Auth, CooldownStateStore, string) {
	t.Helper()
	withQuotaCooldownEnabled(t)
	m := NewManager(nil, nil, nil)
	store := NewFileCooldownStateStore(t.TempDir())
	m.SetCooldownStateStore(store)
	id, model := t.Name(), t.Name()+"-model"
	_, err := m.Register(context.Background(), &Auth{ID: id, Provider: "codex", Status: StatusActive})
	if err != nil {
		t.Fatal(err)
	}
	r := registry.GetGlobalRegistry()
	r.RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { r.UnregisterClient(id) })
	duration := time.Hour
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "codex", Model: model, Error: &Error{HTTPStatus: 429, Message: "quota exceeded"}, RetryAfter: &duration, CredentialScope: true})
	a, _ := m.GetByID(id)
	return m, a, store, model
}

func TestQuotaRecoveryCandidate(t *testing.T) {
	_, base, _, model := quotaGuardFixture(t)
	cases := []struct {
		name   string
		change func(*Auth)
		want   bool
	}{
		{"quota", func(*Auth) {}, true},
		{"inherited nil", func(a *Auth) {
			a.LastError = nil
			a.StatusMessage = ""
			a.ModelStates[model].LastError = nil
			a.ModelStates[model].StatusMessage = ""
		}, true},
		{"disabled", func(a *Auth) { a.Disabled = true }, false},
		{"disabled model", func(a *Auth) { a.ModelStates[model].Status = StatusDisabled }, false},
		{"401", func(a *Auth) { a.LastError = &Error{HTTPStatus: 401} }, false},
		{"unknown error", func(a *Auth) { a.ModelStates[model].LastError = &Error{Message: "unknown"} }, false},
		{"forced", func(a *Auth) { a.LastError.Code = ErrorCodeForceCooldown }, false},
		{"mixed deadline", func(a *Auth) { a.ModelStates[model].NextRetryAfter = a.Quota.NextRecoverAt.Add(time.Minute) }, false},
		{"empty quota reason", func(a *Auth) { a.Quota.Reason = "" }, false},
		{"quota reason without exceeded", func(a *Auth) { a.Quota.Exceeded = false }, false},
		{"unknown reason", func(a *Auth) { a.Quota.Reason = "cloudflare challenge" }, false},
		{"unknown nil diagnostic", func(a *Auth) { a.LastError = nil; a.StatusMessage = "payment_required" }, false},
		{"pending", func(a *Auth) { a.Status = StatusPending }, false},
		{"no quota", func(a *Auth) { *a = Auth{ID: a.ID, Provider: a.Provider, Status: StatusActive} }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := base.Clone()
			tc.change(a)
			if got := QuotaRecoveryCandidate(a); got != tc.want {
				t.Fatalf("candidate=%v want %v", got, tc.want)
			}
		})
	}
	if QuotaRecoveryCandidate(nil) {
		t.Fatal("nil candidate")
	}
}

func TestResetQuotaIfUnchangedRecoversRoutingAndPersistence(t *testing.T) {
	m, a, store, model := quotaGuardFixture(t)
	records, err := store.Load(context.Background())
	if err != nil || len(records) == 0 {
		t.Fatalf("missing saved cooldown: %v %v", records, err)
	}
	if _, err = m.scheduler.pickSingle(context.Background(), a.Provider, model, cliproxyexecutor.Options{}, nil); err == nil {
		t.Fatal("cooling credential routed")
	}
	updated, models, err := m.ResetQuotaIfUnchanged(context.Background(), a)
	if err != nil || updated == nil || len(models) != 1 || models[0] != model {
		t.Fatalf("reset=%v models=%v err=%v", updated, models, err)
	}
	if updated.Unavailable || updated.Quota.Exceeded || !updated.NextRetryAfter.IsZero() || updated.LastError != nil || updated.Generation <= a.Generation {
		t.Fatalf("uncleared auth: %+v", updated)
	}
	state := updated.ModelStates[model]
	if state.Unavailable || state.Quota.Exceeded || !state.NextRetryAfter.IsZero() || state.LastError != nil {
		t.Fatalf("uncleared model: %+v", state)
	}
	records, err = store.Load(context.Background())
	if err != nil || len(records) != 0 {
		t.Fatalf("persisted cooldown remains: %v %v", records, err)
	}
	if registry.GetGlobalRegistry().GetModelCount(model) != 1 {
		t.Fatal("registry did not resume")
	}
	picked, err := m.scheduler.pickSingle(context.Background(), a.Provider, model, cliproxyexecutor.Options{}, nil)
	if err != nil || picked == nil || picked.ID != a.ID {
		t.Fatalf("scheduler did not resume: %v %v", picked, err)
	}
}

func TestResetQuotaIfUnchangedRejectsChangedOrRestrictedAuth(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Manager, *Auth, string)
	}{
		{"stale generation", func(m *Manager, a *Auth, _ string) { a.Generation-- }},
		{"stale epoch", func(m *Manager, a *Auth, _ string) { a.RegistrationEpoch-- }},
		{"provider mismatch", func(m *Manager, a *Auth, _ string) { a.Provider = "claude" }},
		{"reregister", func(m *Manager, a *Auth, _ string) {
			m.Remove(context.Background(), a.ID)
			current, err := m.Register(context.Background(), a.Clone())
			if err != nil {
				t.Fatal(err)
			}
			// Isolate the registration epoch from generation rejection.
			a.Generation = current.Generation
		}},
		{"newer 401", func(m *Manager, a *Auth, model string) {
			m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: a.Provider, Model: model, Error: &Error{HTTPStatus: 401}})
		}},
		{"newer 429", func(m *Manager, a *Auth, model string) {
			m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: a.Provider, Model: model, Error: &Error{HTTPStatus: 429}})
		}},
		{"disabled current", func(m *Manager, a *Auth, _ string) { m.mu.Lock(); m.auths[a.ID].Disabled = true; m.mu.Unlock() }},
		{"forced current", func(m *Manager, a *Auth, _ string) {
			m.mu.Lock()
			m.auths[a.ID].LastError.Code = ErrorCodeForceCooldown
			m.mu.Unlock()
		}},
		{"mixed current", func(m *Manager, a *Auth, model string) {
			m.mu.Lock()
			m.auths[a.ID].ModelStates[model].NextRetryAfter = a.Quota.NextRecoverAt.Add(time.Hour)
			m.mu.Unlock()
		}},
		{"home", func(m *Manager, a *Auth, _ string) {
			cfg := &internalconfig.Config{}
			cfg.Home.Enabled = true
			m.runtimeConfig.Store(cfg)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, a, store, model := quotaGuardFixture(t)
			tc.change(m, a, model)
			before, _ := m.GetByID(a.ID)
			savedBefore, err := store.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			got, models, err := m.ResetQuotaIfUnchanged(context.Background(), a)
			if err != nil || got != nil || models != nil {
				t.Fatalf("guard accepted: %v %v %v", got, models, err)
			}
			after, _ := m.GetByID(a.ID)
			savedAfter, err := store.Load(context.Background())
			if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(savedBefore, savedAfter) {
				t.Fatal("rejected recovery mutated state")
			}
		})
	}
}

type quotaGuardWaitingContext struct {
	context.Context
	waiting chan struct{}
}

func (c *quotaGuardWaitingContext) Done() <-chan struct{} {
	select {
	case c.waiting <- struct{}{}:
	default:
	}
	return c.Context.Done()
}

func TestResetQuotaIfUnchangedCancellation(t *testing.T) {
	for _, wait := range []bool{false, true} {
		t.Run(map[bool]string{false: "already cancelled", true: "waiting gate"}[wait], func(t *testing.T) {
			m, a, store, _ := quotaGuardFixture(t)
			savedBefore, _ := store.Load(context.Background())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var got *Auth
			var err error
			if wait {
				release := m.lockAuthMutation(a.ID)
				defer release()
				waitingCtx := &quotaGuardWaitingContext{Context: ctx, waiting: make(chan struct{}, 1)}
				finished := make(chan struct{})
				go func() { got, _, err = m.ResetQuotaIfUnchanged(waitingCtx, a); close(finished) }()
				select {
				case <-waitingCtx.waiting:
				case <-time.After(5 * time.Second):
					t.Fatal("reset did not wait")
				}
				cancel()
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Fatal("reset did not cancel its lock wait")
				}
			} else {
				cancel()
				got, _, err = m.ResetQuotaIfUnchanged(ctx, a)
			}
			if got != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel result: %v %v", got, err)
			}
			after, _ := m.GetByID(a.ID)
			savedAfter, _ := store.Load(context.Background())
			if !reflect.DeepEqual(a, after) || !reflect.DeepEqual(savedBefore, savedAfter) {
				t.Fatal("cancel mutated cooldown")
			}
		})
	}
}

type quotaGuardCancellingStore struct {
	Store
	cancel  context.CancelFunc
	inspect func()
}

func (s *quotaGuardCancellingStore) Save(context.Context, *Auth) (string, error) {
	s.inspect()
	s.cancel()
	return "", nil
}

func TestResetQuotaIfUnchangedCancellationDuringPersistence(t *testing.T) {
	m, a, store, model := quotaGuardFixture(t)
	m.mu.Lock()
	m.auths[a.ID].Metadata = map[string]any{"type": "codex"}
	m.mu.Unlock()
	a, _ = m.GetByID(a.ID)
	savedBefore, _ := store.Load(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inspected := false
	m.SetStore(&quotaGuardCancellingStore{cancel: cancel, inspect: func() {
		inspected = true
		current, _ := m.GetByID(a.ID)
		if !reflect.DeepEqual(a, current) {
			t.Error("recovery published before persistence completed")
		}
	}})
	got, _, err := m.ResetQuotaIfUnchanged(ctx, a)
	if !inspected || got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel during persistence=%v %v inspected=%v", got, err, inspected)
	}
	after, _ := m.GetByID(a.ID)
	savedAfter, _ := store.Load(context.Background())
	if !reflect.DeepEqual(a, after) || !reflect.DeepEqual(savedBefore, savedAfter) {
		t.Fatal("cancel during persistence mutated cooldown")
	}
	if _, err = m.scheduler.pickSingle(context.Background(), a.Provider, model, cliproxyexecutor.Options{}, nil); err == nil {
		t.Fatal("cancel resumed scheduler")
	}
	if registry.GetGlobalRegistry().GetModelCount(model) != 0 {
		t.Fatal("cancel resumed registry")
	}
}
