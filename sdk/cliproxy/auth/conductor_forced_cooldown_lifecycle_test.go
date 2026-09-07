package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestManager_ForcedCooldownSurvivesAuthUpdates(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	SetQuotaCooldownDisabled(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousDisabled) })
	for _, model := range []string{"", "lifecycle-model"} {
		for _, mode := range []string{"replace_fresh", "replace_stale_clean", "replace_stale_forced", "prepare", "refresh"} {
			t.Run("model="+model+"/"+mode, func(t *testing.T) {
				ctx := context.Background()
				m, auth := newCooldownMonotonicManager(t, "lifecycle-model", "other-model")
				store := NewFileCooldownStateStore(t.TempDir())
				m.SetCooldownStateStore(store)
				m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: "lifecycle-model", Success: true})
				base, _ := m.GetByID(auth.ID)
				retry := 10 * time.Minute
				forced := Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, RetryAfter: &retry, Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests, Message: "explicit quota"}}
				m.MarkResult(ctx, forced)
				staleForced, _ := m.GetByID(auth.ID)
				retry = 20 * time.Minute
				m.MarkResult(ctx, forced)
				m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, Error: &Error{HTTPStatus: http.StatusNotFound, Message: "later ordinary failure"}})
				before := forcedCooldownTestState(t, m, auth.ID, model)
				incoming := base.Clone()
				if mode == "replace_fresh" {
					incoming = &Auth{ID: auth.ID, Provider: auth.Provider, Status: StatusActive}
				} else if mode == "replace_stale_forced" {
					incoming = staleForced
				}
				incoming.Metadata = map[string]any{"notes": "updated note"}
				var errUpdate error
				switch mode {
				case "prepare":
					_, errUpdate = m.UpdatePreparedAuth(ctx, base, incoming)
				case "refresh":
					_, errUpdate = m.UpdateRefreshedAuth(ctx, base, incoming)
				default:
					_, errUpdate = m.Update(ctx, incoming)
				}
				if errUpdate != nil {
					t.Fatal(errUpdate)
				}
				after := forcedCooldownTestState(t, m, auth.ID, model)
				if !after.ForcedCooldownUntil.Equal(before.ForcedCooldownUntil) || !after.NextRetryAfter.Equal(before.NextRetryAfter) || !after.Unavailable {
					t.Errorf("auth update lost the latest cooldown: forced=%v next=%v unavailable=%v; want forced=%v next=%v", after.ForcedCooldownUntil, after.NextRetryAfter, after.Unavailable, before.ForcedCooldownUntil, before.NextRetryAfter)
				}
				snapshot, _ := m.GetByID(auth.ID)
				if snapshot.Metadata["notes"] != "updated note" {
					t.Error("cooldown preservation discarded the configuration update")
				}
				if blocked, _, _ := isAuthBlockedForModel(snapshot, "other-model", time.Now()); blocked != (model == "") {
					t.Errorf("auth update changed cooldown scope: other model blocked=%v", blocked)
				}
				m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, Success: true})
				after = forcedCooldownTestState(t, m, auth.ID, model)
				if !after.ForcedCooldownUntil.Equal(before.ForcedCooldownUntil) || !after.NextRetryAfter.Equal(before.ForcedCooldownUntil) {
					t.Error("success after auth update did not retain exactly the forced deadline")
				}
				if picked, errPick := m.scheduler.pickSingle(ctx, auth.Provider, "lifecycle-model", cliproxyexecutor.Options{}, nil); picked != nil || errPick == nil {
					t.Error("scheduler selected the credential after auth replacement and success")
				}
				records, errLoad := store.Load(ctx)
				if errLoad != nil {
					t.Fatal(errLoad)
				}
				found := false
				for _, record := range records {
					found = found || (record.Model == model && record.ForcedCooldownUntil.Equal(before.ForcedCooldownUntil))
				}
				if !found {
					t.Error("auth update lost the persisted forced cooldown")
				}
			})
		}
	}
}

