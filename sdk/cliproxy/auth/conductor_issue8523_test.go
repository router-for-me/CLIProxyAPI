package auth

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestReenableDisabledAuthClearsUnavailableAndRestoresRouting_Issue8523(t *testing.T) {
	ctx := context.Background()
	reg := registry.GetGlobalRegistry()
	model := "issue8523-gpt-6-luna"
	highID := "issue8523-codex-high.json"
	lowID := "issue8523-codex-low.json"

	t.Cleanup(func() {
		reg.UnregisterClient(highID)
		reg.UnregisterClient(lowID)
	})

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(&issue7756DummyExecutor{id: "codex"})
	now := time.Now().UTC()
	validExp := now.Add(time.Hour).Format(time.RFC3339)

	highAuth := &Auth{
		ID:       highID,
		FileName: highID,
		Provider: "codex",
		Status:   StatusActive,
		Attributes: map[string]string{
			"priority": "8",
		},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "valid-high-token",
			"expired":      validExp,
		},
	}
	lowAuth := &Auth{
		ID:       lowID,
		FileName: lowID,
		Provider: "codex",
		Status:   StatusActive,
		Attributes: map[string]string{
			"priority": "0",
		},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "valid-low-token",
			"expired":      validExp,
		},
	}

	if _, errReg := manager.Register(ctx, highAuth); errReg != nil {
		t.Fatalf("Register(high) error = %v", errReg)
	}
	if _, errReg := manager.Register(ctx, lowAuth); errReg != nil {
		t.Fatalf("Register(low) error = %v", errReg)
	}
	reg.RegisterClient(highID, "codex", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(lowID, "codex", []*registry.ModelInfo{{ID: model}})
	manager.RefreshSchedulerEntry(highID)
	manager.RefreshSchedulerEntry(lowID)

	// Record a successful execution so ModelStates is populated while Failed stays 0.
	manager.MarkResult(ctx, Result{
		AuthID:   highID,
		Provider: "codex",
		Model:    model,
		Success:  true,
	})

	// Disable high-priority credential and unregister its models, then reconcile.
	if _, committed, errMod := manager.Modify(ctx, highID, 0, func(current *Auth) error {
		current.Disabled = true
		current.Status = StatusDisabled
		current.StatusMessage = "disabled via management API"
		return nil
	}); errMod != nil || !committed {
		t.Fatalf("Modify(disable) committed=%v err=%v", committed, errMod)
	}
	reg.UnregisterClient(highID)
	manager.ReconcileRegistryModelStates(ctx, highID)

	disabledSnap, okDisabled := manager.GetByID(highID)
	if !okDisabled || disabledSnap == nil {
		t.Fatal("high auth missing after disable")
	}
	if !disabledSnap.Disabled || !disabledSnap.Unavailable || len(disabledSnap.ModelStates) != 0 {
		t.Fatalf("unexpected disabled precondition: disabled=%v unavailable=%v modelStates=%d",
			disabledSnap.Disabled, disabledSnap.Unavailable, len(disabledSnap.ModelStates))
	}

	// Re-enable via Modify without manually touching Unavailable (mirrors Management API).
	if _, committed, errMod := manager.Modify(ctx, highID, 0, func(current *Auth) error {
		current.Disabled = false
		current.Status = StatusActive
		current.StatusMessage = ""
		return nil
	}); errMod != nil || !committed {
		t.Fatalf("Modify(re-enable) committed=%v err=%v", committed, errMod)
	}
	reg.RegisterClient(highID, "codex", []*registry.ModelInfo{{ID: model}})
	manager.ReconcileRegistryModelStates(ctx, highID)
	manager.RefreshSchedulerEntry(highID)

	got, ok := manager.GetByID(highID)
	if !ok || got == nil {
		t.Fatal("high auth missing after re-enable")
	}
	if got.Unavailable {
		t.Fatalf("re-enabled auth stuck with Unavailable=true (status=%q nextRetry=%v quota=%+v)",
			got.Status, got.NextRetryAfter, got.Quota)
	}
	if got.Status != StatusActive {
		t.Fatalf("re-enabled auth Status = %q, want %q", got.Status, StatusActive)
	}
	if !IsAuthAvailableForModel(got, model, time.Now()) {
		t.Fatalf("IsAuthAvailableForModel = false after re-enable, want true")
	}

	picked, _, errPick := manager.pickNext(ctx, "codex", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext error = %v", errPick)
	}
	if picked == nil || picked.ID != highID {
		pickedID := ""
		if picked != nil {
			pickedID = picked.ID
		}
		t.Fatalf("pickNext selected %q, want re-enabled high-priority auth %q", pickedID, highID)
	}
}

