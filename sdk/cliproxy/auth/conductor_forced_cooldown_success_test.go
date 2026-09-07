package auth

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestManager_ForcedCooldownSurvivesInFlightSuccess(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	previousTransient := transientErrorCooldownSeconds.Load()
	SetQuotaCooldownDisabled(false)
	SetTransientErrorCooldownSeconds(600)
	t.Cleanup(func() {
		quotaCooldownDisabled.Store(previousDisabled)
		transientErrorCooldownSeconds.Store(previousTransient)
	})
	for _, mode := range []string{"execute", "stream", "count"} {
		for _, tc := range []struct {
			name   string
			status int
			code   string
			action string
		}{
			{"502", http.StatusBadGateway, "server_is_overloaded", RequestScopedActionStopAndCooldown},
			{"503", http.StatusServiceUnavailable, "server_is_overloaded", RequestScopedActionStopAndCooldown},
			{"429", http.StatusTooManyRequests, "rate_limit_exceeded", RequestScopedActionStopAndCooldown},
			{"continue", http.StatusBadGateway, "server_is_overloaded", RequestScopedActionContinueAndCooldown},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				testForcedCooldownInFlightSuccess(t, mode, tc.status, tc.code, tc.action)
			})
		}
	}
}

func testForcedCooldownInFlightSuccess(t *testing.T, mode string, status int, code, action string) {
	t.Helper()
	const model = "forced-cooldown-model"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(0, 0, 1)
	store := NewFileCooldownStateStore(t.TempDir())
	m.SetCooldownStateStore(store)
	auth := &Auth{
		ID: "forced-cooldown-auth", Provider: "codex", Status: StatusActive,
		Metadata: map[string]any{
			"request_scoped_errors": []internalconfig.RequestScopedErrorRule{{
				Status: status, Match: []string{code}, Action: action,
			}},
		},
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}, {ID: "other-model"}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, errRegister := m.Register(ctx, auth); errRegister != nil {
		t.Fatal(errRegister)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	finishSuccess := func() { releaseOnce.Do(func() { close(release) }) }
	defer finishSuccess()
	var calls atomic.Int32
	mock := mockCustomErrorExecutor{
		identifier: "codex",
		executeFn: func(ctx context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			calls.Add(1)
			cliproxyexecutor.MarkUpstreamAttempt(ctx)
			switch string(req.Payload) {
			case "pending-success":
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return cliproxyexecutor.Response{}, ctx.Err()
				}
			case "overload":
				retryAfter := 10 * time.Minute
				return cliproxyexecutor.Response{}, customStatusError{
					code: status, retryAfter: &retryAfter,
					msg: `{"error":{"code":"` + code + `"}}`,
				}
			}
			return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
		},
	}
	mock.countFn = mock.executeFn
	m.RegisterExecutor(&customStreamMockExecutor{
		identifier: "codex", mockCustomErrorExecutor: mock,
		streamFn: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			resp, errExec := mock.Execute(ctx, auth, req, opts)
			if errExec != nil {
				return nil, errExec
			}
			chunks := make(chan cliproxyexecutor.StreamChunk, 1)
			chunks <- cliproxyexecutor.StreamChunk{Payload: resp.Payload}
			close(chunks)
			return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
		},
	})
	invoke := func(payload string) error {
		req := cliproxyexecutor.Request{Model: model, Payload: []byte(payload)}
		switch mode {
		case "stream":
			stream, errStream := m.ExecuteStream(ctx, []string{"codex"}, req, cliproxyexecutor.Options{})
			if errStream != nil {
				return errStream
			}
			var errChunk error
			for chunk := range stream.Chunks {
				if chunk.Err != nil {
					errChunk = chunk.Err
				}
			}
			return errChunk
		case "count":
			_, errCount := m.ExecuteCount(ctx, []string{"codex"}, req, cliproxyexecutor.Options{})
			return errCount
		default:
			_, errExec := m.Execute(ctx, []string{"codex"}, req, cliproxyexecutor.Options{})
			return errExec
		}
	}

	successDone := make(chan error, 1)
	var pending sync.WaitGroup
	pending.Add(1)
	defer func() {
		finishSuccess()
		cancel()
		pending.Wait()
	}()
	go func() {
		defer pending.Done()
		successDone <- invoke("pending-success")
	}()
	select {
	case <-started:
	case errEarly := <-successDone:
		t.Fatalf("pending request exited before executor entry: %v", errEarly)
	}
	errExec := invoke("overload")
	if statusCodeFromError(errExec) != status {
		t.Fatalf("overload error = %v, want HTTP %d", errExec, status)
	}
	before, _ := m.GetByID(auth.ID)
	stateBefore := before.ModelStates[model]
	if stateBefore == nil || stateBefore.LastError == nil || stateBefore.LastError.Code != ErrorCodeForceCooldown || time.Until(stateBefore.NextRetryAfter) < 9*time.Minute {
		t.Fatalf("failure did not establish the configured forced cooldown: %+v", stateBefore)
	}
	deadline := stateBefore.NextRetryAfter
	t.Logf("after HTTP %d: cooling until %s", status, deadline.Format(time.RFC3339Nano))

	finishSuccess()
	if errSuccess := <-successDone; errSuccess != nil {
		t.Fatalf("in-flight success failed: %v", errSuccess)
	}
	after, _ := m.GetByID(auth.ID)
	stateAfter := after.ModelStates[model]
	if stateAfter == nil || !stateAfter.Unavailable || !stateAfter.NextRetryAfter.Equal(deadline) || !stateAfter.ForcedCooldownUntil.Equal(deadline) {
		t.Errorf("in-flight success cleared forced cooldown: %+v", stateAfter)
	}
	if after.Success != 1 || after.Failed != 1 {
		t.Errorf("success/failure accounting changed: success=%d failed=%d", after.Success, after.Failed)
	}
	records, errLoad := store.Load(ctx)
	found := false
	for _, record := range records {
		if record.Model == model && record.NextRetryAfter.Equal(deadline) && record.ForcedCooldownUntil.Equal(deadline) && record.LastError != nil && record.LastError.Code == ErrorCodeForceCooldown {
			found = true
		}
	}
	if errLoad != nil || !found {
		t.Errorf("persisted cooldown lost after in-flight success: records=%v error=%v", records, errLoad)
	}
	errRetry := invoke("")
	if errRetry == nil || calls.Load() != 2 {
		t.Errorf("new request reused cooling credential: calls=%d error=%v", calls.Load(), errRetry)
	}
	if blocked, _, _ := isAuthBlockedForModel(after, "other-model", time.Now()); blocked {
		t.Error("forced model cooldown blocked an unrelated model")
	}
}