func TestManager_ForcedCooldownSurvivesUnauthorizedRefresh(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	SetQuotaCooldownDisabled(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousDisabled) })
	for _, model := range []string{"", "refresh-model"} {
		for _, forced := range []bool{false, true} {
			name := "ordinary"
			if forced {
				name = "forced"
			}
			t.Run("model="+model+"/"+name, func(t *testing.T) {
				ctx := context.Background()
				m, auth := newCooldownMonotonicManager(t, "refresh-model", "other-model")
				m.RegisterExecutor(&unauthorizedRefreshExecutor{id: auth.Provider})
				if forced {
					retry := 10 * time.Minute
					m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, RetryAfter: &retry, Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests}})
				}
				m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, Error: &Error{HTTPStatus: http.StatusUnauthorized, Message: "expired token"}})
				before := forcedCooldownTestState(t, m, auth.ID, model)
				if _, errRefresh := m.refreshAuthForRequest(ctx, auth.ID, ""); errRefresh != nil {
					t.Fatal(errRefresh)
				}
				after := forcedCooldownTestState(t, m, auth.ID, model)
				if forced && (!after.ForcedCooldownUntil.Equal(before.ForcedCooldownUntil) || !after.Unavailable) {
					t.Error("successful token refresh cleared an active explicit cooldown")
				}
				if !forced && model != "" && (after.Unavailable || !after.NextRetryAfter.IsZero()) {
					t.Error("ordinary model 401 did not recover after token refresh")
				}
			})
		}
	}
}

func TestManager_ForcedCooldownResetThenStaleUpdate(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	SetQuotaCooldownDisabled(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousDisabled) })
	for _, model := range []string{"", "reset-model"} {
		for _, mode := range []string{"replace", "prepare", "refresh"} {
			t.Run("model="+model+"/"+mode, func(t *testing.T) {
				ctx := context.Background()
				m, auth := newCooldownMonotonicManager(t, "reset-model", "other-model")
				retry := 10 * time.Minute
				m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, RetryAfter: &retry, Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests}})
				stale, _ := m.GetByID(auth.ID)
				if _, _, errReset := m.ResetQuota(ctx, auth.ID); errReset != nil {
					t.Fatal(errReset)
				}
				var errUpdate error
				switch mode {
				case "prepare":
					_, errUpdate = m.UpdatePreparedAuth(ctx, stale, stale.Clone())
				case "refresh":
					_, errUpdate = m.UpdateRefreshedAuth(ctx, stale, stale.Clone())
				default:
					_, errUpdate = m.Update(ctx, stale.Clone())
				}
				if errUpdate != nil {
					t.Fatal(errUpdate)
				}
				snapshot, _ := m.GetByID(auth.ID)
				if blocked, _, _ := isAuthBlockedForModel(snapshot, "reset-model", time.Now()); blocked {
					t.Error("stale auth update resurrected an explicitly reset cooldown")
				}
				if snapshot.LastError != nil || snapshot.Status != StatusActive {
					t.Error("stale auth update resurrected the reset error status")
				}
			})
		}
	}
}

func TestManager_ForcedCooldownRestoreDoesNotShortenLiveState(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	SetQuotaCooldownDisabled(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousDisabled) })
	for _, aggregate := range []bool{false, true} {
		name := "older_auth_record"
		if aggregate {
			name = "model_aggregate"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			m, auth := newCooldownMonotonicManager(t, "restore-live-model", "other-model")
			retry := 20 * time.Minute
			m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, RetryAfter: &retry, Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests}})
			before, _ := m.GetByID(auth.ID)
			old := CooldownStateRecord{AuthID: auth.ID, Provider: auth.Provider, NextRetryAfter: time.Now().Add(10 * time.Minute), LastError: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusBadGateway}}
			if !aggregate {
				old.ForcedCooldownUntil = old.NextRetryAfter
			}
			records := []CooldownStateRecord{old}
			if aggregate {
				old.Model = "restore-live-model"
				records = append(records, old)
			}
			store := NewFileCooldownStateStore(t.TempDir())
			if errSave := store.Save(ctx, records); errSave != nil {
				t.Fatal(errSave)
			}
			m.SetCooldownStateStore(store)
			if errRestore := m.RestoreCooldownStates(ctx); errRestore != nil {
				t.Fatal(errRestore)
			}
			after, _ := m.GetByID(auth.ID)
			if !after.ForcedCooldownUntil.Equal(before.ForcedCooldownUntil) || after.NextRetryAfter.Before(before.ForcedCooldownUntil) {
				t.Error("loading an older sidecar shortened a live credential forced cooldown")
			}
			m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Success: true})
			after, _ = m.GetByID(auth.ID)
			if blocked, _, next := isAuthBlockedForModel(after, "other-model", time.Now()); !blocked || next.Before(before.ForcedCooldownUntil) {
				t.Error("restoration and success unblocked the credential too early")
			}
		})
	}
}

