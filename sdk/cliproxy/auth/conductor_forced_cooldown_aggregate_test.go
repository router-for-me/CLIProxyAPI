package auth

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestManager_ForcedCooldownRecoveryAggregates(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	SetQuotaCooldownDisabled(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousDisabled) })
	for _, recovery := range []string{"success", "refresh", "refresh_helper"} {
		for _, priorQuota := range []bool{false, true} {
			name := recovery + "/ordinary_401"
			if priorQuota {
				name = recovery + "/prior_ordinary_quota"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				m, auth := newCooldownMonotonicManager(t, "aggregate-model")
				store := NewFileCooldownStateStore(t.TempDir())
				m.SetCooldownStateStore(store)
				if priorQuota {
					ordinary := 2 * time.Hour
					m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: "aggregate-model", RetryAfter: &ordinary, Error: &Error{HTTPStatus: http.StatusTooManyRequests}})
				}
				forced := 10 * time.Minute
				m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: "aggregate-model", RetryAfter: &forced, Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests}})
				if !priorQuota || recovery != "success" {
					m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: "aggregate-model", Error: &Error{HTTPStatus: http.StatusUnauthorized, Message: "expired token"}})
				}
				before, _ := m.GetByID(auth.ID)
				deadline := before.ModelStates["aggregate-model"].ForcedCooldownUntil
				var snapshot *Auth
				switch recovery {
				case "refresh_helper":
					snapshot = before.Clone()
					resumed := clearUnauthorizedModelStates(snapshot, time.Now())
					if len(resumed) != 0 {
						t.Error("a still-forced model must not be reported as immediately resumed")
					}
				case "refresh":
					m.RegisterExecutor(&unauthorizedRefreshExecutor{id: auth.Provider})
					if _, errRefresh := m.refreshAuthForRequest(ctx, auth.ID, ""); errRefresh != nil {
						t.Fatal(errRefresh)
					}
					snapshot, _ = m.GetByID(auth.ID)
				default:
					m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: "aggregate-model", Success: true})
					snapshot, _ = m.GetByID(auth.ID)
				}
				assertRecoveredForcedAggregate(t, snapshot, "aggregate-model", deadline)
				if recovery == "refresh_helper" {
					return
				}
				if picked, errPick := m.scheduler.pickSingle(ctx, auth.Provider, "aggregate-model", cliproxyexecutor.Options{}, nil); picked != nil || errPick == nil {
					t.Error("scheduler reused the forced model before its deadline")
				}
				assertForcedAggregateSchedulerExpiry(t, m, auth, "aggregate-model", deadline)
				records, errLoad := store.Load(ctx)
				if errLoad != nil {
					t.Fatal(errLoad)
				}
				if len(records) != 2 {
					t.Fatalf("expected auth and model cooldown records, got %d", len(records))
				}
				for _, record := range records {
					if !record.NextRetryAfter.Equal(deadline) || record.Quota.Exceeded || !record.Quota.NextRecoverAt.IsZero() {
						t.Errorf("persisted stale aggregate: model=%q next=%v quota=%v", record.Model, record.NextRetryAfter, record.Quota.NextRecoverAt)
					}
				}
				restored, _ := newCooldownMonotonicManager(t, "aggregate-model")
				restored.SetCooldownStateStore(store)
				if errRestore := restored.RestoreCooldownStates(ctx); errRestore != nil {
					t.Fatal(errRestore)
				}
				afterRestore, _ := restored.GetByID(auth.ID)
				assertRecoveredForcedAggregate(t, afterRestore, "aggregate-model", deadline)
				assertForcedAggregateSchedulerExpiry(t, restored, auth, "aggregate-model", deadline)
			})
		}
	}
}

func assertForcedAggregateSchedulerExpiry(t *testing.T, m *Manager, auth *Auth, model string, deadline time.Time) {
	t.Helper()
	m.scheduler.mu.Lock()
	defer m.scheduler.mu.Unlock()
	provider := m.scheduler.providers[auth.Provider]
	if provider == nil {
		t.Fatal("scheduler has no provider state")
	}
	for _, selectedModel := range []string{model, ""} {
		shard := provider.ensureModelLocked(selectedModel, time.Now())
		entry := shard.entries[auth.ID]
		if entry == nil || entry.state == scheduledStateReady || !entry.nextRetryAt.Equal(deadline) {
			t.Errorf("scheduler model=%q did not retain exactly the forced deadline", selectedModel)
		}
		shard.promoteExpiredLocked(deadline.Add(time.Second))
		if entry == nil || entry.state != scheduledStateReady {
			t.Errorf("scheduler model=%q retained stale aggregate after forced expiry", selectedModel)
		}
	}
}