func TestManager_ForcedCooldownRecoveryBoundaries(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	previousTransient := transientErrorCooldownSeconds.Load()
	t.Cleanup(func() {
		quotaCooldownDisabled.Store(previousDisabled)
		transientErrorCooldownSeconds.Store(previousTransient)
	})
	for _, scope := range []string{"model", "credential"} {
		for _, tc := range []struct {
			name          string
			ordinary      bool
			expired       bool
			manualReset   bool
			secondFailure bool
			disable       bool
			transient     int
		}{
			{name: "active"},
			{name: "expired", expired: true},
			{name: "ordinary", ordinary: true},
			{name: "manual_reset", manualReset: true},
			{name: "intervening_failure", secondFailure: true, transient: 15},
			{name: "intervening_failure_cooling_disabled", secondFailure: true, disable: true, transient: -1},
		} {
			t.Run(scope+"/"+tc.name, func(t *testing.T) {
				SetQuotaCooldownDisabled(false)
				SetTransientErrorCooldownSeconds(600)
				ctx := context.Background()
				m, auth := newCooldownMonotonicManager(t, "recovery-model")
				store := NewFileCooldownStateStore(t.TempDir())
				m.SetCooldownStateStore(store)
				model := "recovery-model"
				if scope == "credential" {
					model = ""
				}
				code := ErrorCodeForceCooldown
				if tc.ordinary {
					code = ""
				}
				m.MarkResult(ctx, Result{
					AuthID: auth.ID, Provider: auth.Provider, Model: model,
					Error: &Error{Code: code, HTTPStatus: http.StatusBadGateway, Message: "server_is_overloaded"},
				})
				before, _ := m.GetByID(auth.ID)
				deadline := before.NextRetryAfter
				if model != "" {
					deadline = before.ModelStates[model].NextRetryAfter
				}
				if !deadline.After(time.Now()) {
					t.Fatal("initial cooldown was not established")
				}
				if tc.expired {
					// Advance only the stored deadline; do not wait in real time.
					m.mu.Lock()
					current := m.auths[auth.ID]
					current.NextRetryAfter = time.Now().Add(-time.Second)
					current.ForcedCooldownUntil = current.NextRetryAfter
					if model != "" {
						current.ModelStates[model].NextRetryAfter = current.NextRetryAfter
						current.ModelStates[model].ForcedCooldownUntil = current.NextRetryAfter
					}
					m.mu.Unlock()
				}
				if tc.secondFailure {
					SetQuotaCooldownDisabled(tc.disable)
					SetTransientErrorCooldownSeconds(tc.transient)
					secondErr := &Error{HTTPStatus: http.StatusInternalServerError, Message: "later failure"}
					m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, Error: secondErr})
					if secondErr.Code != "" {
						t.Error("MarkResult mutated the caller's error")
					}
				}
				if tc.manualReset {
					if _, _, errReset := m.ResetQuota(ctx, auth.ID); errReset != nil {
						t.Fatal(errReset)
					}
				}
				m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, Success: true})
				after, _ := m.GetByID(auth.ID)
				unavailable, next, lastError := after.Unavailable, after.NextRetryAfter, after.LastError
				if model != "" {
					state := after.ModelStates[model]
					unavailable, next, lastError = state.Unavailable, state.NextRetryAfter, state.LastError
				}
				wantRetained := !tc.ordinary && !tc.expired && !tc.manualReset
				if wantRetained {
					wantCode := ErrorCodeForceCooldown
					if tc.secondFailure {
						wantCode = ""
					}
					if !unavailable || !next.Equal(deadline) || lastError == nil || lastError.Code != wantCode {
						t.Fatalf("forced cooldown not retained: unavailable=%v next=%v lastError=%v", unavailable, next, lastError)
					}
					if blocked, _, _ := isAuthBlockedForModel(after, "recovery-model", deadline.Add(time.Second)); blocked {
						t.Error("credential remains blocked after the forced deadline")
					}
					// disable-cooling also disables sidecar persistence by design.
					if tc.disable {
						return
					}
					// A restored sidecar must retain the forced marker and deadline.
					restored := NewManager(nil, nil, nil)
					restored.SetCooldownStateStore(store)
					if _, errRegister := restored.Register(ctx, auth); errRegister != nil {
						t.Fatal(errRegister)
					}
					if errRestore := restored.RestoreCooldownStates(ctx); errRestore != nil {
						t.Fatal(errRestore)
					}
					restored.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, Success: true})
					restoredAuth, _ := restored.GetByID(auth.ID)
					if blocked, _, _ := isAuthBlockedForModel(restoredAuth, "recovery-model", time.Now()); !blocked {
						t.Error("restored forced cooldown was cleared by success")
					}
				} else if unavailable || !next.IsZero() || lastError != nil {
					t.Fatalf("expected normal recovery: unavailable=%v next=%v lastError=%v", unavailable, next, lastError)
				}
			})
		}
	}
}