func TestReenableDisabledAuthWithLateInFlightSuccessClearsUnavailable_Issue8523(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	authID := "issue8523-late-success.json"
	model := "issue8523-model"
	validExp := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)

	if _, errReg := manager.Register(ctx, &Auth{
		ID:       authID,
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "valid-token",
			"expired":      validExp,
		},
	}); errReg != nil {
		t.Fatalf("Register error = %v", errReg)
	}

	// Disable credential, then receive a late in-flight success while disabled.
	if _, _, errMod := manager.Modify(ctx, authID, 0, func(current *Auth) error {
		current.Disabled = true
		current.Status = StatusDisabled
		return nil
	}); errMod != nil {
		t.Fatalf("Modify(disable) error = %v", errMod)
	}
	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "codex",
		Model:    model,
		Success:  true,
	})

	// Re-enable credential.
	if _, _, errMod := manager.Modify(ctx, authID, 0, func(current *Auth) error {
		current.Disabled = false
		current.Status = StatusActive
		current.StatusMessage = ""
		return nil
	}); errMod != nil {
		t.Fatalf("Modify(re-enable) error = %v", errMod)
	}

	got, _ := manager.GetByID(authID)
	if got.Unavailable {
		t.Fatalf("auth.Unavailable = true after re-enable following late disabled success, want false")
	}
	if !IsAuthAvailableForModel(got, model, time.Now()) {
		t.Fatalf("IsAuthAvailableForModel = false, want true")
	}
}

func TestReenableDisabledAuthWithRequiresSignInAndExpiredTokenStaysError_Issue8523(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	authID := "issue8523-requires-signin.json"
	expiredAt := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)

	if _, errReg := manager.Register(ctx, &Auth{
		ID:             authID,
		Provider:       "codex",
		Disabled:       true,
		Status:         StatusDisabled,
		Unavailable:    true,
		RequiresSignIn: true,
		RefreshError:   &Error{Code: "invalid_grant", Message: "refresh token invalid", HTTPStatus: http.StatusUnauthorized},
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  "expired-token",
			"refresh_token": "rejected-refresh",
			"expired":       expiredAt,
		},
		RejectedRefreshToken: "rejected-refresh",
	}); errReg != nil {
		t.Fatalf("Register error = %v", errReg)
	}

	// Re-enable without replacing tokens: must NOT become status=active + unavailable=true or falsely available.
	if _, _, errMod := manager.Modify(ctx, authID, 0, func(current *Auth) error {
		current.Disabled = false
		current.Status = StatusActive
		current.StatusMessage = ""
		return nil
	}); errMod != nil {
		t.Fatalf("Modify(re-enable) error = %v", errMod)
	}

	got, _ := manager.GetByID(authID)
	if !got.Unavailable {
		t.Fatal("auth.Unavailable = false, want true when RequiresSignIn and token is expired")
	}
	if got.Status != StatusError {
		t.Fatalf("auth.Status = %q, want %q (must not expose status=active while unavailable)", got.Status, StatusError)
	}
	if got.StatusMessage == "" {
		t.Fatal("auth.StatusMessage is empty, want explicit error reason")
	}
}