func assertRecoveredForcedAggregate(t *testing.T, auth *Auth, model string, deadline time.Time) {
	t.Helper()
	state := auth.ModelStates[model]
	if state == nil || !state.ForcedCooldownUntil.Equal(deadline) || !state.NextRetryAfter.Equal(deadline) {
		t.Fatalf("recovered model lost its forced deadline: %+v", state)
	}
	if !auth.ForcedCooldownUntil.IsZero() {
		t.Error("model recovery widened the forced cooldown to credential scope")
	}
	if !auth.NextRetryAfter.Equal(deadline) || auth.Quota.Exceeded || !auth.Quota.NextRecoverAt.IsZero() {
		t.Errorf("auth retained stale aggregate: next=%v quota=%v; want next=%v, no quota", auth.NextRetryAfter, auth.Quota.NextRecoverAt, deadline)
	}
	for _, selectedModel := range []string{model, ""} {
		if blocked, _, _ := isAuthBlockedForModel(auth, selectedModel, time.Now()); !blocked {
			t.Errorf("selection model=%q bypassed an active forced cooldown", selectedModel)
		}
		if blocked, _, next := isAuthBlockedForModel(auth, selectedModel, deadline.Add(time.Second)); blocked {
			t.Errorf("selection model=%q retained stale cooldown after forced expiry: %v", selectedModel, next)
		}
	}
}

func TestManager_ForcedCooldownRefreshKeepsConcurrentState(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	SetQuotaCooldownDisabled(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousDisabled) })
	for _, mutation := range []string{"sibling", "same_model", "credential_forced", "credential_quota", "reset"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			m, auth := newCooldownMonotonicManager(t, "refresh-model", "sibling-model")
			forced := 10 * time.Minute
			m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: "refresh-model", RetryAfter: &forced, Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests}})
			m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: "refresh-model", Error: &Error{HTTPStatus: http.StatusUnauthorized, Message: "expired token"}})
			before, _ := m.GetByID(auth.ID)
			deadline := before.ModelStates["refresh-model"].ForcedCooldownUntil
			executor := &aggregateBlockingRefreshExecutor{unauthorizedRefreshExecutor: unauthorizedRefreshExecutor{id: auth.Provider}, started: make(chan struct{}), release: make(chan struct{})}
			m.RegisterExecutor(executor)
			done := make(chan error, 1)
			go func() {
				_, errRefresh := m.refreshAuthForRequest(ctx, auth.ID, "")
				done <- errRefresh
			}()
			<-executor.started
			longer := 20 * time.Minute
			result := Result{AuthID: auth.ID, Provider: auth.Provider, Model: "sibling-model", RetryAfter: &longer, Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "new quota"}}
			switch mutation {
			case "same_model":
				result.Model = "refresh-model"
				result.Error = &Error{HTTPStatus: http.StatusNotFound, Message: "new model error"}
			case "credential_forced":
				result.Model = ""
				result.Error.Code = ErrorCodeForceCooldown
			case "credential_quota":
				result.CredentialScope = true
			case "reset":
				if _, _, errReset := m.ResetQuota(ctx, auth.ID); errReset != nil {
					t.Error(errReset)
				}
			}
			if mutation != "reset" {
				m.MarkResult(ctx, result)
			}
			concurrent, _ := m.GetByID(auth.ID)
			close(executor.release)
			if errRefresh := <-done; errRefresh != nil {
				t.Fatal(errRefresh)
			}
			after, _ := m.GetByID(auth.ID)
			if mutation == "reset" {
				if blocked, _, _ := isAuthBlockedForModel(after, "", time.Now()); blocked {
					t.Error("refresh resurrected a reset aggregate cooldown")
				}
				return
			}
			if mutation == "sibling" {
				if !after.NextRetryAfter.Equal(deadline) || !after.Quota.NextRecoverAt.Equal(concurrent.ModelStates["sibling-model"].Quota.NextRecoverAt) {
					t.Error("refresh did not rebuild aggregate from healed and concurrent model states")
				}
				if blocked, _, _ := isAuthBlockedForModel(after, "refresh-model", deadline.Add(time.Second)); blocked {
					t.Error("sibling quota widened the recovered model cooldown")
				}
			}
			if mutation == "credential_forced" && !after.ForcedCooldownUntil.Equal(concurrent.ForcedCooldownUntil) {
				t.Error("refresh cleared a concurrent credential forced action")
			}
			if mutation == "credential_quota" && (!after.Quota.NextRecoverAt.Equal(concurrent.Quota.NextRecoverAt) || after.Quota.Reason != "credential_quota") {
				t.Error("refresh cleared a concurrent credential quota")
			}
			blockedModel := result.Model
			if blocked, _, _ := isAuthBlockedForModel(after, blockedModel, deadline.Add(time.Second)); !blocked {
				t.Error("refresh cleared a concurrent error before its own deadline")
			}
		})
	}
}