func TestManager_ForcedCooldownLifecycleReleaseBoundaries(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	SetQuotaCooldownDisabled(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousDisabled) })
	for _, model := range []string{"", "boundary-model"} {
		for _, action := range []string{"expire", "reset", "disable", "disable_status", "disable_cooling", "remove"} {
			t.Run("model="+model+"/"+action, func(t *testing.T) {
				ctx := context.Background()
				m, auth := newCooldownMonotonicManager(t, "boundary-model", "other-model")
				retry := 10 * time.Minute
				m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, RetryAfter: &retry, Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests}})
				fresh := &Auth{ID: auth.ID, Provider: auth.Provider, Status: StatusActive}
				switch action {
				case "expire":
					m.mu.Lock()
					current := m.auths[auth.ID]
					past := time.Now().Add(-time.Minute)
					current.ForcedCooldownUntil, current.NextRetryAfter, current.Quota.NextRecoverAt = past, past, past
					for _, state := range current.ModelStates {
						state.ForcedCooldownUntil, state.NextRetryAfter, state.Quota.NextRecoverAt = past, past, past
					}
					m.mu.Unlock()
				case "reset":
					if _, _, errReset := m.ResetQuota(ctx, auth.ID); errReset != nil {
						t.Fatal(errReset)
					}
				case "disable", "disable_status":
					disabled := fresh.Clone()
					disabled.Disabled = action == "disable"
					if action == "disable_status" {
						disabled.Status = StatusDisabled
					}
					if _, errUpdate := m.Update(ctx, disabled); errUpdate != nil {
						t.Fatal(errUpdate)
					}
				case "disable_cooling":
					m.SetConfig(&internalconfig.Config{DisableCooling: true})
					m.SetConfig(&internalconfig.Config{})
				case "remove":
					m.Remove(ctx, auth.ID)
					if _, errRegister := m.Register(ctx, fresh); errRegister != nil {
						t.Fatal(errRegister)
					}
				}
				if _, errUpdate := m.Update(ctx, fresh); errUpdate != nil {
					t.Fatal(errUpdate)
				}
				snapshot, _ := m.GetByID(auth.ID)
				if blocked, _, _ := isAuthBlockedForModel(snapshot, "boundary-model", time.Now()); blocked {
					t.Error("auth update retained a cooldown after its release boundary")
				}
				if picked, errPick := m.scheduler.pickSingle(ctx, auth.Provider, "boundary-model", cliproxyexecutor.Options{}, nil); picked == nil || errPick != nil {
					t.Errorf("scheduler did not release the credential: %v", errPick)
				}
			})
		}
	}
}