func TestFileSnapshotReloadHealsOrphanedUnavailableWithoutCooldown_Issue8523(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	authID := "issue8523-file-reload.json"
	validExp := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, authID)
	raw := []byte("{\"type\":\"codex\",\"access_token\":\"valid-token\",\"expired\":\"" + validExp + "\",\"priority\":2}")
	if errWrite := os.WriteFile(filePath, raw, 0o600); errWrite != nil {
		t.Fatalf("WriteFile error = %v", errWrite)
	}

	if _, errReg := manager.Register(ctx, &Auth{
		ID:          authID,
		Provider:    "codex",
		Status:      StatusActive,
		Unavailable: true, // Orphaned unavailable with zero deadlines and active status.
		Attributes: map[string]string{
			AttributePath: filePath,
			"priority":    "0",
		},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "valid-token",
			"expired":      validExp,
			"priority":     0,
		},
	}); errReg != nil {
		t.Fatalf("Register error = %v", errReg)
	}

	reloaded := &Auth{
		ID:       authID,
		Provider: "codex",
		Status:   StatusActive,
		Attributes: map[string]string{
			AttributePath: filePath,
			"priority":    "2",
		},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "valid-token",
			"expired":      validExp,
			"priority":     2,
		},
	}
	reloaded.BindFileSnapshot(filePath, raw)

	updated, errUpdate := manager.Update(ctx, reloaded)
	if errUpdate != nil {
		t.Fatalf("Update error = %v", errUpdate)
	}
	if updated.Unavailable {
		t.Fatalf("file reload preserved orphaned Unavailable=true with zero cooldown deadline")
	}
	if !IsAuthAvailableForModel(updated, "gpt-6-luna", time.Now()) {
		t.Fatalf("IsAuthAvailableForModel = false after file reload, want true")
	}

	// Negative case: an active timed cooldown must still be preserved across a priority edit.
	activeRetry := time.Now().Add(10 * time.Minute)
	if _, _, errMod := manager.Modify(ctx, authID, 0, func(current *Auth) error {
		current.Unavailable = true
		current.Status = StatusError
		current.StatusMessage = "quota exhausted"
		current.NextRetryAfter = activeRetry
		current.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: activeRetry}
		return nil
	}); errMod != nil {
		t.Fatalf("Modify(cooldown) error = %v", errMod)
	}

	reloaded2 := reloaded.Clone()
	reloaded2.Attributes["priority"] = "3"
	reloaded2.Metadata["priority"] = 3
	reloaded2.BindFileSnapshot(filePath, raw)
	updated2, errUpdate2 := manager.Update(ctx, reloaded2)
	if errUpdate2 != nil {
		t.Fatalf("Update(with active cooldown) error = %v", errUpdate2)
	}
	if !updated2.Unavailable || !updated2.NextRetryAfter.After(time.Now()) {
		t.Fatalf("active cooldown was lost on file reload: unavailable=%v nextRetry=%v", updated2.Unavailable, updated2.NextRetryAfter)
	}
}

func TestMergeRefreshedAuthConcurrentReenableClearsUnavailable_Issue8523(t *testing.T) {
	validExp := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	base := &Auth{
		ID:          "issue8523-merge",
		Provider:    "codex",
		Disabled:    true,
		Status:      StatusDisabled,
		Unavailable: true,
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "old-token",
			"expired":      validExp,
			"disabled":     true,
		},
	}
	// User concurrently re-enabled before refresh committed, carrying Unavailable=true.
	current := base.Clone()
	current.Disabled = false
	current.Status = StatusActive
	current.StatusMessage = ""
	current.Metadata["disabled"] = false

	// Refresh succeeded against disabled base (retaining StatusDisabled).
	updated := base.Clone()
	updated.Metadata["access_token"] = "new-token"

	merged := MergeRefreshedAuth(base, current, updated)
	if merged == nil {
		t.Fatal("MergeRefreshedAuth returned nil")
	}
	if merged.Disabled || merged.Status != StatusActive {
		t.Fatalf("merged disabled=%v status=%q, want false / %q", merged.Disabled, merged.Status, StatusActive)
	}
	if merged.Unavailable {
		t.Fatalf("merged.Unavailable = true after concurrent re-enable during refresh, want false")
	}
}