type aggregateBlockingRefreshExecutor struct {
	unauthorizedRefreshExecutor
	started chan struct{}
	release chan struct{}
}

func (e *aggregateBlockingRefreshExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	close(e.started)
	select {
	case <-e.release:
		return e.unauthorizedRefreshExecutor.Refresh(ctx, auth)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestManager_ForcedCooldownRefreshAggregateBoundaries(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	SetQuotaCooldownDisabled(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousDisabled) })
	for _, boundary := range []string{"ordinary_recovery", "expired_force", "credential_forced", "credential_quota", "credential_ordinary_quota", "credential_ordinary_401", "disabled", "delete_ordinary_model", "delete_forced_model", "nil_forced_model", "clear_forced_model"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			m, auth := newCooldownMonotonicManager(t, "boundary-model")
			store := NewFileCooldownStateStore(t.TempDir())
			m.SetCooldownStateStore(store)
			forced := 10 * time.Minute
			if boundary != "ordinary_recovery" && boundary != "delete_ordinary_model" {
				m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: "boundary-model", RetryAfter: &forced, Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests}})
			}
			m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: "boundary-model", Error: &Error{HTTPStatus: http.StatusUnauthorized, Message: "same unauthorized text"}})
			longer := 20 * time.Minute
			if boundary == "credential_forced" || boundary == "credential_quota" || boundary == "credential_ordinary_quota" || boundary == "credential_ordinary_401" {
				result := Result{AuthID: auth.ID, Provider: auth.Provider, RetryAfter: &longer, Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "credential quota"}}
				if boundary == "credential_forced" {
					result.Error.Code = ErrorCodeForceCooldown
				} else if boundary == "credential_quota" {
					result.Model = "boundary-model"
					result.CredentialScope = true
				} else if boundary == "credential_ordinary_401" {
					result.Error = &Error{HTTPStatus: http.StatusUnauthorized, Message: "same unauthorized text"}
				}
				m.MarkResult(ctx, result)
			}
			m.mu.Lock()
			current := m.auths[auth.ID]
			observed := time.Now()
			current.Quota.Signals = map[string]string{"X-Codex-Plan-Type": "test-plan"}
			current.Quota.ObservedAt = observed
			current.ModelStates["boundary-model"].Quota.Signals = map[string]string{"X-Codex-Plan-Type": "model-plan"}
			current.ModelStates["boundary-model"].Quota.ObservedAt = observed
			if boundary == "expired_force" {
				current.ModelStates["boundary-model"].ForcedCooldownUntil = time.Now().Add(-time.Minute)
			}
			m.mu.Unlock()
			before, _ := m.GetByID(auth.ID)
			updated := before.Clone()
			updated.LastError = nil
			updated.Status = StatusActive
			updated.Unavailable = false
			clearUnauthorizedModelStates(updated, time.Now())
			switch boundary {
			case "disabled":
				updated.Disabled = true
				updated.Status = StatusDisabled
			case "delete_ordinary_model", "delete_forced_model":
				delete(updated.ModelStates, "boundary-model")
			case "nil_forced_model":
				updated.ModelStates["boundary-model"] = nil
			case "clear_forced_model":
				resetModelState(updated.ModelStates["boundary-model"], time.Now())
			}
			if _, errUpdate := m.UpdateRefreshedAuth(ctx, before, updated); errUpdate != nil {
				t.Fatal(errUpdate)
			}
			after, _ := m.GetByID(auth.ID)
			if !reflect.DeepEqual(after.Quota.Signals, before.Quota.Signals) || !after.Quota.ObservedAt.Equal(observed) {
				t.Error("aggregate recovery discarded passive quota observations")
			}
			switch boundary {
			case "ordinary_recovery", "expired_force", "delete_ordinary_model":
				if blocked, _, _ := isAuthBlockedForModel(after, "", time.Now()); blocked || after.LastError != nil || after.Status != StatusActive {
					t.Error("model recovery left stale credential availability or error status")
				}
				records, errLoad := store.Load(ctx)
				if errLoad != nil || len(records) != 0 {
					t.Errorf("recovery did not remove persisted cooldowns: records=%d error=%v", len(records), errLoad)
				}
				if boundary == "delete_ordinary_model" && len(after.ModelStates) != 0 {
					t.Error("refresh replacement resurrected a deleted ordinary model state")
				}
			case "delete_forced_model", "nil_forced_model", "clear_forced_model":
				deadline := before.ModelStates["boundary-model"].ForcedCooldownUntil
				assertRecoveredForcedAggregate(t, after, "boundary-model", deadline)
				assertForcedAggregateSchedulerExpiry(t, m, auth, "boundary-model", deadline)
			case "disabled":
				if !after.Disabled || after.Status != StatusDisabled {
					t.Error("aggregate recovery re-enabled a disabled credential")
				}
			default:
				if !after.NextRetryAfter.Equal(before.NextRetryAfter) || !reflect.DeepEqual(cooldownFieldsOf(after.Quota), cooldownFieldsOf(before.Quota)) || !after.ForcedCooldownUntil.Equal(before.ForcedCooldownUntil) {
					t.Error("model recovery changed an independent credential cooldown")
				}
			}
		})
	}
}