func TestManager_ForcedCooldownConcurrentReplacementAndResults(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	SetQuotaCooldownDisabled(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousDisabled) })
	for _, model := range []string{"", "concurrent-model"} {
		t.Run("model="+model, func(t *testing.T) {
			ctx := context.Background()
			m, auth := newCooldownMonotonicManager(t, "concurrent-model", "other-model")
			retry := 20 * time.Minute
			m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, RetryAfter: &retry, Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests}})
			deadline := forcedCooldownTestState(t, m, auth.ID, model).ForcedCooldownUntil
			start := make(chan struct{})
			var wg sync.WaitGroup
			for worker := range 4 {
				wg.Go(func() {
					<-start
					for range 20 {
						switch worker {
						case 0:
							_, errUpdate := m.Update(ctx, &Auth{ID: auth.ID, Provider: auth.Provider, Status: StatusActive})
							if errUpdate != nil {
								t.Error(errUpdate)
							}
						case 1:
							base, _ := m.GetByID(auth.ID)
							if _, errUpdate := m.UpdatePreparedAuth(ctx, base, base.Clone()); errUpdate != nil {
								t.Error(errUpdate)
							}
						case 2:
							m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, Success: true})
						case 3:
							m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, Error: &Error{HTTPStatus: http.StatusNotFound}})
						}
					}
				})
			}
			close(start)
			wg.Wait()
			m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, Success: true})
			state := forcedCooldownTestState(t, m, auth.ID, model)
			if !state.ForcedCooldownUntil.Equal(deadline) || !state.NextRetryAfter.Equal(deadline) {
				t.Error("concurrent auth updates and results changed the explicit deadline")
			}
		})
	}
}

func TestManager_ForcedCooldownSurvivesRegistryReconciliation(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	SetQuotaCooldownDisabled(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousDisabled) })
	ctx := context.Background()
	m, auth := newCooldownMonotonicManager(t, "removed-model", "kept-model")
	m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: "removed-model", Error: &Error{HTTPStatus: http.StatusNotFound}})
	retry := 10 * time.Minute
	m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, RetryAfter: &retry, Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests}})
	before, _ := m.GetByID(auth.ID)
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "kept-model"}})
	m.ReconcileRegistryModelStates(ctx, auth.ID)
	after, _ := m.GetByID(auth.ID)
	if !after.ForcedCooldownUntil.Equal(before.ForcedCooldownUntil) || !after.Unavailable || after.Status != StatusError || after.LastError == nil {
		t.Error("model reconciliation cleared active credential-level cooldown state")
	}
	if _, exists := after.ModelStates["removed-model"]; exists {
		t.Error("forced credential cooldown prevented obsolete model cleanup")
	}
}

func TestManager_ForcedCooldownCrossScopeSuccess(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	SetQuotaCooldownDisabled(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousDisabled) })
	for _, ordinaryModel := range []string{"", "healed-model"} {
		t.Run("ordinary_model="+ordinaryModel, func(t *testing.T) {
			ctx := context.Background()
			m, auth := newCooldownMonotonicManager(t, "healed-model", "still-cooling-model")
			m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: ordinaryModel, Error: &Error{HTTPStatus: http.StatusNotFound}})
			retry := 10 * time.Minute
			m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, RetryAfter: &retry, Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests}})
			before, _ := m.GetByID(auth.ID)
			// A separate failed model must retain its own ordinary cooldown.
			m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: "still-cooling-model", Error: &Error{HTTPStatus: http.StatusNotFound}})
			m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: "healed-model", Success: true})
			after, _ := m.GetByID(auth.ID)
			if !after.NextRetryAfter.Equal(before.ForcedCooldownUntil) || after.Quota.Exceeded {
				t.Error("model success retained ordinary credential cooldown beyond the explicit deadline")
			}
			afterForcedExpiry := before.ForcedCooldownUntil.Add(time.Second)
			if blocked, _, _ := isAuthBlockedForModel(after, "healed-model", afterForcedExpiry); blocked {
				t.Error("healed model remained blocked after the credential forced deadline")
			}
			if blocked, _, _ := isAuthBlockedForModel(after, "still-cooling-model", afterForcedExpiry); !blocked {
				t.Error("success cleared a different model's ordinary cooldown")
			}
		})
	}
}

func TestAuthCloneEmptyRuntimeMapsAreIndependent(t *testing.T) {
	auth := &Auth{Attributes: map[string]string{}, Metadata: map[string]any{}, ModelStates: map[string]*ModelState{}}
	snapshot := auth.Clone()
	snapshot.Attributes["test"] = "value"
	snapshot.Metadata["test"] = "value"
	snapshot.ModelStates["test"] = &ModelState{}
	if len(auth.Attributes) != 0 || len(auth.Metadata) != 0 || len(auth.ModelStates) != 0 {
		t.Error("mutating a cloned empty map changed the original auth snapshot")
	}
}