func TestOrphanedUnavailableIsRoutableWithoutResetOrEdit_Issue8523(t *testing.T) {
	ctx := context.Background()
	reg := registry.GetGlobalRegistry()
	model := "issue8523-orphaned-direct-gpt-6-luna"
	highID := "issue8523-orphaned-high.json"
	lowID := "issue8523-orphaned-low.json"
	t.Cleanup(func() {
		reg.UnregisterClient(highID)
		reg.UnregisterClient(lowID)
	})

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(&issue7756DummyExecutor{id: "codex"})
	now := time.Now().UTC()
	validExp := now.Add(time.Hour).Format(time.RFC3339)

	// Register high-priority auth directly in the contradictory stuck state:
	// enabled, StatusActive, empty StatusMessage, nil LastError, empty ModelStates,
	// zero NextRetryAfter, Quota.Exceeded=false, valid token, but Unavailable=true.
	if _, errReg := manager.Register(ctx, &Auth{
		ID:          highID,
		FileName:    highID,
		Provider:    "codex",
		Status:      StatusActive,
		Unavailable: true,
		Attributes:  map[string]string{"priority": "8"},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "valid-high-token",
			"expired":      validExp,
		},
	}); errReg != nil {
		t.Fatalf("Register(high) error = %v", errReg)
	}
	if _, errReg := manager.Register(ctx, &Auth{
		ID:         lowID,
		FileName:   lowID,
		Provider:   "codex",
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "0"},
		Metadata: map[string]any{
			"type":         "codex",
			"access_token": "valid-low-token",
			"expired":      validExp,
		},
	}); errReg != nil {
		t.Fatalf("Register(low) error = %v", errReg)
	}
	reg.RegisterClient(highID, "codex", []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(lowID, "codex", []*registry.ModelInfo{{ID: model}})
	manager.RefreshSchedulerEntry(highID)
	manager.RefreshSchedulerEntry(lowID)

	highSnap, _ := manager.GetByID(highID)
	if !IsAuthAvailableForModel(highSnap, model, time.Now()) {
		t.Fatal("IsAuthAvailableForModel = false for orphaned active auth without reset/edit, want true")
	}
	proj := manager.clientModelProjectionForAuth(highSnap, model, time.Now())
	if proj.Suspended {
		t.Fatalf("clientModelProjectionForAuth suspended orphaned active auth: %+v", proj)
	}

	// Without editing files, toggling disabled, or calling ResetQuota, routing must select highID.
	picked, _, errPick := manager.pickNext(ctx, "codex", model, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext error = %v", errPick)
	}
	if picked == nil || picked.ID != highID {
		pickedID := ""
		if picked != nil {
			pickedID = picked.ID
		}
		t.Fatalf("pickNext selected %q, want %q without requiring file edit or cooldown reset", pickedID, highID)
	}
}

type issue8523BlockingStore struct {
	mu          sync.Mutex
	blockNext   bool
	saveStarted chan struct{}
	releaseSave chan struct{}
}

func (s *issue8523BlockingStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *issue8523BlockingStore) Save(_ context.Context, auth *Auth) (string, error) {
	s.mu.Lock()
	shouldBlock := s.blockNext
	if shouldBlock {
		s.blockNext = false
	}
	s.mu.Unlock()
	if shouldBlock {
		close(s.saveStarted)
		<-s.releaseSave
	}
	if auth == nil {
		return "", nil
	}
	return auth.ID, nil
}

func (s *issue8523BlockingStore) Delete(context.Context, string) error { return nil }

type issue8523FailingRefreshExecutor struct {
	issue7756DummyExecutor
	err error
}

func (e *issue8523FailingRefreshExecutor) Refresh(context.Context, *Auth) (*Auth, error) {
	return nil, e.err
}