func TestManager_ForcedCooldownRefreshPreservesEarlierCredentialQuota(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	SetQuotaCooldownDisabled(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousDisabled) })
	for _, recovery := range []string{"refresh", "success"} {
		t.Run(recovery, func(t *testing.T) {
			ctx := context.Background()
			m, auth := newCooldownMonotonicManager(t, "earlier-quota-model")
			store := NewFileCooldownStateStore(t.TempDir())
			m.SetCooldownStateStore(store)
			credentialQuota := 2 * time.Hour
			m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, RetryAfter: &credentialQuota, Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "credential rate limit"}})
			forced := 10 * time.Minute
			m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: "earlier-quota-model", RetryAfter: &forced, Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests}})
			m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: "earlier-quota-model", Error: &Error{HTTPStatus: http.StatusUnauthorized}})
			before, _ := m.GetByID(auth.ID)
			if recovery == "refresh" {
				m.RegisterExecutor(&unauthorizedRefreshExecutor{id: auth.Provider})
				if _, errRefresh := m.refreshAuthForRequest(ctx, auth.ID, ""); errRefresh != nil {
					t.Fatal(errRefresh)
				}
			} else {
				m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: "earlier-quota-model", Success: true})
			}
			after, _ := m.GetByID(auth.ID)
			if !after.Quota.NextRecoverAt.Equal(before.Quota.NextRecoverAt) || !after.Quota.Exceeded {
				t.Error("model recovery cleared a credential quota that was not derived from the model")
			}
			restored, _ := newCooldownMonotonicManager(t, "earlier-quota-model")
			restored.SetCooldownStateStore(store)
			if errRestore := restored.RestoreCooldownStates(ctx); errRestore != nil {
				t.Fatal(errRestore)
			}
			afterRestore, _ := restored.GetByID(auth.ID)
			if !afterRestore.Quota.NextRecoverAt.Equal(before.Quota.NextRecoverAt) || !afterRestore.Quota.Exceeded {
				t.Error("restoration lost the independent credential quota after model recovery")
			}
		})
	}
}
