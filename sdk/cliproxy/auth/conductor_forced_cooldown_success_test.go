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
	if stateAfter == nil || !stateAfter.Unavailable || !stateAfter.NextRetryAfter.Equal(deadline) {
		t.Errorf("in-flight success cleared forced cooldown: %+v", stateAfter)
	}
	if after.Success != 1 || after.Failed != 1 {
		t.Errorf("success/failure accounting changed: success=%d failed=%d", after.Success, after.Failed)
	}
	records, errLoad := store.Load(ctx)
	found := false
	for _, record := range records {
		if record.Model == model && record.NextRetryAfter.Equal(deadline) && record.LastError != nil && record.LastError.Code == ErrorCodeForceCooldown {
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
					if model != "" {
						current.ModelStates[model].NextRetryAfter = current.NextRetryAfter
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
					if !unavailable || !next.Equal(deadline) || lastError == nil || lastError.Code != ErrorCodeForceCooldown {
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