func TestManager_ForcedCooldownKeepsOrdinaryDeadlinesSeparate(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	previousTransient := transientErrorCooldownSeconds.Load()
	t.Cleanup(func() {
		quotaCooldownDisabled.Store(previousDisabled)
		transientErrorCooldownSeconds.Store(previousTransient)
	})
	for _, scope := range []string{"model", "credential"} {
		for _, tc := range []struct {
			name         string
			status       int
			forcedStatus int
			reversed     bool
		}{
			{name: "forced_then_404", status: http.StatusNotFound},
			{name: "forced_then_401", status: http.StatusUnauthorized},
			{name: "forced_then_429", status: http.StatusTooManyRequests},
			{name: "404_then_forced", status: http.StatusNotFound, reversed: true},
			{name: "429_then_forced_429", status: http.StatusTooManyRequests, forcedStatus: http.StatusTooManyRequests, reversed: true},
		} {
			for _, restore := range []bool{false, true} {
				for _, expired := range []bool{false, true} {
					name := scope + "/" + tc.name
					if restore {
						name += "/restored"
					} else {
						name += "/live"
					}
					if expired {
						name += "/after_forced_expiry"
					} else {
						name += "/during_forced_cooldown"
					}
					t.Run(name, func(t *testing.T) {
						SetQuotaCooldownDisabled(false)
						SetTransientErrorCooldownSeconds(600)
						ctx := context.Background()
						m, auth := newCooldownMonotonicManager(t, "separate-deadlines")
						model := "separate-deadlines"
						if scope == "credential" {
							model = ""
						}
						store := NewFileCooldownStateStore(t.TempDir())
						m.SetCooldownStateStore(store)
						forced := Result{
							AuthID: auth.ID, Provider: auth.Provider, Model: model,
							Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusBadGateway, Message: "server_is_overloaded"},
						}
						if tc.forcedStatus != 0 {
							forced.Error.HTTPStatus = tc.forcedStatus
							forcedRetry := 10 * time.Minute
							forced.RetryAfter = &forcedRetry
						}
						ordinaryRetry := 2 * time.Hour
						ordinary := Result{
							AuthID: auth.ID, Provider: auth.Provider, Model: model, RetryAfter: &ordinaryRetry,
							Error: &Error{HTTPStatus: tc.status, Message: "ordinary upstream failure"},
						}
						beforeForce := time.Now()
						if tc.reversed {
							m.MarkResult(ctx, ordinary)
							m.MarkResult(ctx, forced)
						} else {
							m.MarkResult(ctx, forced)
							m.MarkResult(ctx, ordinary)
						}
						state := forcedCooldownTestState(t, m, auth.ID, model)
						forcedUntil := state.ForcedCooldownUntil
						if forcedUntil.Before(beforeForce.Add(10*time.Minute)) || forcedUntil.After(time.Now().Add(10*time.Minute)) {
							t.Fatalf("forced deadline inherited an ordinary duration: %v", forcedUntil.Sub(beforeForce))
						}
						if !state.NextRetryAfter.After(forcedUntil.Add(19 * time.Minute)) {
							t.Fatalf("ordinary cooldown was lost before success: %+v", state)
						}
						if !tc.reversed && (state.LastError == nil || state.LastError.Code != "" || state.LastError.HTTPStatus != tc.status) {
							t.Fatalf("ordinary error was relabeled as forced: %+v", state.LastError)
						}
						if ordinary.Error.Code != "" {
							t.Fatal("caller-owned ordinary error was mutated")
						}

						if expired {
							// Expire only the explicit action, leaving the longer ordinary
							// deadline live. No wall-clock waiting is needed.
							m.mu.Lock()
							current := m.auths[auth.ID]
							if model == "" {
								current.ForcedCooldownUntil = time.Now().Add(-time.Second)
							} else {
								current.ModelStates[model].ForcedCooldownUntil = time.Now().Add(-time.Second)
							}
							m.mu.Unlock()
							m.persistCooldownStates(ctx)
						}
						if restore {
							restored := NewManager(nil, nil, nil)
							restored.SetCooldownStateStore(store)
							if _, errRegister := restored.Register(ctx, auth); errRegister != nil {
								t.Fatal(errRegister)
							}
							if errRestore := restored.RestoreCooldownStates(ctx); errRestore != nil {
								t.Fatal(errRestore)
							}
							m = restored
							if model != "" {
								snapshot, _ := m.GetByID(auth.ID)
								if !snapshot.ForcedCooldownUntil.IsZero() {
									t.Fatal("model cooldown was promoted into an auth-level forced deadline")
								}
							}
						}
						m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, Success: true})
						state = forcedCooldownTestState(t, m, auth.ID, model)
						if expired {
							if state.Unavailable || !state.NextRetryAfter.IsZero() || !state.ForcedCooldownUntil.IsZero() || state.LastError != nil {
								t.Fatalf("success did not heal ordinary cooldown after forced expiry: %+v", state)
							}
						} else if !state.Unavailable || !state.NextRetryAfter.Equal(forcedUntil) || !state.ForcedCooldownUntil.Equal(forcedUntil) || state.Quota.Exceeded || !state.Quota.NextRecoverAt.IsZero() {
							t.Fatalf("success should retain only the original forced deadline: %+v", state)
						}
						if _, _, errReset := m.ResetQuota(ctx, auth.ID); errReset != nil {
							t.Fatal(errReset)
						}
						state = forcedCooldownTestState(t, m, auth.ID, model)
						if state.Unavailable || !state.NextRetryAfter.IsZero() || !state.ForcedCooldownUntil.IsZero() {
							t.Fatalf("manual reset left cooldown state behind: %+v", state)
						}
					})
				}
			}
		}
	}
}