func TestReenableNotOverwrittenByConcurrentDisabledRefreshFailureDuringPersist_Issue8523(t *testing.T) {
	ctx := context.Background()
	store := &issue8523BlockingStore{
		saveStarted: make(chan struct{}),
		releaseSave: make(chan struct{}),
	}
	manager := NewManager(store, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(&issue8523FailingRefreshExecutor{
		issue7756DummyExecutor: issue7756DummyExecutor{id: "codex"},
		err:                    &Error{Code: "upstream_503", Message: "temporary refresh failure", HTTPStatus: http.StatusServiceUnavailable},
	})

	authID := "issue8523-persist-race.json"
	validExp := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)

	// Start with disabled=true, unavailable=false so a concurrent refresh failure
	// during persistLocked flips existing.Unavailable false->true while existing.Status stays disabled.
	if _, errReg := manager.Register(ctx, &Auth{
		ID:          authID,
		Provider:    "codex",
		Disabled:    true,
		Status:      StatusDisabled,
		Unavailable: false,
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  "valid-token",
			"refresh_token": "refresh-tok",
			"expired":       validExp,
			"disabled":      true,
		},
	}); errReg != nil {
		t.Fatalf("Register error = %v", errReg)
	}

	store.mu.Lock()
	store.blockNext = true
	store.mu.Unlock()

	modifyDone := make(chan error, 1)
	go func() {
		_, _, errMod := manager.Modify(ctx, authID, 0, func(current *Auth) error {
			current.Disabled = false
			current.Status = StatusActive
			current.StatusMessage = ""
			current.Unavailable = false
			current.LastError = nil
			current.Metadata["disabled"] = false
			return nil
		})
		modifyDone <- errMod
	}()

	// Wait until Modify has entered persistLocked and released m.mu.
	<-store.saveStarted

	// Run a transient refresh failure while existing in m.auths is still disabled.
	manager.refreshAuth(ctx, authID)

	// Verify existing in m.auths was flipped to Unavailable=true while still disabled.
	midSnap, _ := manager.GetByID(authID)
	if !midSnap.Disabled || !midSnap.Unavailable {
		t.Fatalf("expected concurrent refresh to mark still-disabled auth Unavailable=true, got disabled=%v unavailable=%v",
			midSnap.Disabled, midSnap.Unavailable)
	}

	// Unblock Store.Save so Modify runs post-save mergeAuthSaveDelta and publishes.
	close(store.releaseSave)
	if errMod := <-modifyDone; errMod != nil {
		t.Fatalf("Modify(re-enable) error = %v", errMod)
	}

	got, _ := manager.GetByID(authID)
	if got.Disabled || got.Status != StatusActive {
		t.Fatalf("final auth disabled=%v status=%q, want false / %q", got.Disabled, got.Status, StatusActive)
	}
	if got.Unavailable {
		t.Fatalf("post-save delta merge re-introduced Unavailable=true on re-enabled active auth (lastErr=%+v)", got.LastError)
	}
	if !IsAuthAvailableForModel(got, "gpt-6-luna", time.Now()) {
		t.Fatal("IsAuthAvailableForModel = false after re-enable race, want true")
	}
}

func TestReenableDisabledSIWCUnknownDiagnostic401PreservesOuterRouting_Issue8523(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	authID := "issue8523-siwc-diagnostic-401.json"
	now := time.Now().UTC()
	validOuterExp := now.Add(time.Hour).Format(time.RFC3339)
	expiredSIWCExp := now.Add(-time.Hour).Format(time.RFC3339)

	// Disabled Codex SIWC auth whose disabled-era refresh left both
	// RefreshError.Code=refresh_outcome_unknown and a diagnostic HTTP 401 LastError.
	if _, errReg := manager.Register(ctx, &Auth{
		ID:                       authID,
		Provider:                 "codex",
		Disabled:                 true,
		Status:                   StatusDisabled,
		Unavailable:              true,
		RequiresSignIn:           true,
		RejectedSIWCRefreshToken: "siwc-refresh",
		RefreshError: &Error{
			Code:       "refresh_outcome_unknown",
			Message:    "siwc refresh transport diagnostic",
			HTTPStatus: http.StatusUnauthorized,
		},
		LastError: &Error{
			Code:       "unauthorized",
			Message:    "siwc refresh transport diagnostic",
			HTTPStatus: http.StatusUnauthorized,
		},
		Metadata: map[string]any{
			"type":          "codex",
			"access_token":  "outer-valid-token",
			"refresh_token": "outer-refresh",
			"expired":       validOuterExp,
			"use_siwc":      true,
			"disabled":      true,
			"siwc": map[string]any{
				"access_token":  "siwc-expired-token",
				"refresh_token": "siwc-refresh",
				"client_id":     "siwc-client",
				"expires_at":    expiredSIWCExp,
			},
		},
	}); errReg != nil {
		t.Fatalf("Register error = %v", errReg)
	}

	// Re-enable via Modify (mirrors Management API re-enable).
	if _, _, errMod := manager.Modify(ctx, authID, 0, func(current *Auth) error {
		current.Disabled = false
		current.Status = StatusActive
		current.StatusMessage = ""
		current.Unavailable = false
		current.LastError = nil
		current.Metadata["disabled"] = false
		return nil
	}); errMod != nil {
		t.Fatalf("Modify(re-enable) error = %v", errMod)
	}

	got, _ := manager.GetByID(authID)
	if !got.Unavailable || got.Status != StatusError {
		t.Fatalf("re-enabled SIWC auth = (unavailable=%v, status=%q), want (true, error)", got.Unavailable, got.Status)
	}
	if got.LastError == nil || got.LastError.Code != "refresh_outcome_unknown" || got.LastError.HTTPStatus == http.StatusUnauthorized {
		t.Fatalf("re-enabled SIWC auth escalated diagnostic 401: LastError=%+v", got.LastError)
	}
	if hasUnauthorizedAuthFailure(got) {
		t.Fatal("hasUnauthorizedAuthFailure = true, want false so valid outer token remains routable")
	}

	// SIWC requests (default headers) must stay blocked because SIWC token is expired.
	if blocked, _, _ := isAuthBlockedForModelForRequest(ctx, got, "gpt-6-luna", now); !blocked {
		t.Fatal("SIWC request was not blocked while SIWC token is expired")
	}
	// Native Codex requests (using healthy outer token) must remain routable.
	nativeHeaders := http.Header{"User-Agent": {"codex_cli_rs/1.0.0"}}
	if blocked, _, _ := isAuthBlockedForModelForRequest(ctx, got, "gpt-6-luna", now, nativeHeaders); blocked {
		t.Fatal("native Codex request with valid outer token was blocked after re-enable")
	}
}