func forcedCooldownTestState(t *testing.T, m *Manager, authID, model string) *ModelState {
	t.Helper()
	auth, ok := m.GetByID(authID)
	if !ok || auth == nil {
		t.Fatal("auth missing")
	}
	if model != "" {
		state := auth.ModelStates[model]
		if state == nil {
			t.Fatal("model state missing")
		}
		return state
	}
	return &ModelState{
		Unavailable: auth.Unavailable, NextRetryAfter: auth.NextRetryAfter,
		ForcedCooldownUntil: auth.ForcedCooldownUntil, LastError: auth.LastError, Quota: auth.Quota,
	}
}

func TestManager_ForcedCooldownLegacySidecars(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	SetQuotaCooldownDisabled(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousDisabled) })
	for _, model := range []string{"legacy-model", ""} {
		t.Run("model="+model, func(t *testing.T) {
			ctx := context.Background()
			m, auth := newCooldownMonotonicManager(t, "legacy-model")
			store := NewFileCooldownStateStore(t.TempDir())
			m.SetCooldownStateStore(store)
			deadline := time.Now().Add(10 * time.Minute)
			// This is the old JSON shape: no independent forced deadline.
			if errSave := store.Save(ctx, []CooldownStateRecord{{
				AuthID: auth.ID, Provider: auth.Provider, Model: model,
				NextRetryAfter: deadline, LastError: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusBadGateway},
			}}); errSave != nil {
				t.Fatal(errSave)
			}
			if errRestore := m.RestoreCooldownStates(ctx); errRestore != nil {
				t.Fatal(errRestore)
			}
			m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, Success: true})
			state := forcedCooldownTestState(t, m, auth.ID, model)
			if !state.Unavailable || !state.NextRetryAfter.Equal(deadline) || !state.ForcedCooldownUntil.Equal(deadline) {
				t.Fatalf("legacy forced cooldown was lost: %+v", state)
			}
		})
	}
}

func TestManager_ForcedCooldownOnlyExplicitActionsExtendDeadline(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	previousTransient := transientErrorCooldownSeconds.Load()
	t.Cleanup(func() {
		quotaCooldownDisabled.Store(previousDisabled)
		transientErrorCooldownSeconds.Store(previousTransient)
	})
	for _, model := range []string{"forced-extensions", ""} {
		t.Run("model="+model, func(t *testing.T) {
			SetQuotaCooldownDisabled(false)
			SetTransientErrorCooldownSeconds(600)
			ctx := context.Background()
			m, auth := newCooldownMonotonicManager(t, "forced-extensions")
			result := Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusBadGateway}}
			m.MarkResult(ctx, result)
			first := forcedCooldownTestState(t, m, auth.ID, model).ForcedCooldownUntil
			m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, Error: &Error{HTTPStatus: http.StatusNotFound}})
			SetTransientErrorCooldownSeconds(15)
			m.MarkResult(ctx, result)
			state := forcedCooldownTestState(t, m, auth.ID, model)
			if !state.ForcedCooldownUntil.Equal(first) {
				t.Fatalf("shorter explicit action inherited the ordinary 12h deadline: %+v", state)
			}
			longer := 2 * time.Hour
			result.Error = &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests}
			result.RetryAfter = &longer
			before := time.Now()
			m.MarkResult(ctx, result)
			state = forcedCooldownTestState(t, m, auth.ID, model)
			if state.ForcedCooldownUntil.Before(before.Add(longer)) || state.ForcedCooldownUntil.After(time.Now().Add(longer)) {
				t.Fatalf("longer explicit action did not establish its own 2h deadline: %+v", state)
			}
			m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, Success: true})
			after := forcedCooldownTestState(t, m, auth.ID, model)
			if !after.Unavailable || !after.NextRetryAfter.Equal(state.ForcedCooldownUntil) {
				t.Fatalf("success did not retain only the explicit deadline: %+v", after)
			}
		})
	}
}