func TestOrphanedUnavailablePredicateRejectsExpiredTokenAndFutureQuotaRecoverAt_Issue8523(t *testing.T) {
	now := time.Now().UTC()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	model := "issue8523-negative-guard-model"

	t.Run("expired access token without RequiresSignIn stays unavailable", func(t *testing.T) {
		expiredAuth := &Auth{
			ID:          "issue8523-expired-no-signin.json",
			Provider:    "codex",
			Status:      StatusActive,
			Unavailable: true,
			Metadata: map[string]any{
				"type":         "codex",
				"access_token": "expired-token",
				"expired":      now.Add(-time.Minute).Format(time.RFC3339),
			},
		}
		if IsOrphanedAuthUnavailable(expiredAuth, now) {
			t.Fatal("IsOrphanedAuthUnavailable = true for expired access token, want false")
		}
		if IsAuthAvailableForModel(expiredAuth, model, now) {
			t.Fatal("IsAuthAvailableForModel = true for expired access token, want false")
		}
		if proj := manager.clientModelProjectionForAuth(expiredAuth, model, now); !proj.Suspended {
			t.Fatalf("clientModelProjectionForAuth.Suspended = false for expired access token, want true (%+v)", proj)
		}
	})

	t.Run("future Quota.NextRecoverAt without Quota.Exceeded stays unavailable", func(t *testing.T) {
		recoverAt := now.Add(10 * time.Minute)
		recoveringAuth := &Auth{
			ID:          "issue8523-future-recover-at.json",
			Provider:    "codex",
			Status:      StatusActive,
			Unavailable: true,
			Quota: QuotaState{
				Exceeded:      false,
				NextRecoverAt: recoverAt,
			},
			Metadata: map[string]any{
				"type":         "codex",
				"access_token": "valid-token",
				"expired":      now.Add(time.Hour).Format(time.RFC3339),
			},
		}
		if IsOrphanedAuthUnavailable(recoveringAuth, now) {
			t.Fatal("IsOrphanedAuthUnavailable = true with future Quota.NextRecoverAt, want false")
		}
		if IsAuthAvailableForModel(recoveringAuth, model, now) {
			t.Fatal("IsAuthAvailableForModel = true with future Quota.NextRecoverAt, want false")
		}
		if proj := manager.clientModelProjectionForAuth(recoveringAuth, model, now); !proj.Suspended {
			t.Fatalf("clientModelProjectionForAuth.Suspended = false with future Quota.NextRecoverAt, want true (%+v)", proj)
		}
	})
}