func TestManager_ForcedCooldownMergeAndRecordEquality(t *testing.T) {
	now := time.Now()
	forcedUntil := now.Add(10 * time.Minute)
	ordinaryUntil := now.Add(12 * time.Hour)
	forced := &ModelState{Unavailable: true, NextRetryAfter: forcedUntil, ForcedCooldownUntil: forcedUntil}
	ordinary := &ModelState{Unavailable: true, NextRetryAfter: ordinaryUntil, UpdatedAt: now}
	for _, reversed := range []bool{false, true} {
		target, source := forced.Clone(), ordinary.Clone()
		if reversed {
			target, source = source, target
		}
		merged := mergeModelState(target, source)
		if !merged.NextRetryAfter.Equal(ordinaryUntil) || !merged.ForcedCooldownUntil.Equal(forcedUntil) {
			t.Fatalf("merge conflated ordinary and forced deadlines: %+v", merged)
		}
	}
	a := CooldownStateRecord{NextRetryAfter: ordinaryUntil, ForcedCooldownUntil: forcedUntil}
	b := a
	b.ForcedCooldownUntil = forcedUntil.Add(time.Minute)
	if cooldownStateRecordEqual(a, b) {
		t.Fatal("forced-only deadline change would not trigger persistence")
	}
}

func TestManager_ForcedCooldownLegacyAuthWithModelHistory(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	SetQuotaCooldownDisabled(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousDisabled) })
	for _, tc := range []struct {
		name            string
		modelRecords    bool
		aggregate       bool
		credential429   bool
		sameDeadline    bool
		differentSource bool
	}{
		{name: "clean_history"},
		{name: "unrelated_model_sidecar", modelRecords: true},
		{name: "same_deadline_different_error", modelRecords: true, sameDeadline: true},
		{name: "matching_model_aggregate", modelRecords: true, aggregate: true},
		{name: "aggregate_deadline_and_error_from_different_models", modelRecords: true, aggregate: true, differentSource: true},
		{name: "credential_quota", modelRecords: true, aggregate: true, credential429: true},
	} {
		for _, successModel := range []string{"", "history-model"} {
			t.Run(tc.name+"/success_model="+successModel, func(t *testing.T) {
				ctx := context.Background()
				m, auth := newCooldownMonotonicManager(t, "history-model", "sidecar-model", "earlier-model")
				// Real model history exists before loading the auth-level sidecar.
				m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: "history-model", Success: true})
				store := NewFileCooldownStateStore(t.TempDir())
				m.SetCooldownStateStore(store)
				deadline := time.Now().Add(10 * time.Minute)
				failure := &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusBadGateway, Message: "legacy forced cooldown"}
				authRecord := CooldownStateRecord{AuthID: auth.ID, Provider: auth.Provider, NextRetryAfter: deadline, LastError: failure}
				if tc.credential429 {
					authRecord.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: deadline}
				}
				records := []CooldownStateRecord{authRecord}
				if tc.modelRecords {
					modelRecord := authRecord
					modelRecord.Model = "sidecar-model"
					if !tc.aggregate {
						if !tc.sameDeadline {
							modelRecord.NextRetryAfter = deadline.Add(time.Hour)
						}
						modelRecord.LastError = &Error{HTTPStatus: http.StatusNotFound, Message: "unrelated model failure"}
					}
					if tc.differentSource {
						modelRecord.NextRetryAfter = deadline.Add(time.Hour)
						earlier := authRecord
						earlier.Model = "earlier-model"
						earlier.LastError = &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusServiceUnavailable, Message: "earlier model failure"}
						records = append(records, earlier)
					}
					records = append(records, modelRecord)
				}
				if errSave := store.Save(ctx, records); errSave != nil {
					t.Fatal(errSave)
				}
				if errRestore := m.RestoreCooldownStates(ctx); errRestore != nil {
					t.Fatal(errRestore)
				}
				snapshot, _ := m.GetByID(auth.ID)
				wantForced := !tc.aggregate || tc.credential429
				if wantForced && !snapshot.ForcedCooldownUntil.Equal(deadline) {
					t.Errorf("model history suppressed genuine legacy auth cooldown: got=%v want=%v", snapshot.ForcedCooldownUntil, deadline)
				} else if !wantForced && !snapshot.ForcedCooldownUntil.IsZero() {
					t.Errorf("model aggregate became auth-level forced cooldown: %v", snapshot.ForcedCooldownUntil)
				}
				if blocked, _, _ := isAuthBlockedForModel(snapshot, "history-model", time.Now()); blocked != wantForced {
					t.Errorf("history model blocked=%v, want=%v", blocked, wantForced)
				}
				if wantForced && !isCredentialBlocked(snapshot, 3, time.Now()) {
					t.Error("scheduler did not recognize the credential-wide forced cooldown")
				}
				m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: successModel, Success: true})
				snapshot, _ = m.GetByID(auth.ID)
				if wantForced {
					if !snapshot.ForcedCooldownUntil.Equal(deadline) || !snapshot.Unavailable || snapshot.NextRetryAfter.Before(deadline) {
						t.Errorf("success cleared restored auth cooldown: forced=%v next=%v unavailable=%v", snapshot.ForcedCooldownUntil, snapshot.NextRetryAfter, snapshot.Unavailable)
					}
					if picked, errPick := m.scheduler.pickSingle(ctx, auth.Provider, "history-model", cliproxyexecutor.Options{}, nil); errPick == nil || picked != nil {
						t.Error("scheduler selected a credential with an active auth-level forced cooldown")
					}
				}
			})
		}
	}
}

func TestManager_ForcedCooldown429RetryAfterBoundaries(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	SetQuotaCooldownDisabled(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousDisabled) })
	zero, negative, longer := time.Duration(0), -time.Minute, 3*time.Hour
	for _, model := range []string{"quota-window-model", ""} {
		for _, tc := range []struct {
			name  string
			retry *time.Duration
			want  time.Duration
		}{
			{name: "zero_floor", retry: &zero, want: minQuotaCooldownFloor},
			{name: "negative_floor", retry: &negative, want: minQuotaCooldownFloor},
			{name: "absent_retry_after", want: quotaBackoffBase * (1 << 6)},
			{name: "longer_explicit_window", retry: &longer, want: longer},
		} {
			t.Run("model="+model+"/"+tc.name, func(t *testing.T) {
				ctx := context.Background()
				m, auth := newCooldownMonotonicManager(t, "quota-window-model")
				ordinaryRetry := 2 * time.Hour
				m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, RetryAfter: &ordinaryRetry, Error: &Error{HTTPStatus: http.StatusTooManyRequests}})
				ordinaryUntil := forcedCooldownTestState(t, m, auth.ID, model).NextRetryAfter
				// Use an established backoff level so the no-header window remains
				// comfortably live without relying on sub-second test timing.
				m.mu.Lock()
				if model == "" {
					m.auths[auth.ID].Quota.BackoffLevel = 6
				} else {
					m.auths[auth.ID].ModelStates[model].Quota.BackoffLevel = 6
				}
				m.mu.Unlock()
				forced := Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, RetryAfter: tc.retry, Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests}}
				before := time.Now()
				m.MarkResult(ctx, forced)
				state := forcedCooldownTestState(t, m, auth.ID, model)
				deadline := state.ForcedCooldownUntil
				if deadline.Before(before.Add(tc.want)) || deadline.After(time.Now().Add(tc.want)) {
					t.Fatalf("forced 429 deadline = %v, want its own %v window", deadline.Sub(before), tc.want)
				}
				if tc.want < ordinaryRetry && (!state.NextRetryAfter.Equal(ordinaryUntil) || !state.Quota.NextRecoverAt.Equal(ordinaryUntil)) {
					t.Fatalf("ordinary quota window shortened before success: %+v", state)
				}
				if tc.retry == nil {
					m.MarkResult(ctx, forced)
					state = forcedCooldownTestState(t, m, auth.ID, model)
					if !state.ForcedCooldownUntil.Equal(deadline) || state.Quota.BackoffLevel != 6 {
						t.Fatalf("in-flight no-header failure escalated the active window: %+v", state)
					}
				}
				m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, Success: true})
				state = forcedCooldownTestState(t, m, auth.ID, model)
				if !state.Unavailable || !state.NextRetryAfter.Equal(deadline) || state.Quota.Exceeded || !state.Quota.NextRecoverAt.IsZero() {
					t.Fatalf("success retained more than the forced 429 window: %+v", state)
				}
			})
		}
	}
}
