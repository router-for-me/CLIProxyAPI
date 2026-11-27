package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func registerIssue8553ClaudeModels(t *testing.T, authID string, models ...string) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	infos := make([]*registry.ModelInfo, 0, len(models))
	for _, id := range models {
		infos = append(infos, &registry.ModelInfo{ID: id, Type: "claude"})
	}
	reg.RegisterClient(authID, "claude", infos)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})
}

func TestVerifyInference_ReleasesActiveCredentialQuotaAndSyncsStoreAndScheduler_Issue8553(t *testing.T) {
	ctx := WithSkipPersist(context.Background())
	m := NewManager(nil, &RoundRobinSelector{}, nil)
	store := &recordingCooldownStateStore{}
	m.SetCooldownStateStore(store)
	exec := &issue7737VerifyExecutor{id: "claude"}
	m.RegisterExecutor(exec)

	authID := "claude:apikey:issue8553-release"
	registerIssue8553ClaudeModels(t, authID, "claude-test", "claude-sibling")
	if _, errReg := m.Register(ctx, &Auth{ID: authID, Provider: "claude"}); errReg != nil {
		t.Fatalf("Register: %v", errReg)
	}

	// Seed sibling model state so credential_quota propagates to it as well.
	m.MarkResult(ctx, Result{
		AuthID:            authID,
		Provider:          "claude",
		Model:             "claude-sibling",
		Success:           true,
		CompletedResponse: true,
		ExecutionSeq:      m.nextExecutionSeq(),
	})

	// Record a credential-scoped 429 with a 2h reset.
	retry := 2 * time.Hour
	m.MarkResult(ctx, Result{
		AuthID:          authID,
		Provider:        "claude",
		Model:           "claude-test",
		Success:         false,
		Error:           &Error{Code: "rate_limit_error", Message: "rate limited", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter:      &retry,
		CredentialScope: true,
		ExecutionSeq:    m.nextExecutionSeq(),
	})

	before, _ := m.GetByID(authID)
	if before.Quota.Reason != "credential_quota" || !before.Quota.Exceeded || !before.Unavailable {
		t.Fatalf("expected active credential_quota cooldown before verify, got %+v", before)
	}
	if len(store.savedRecords()) == 0 {
		t.Fatalf("expected cooldown store to persist active credential_quota record before verify")
	}

	// Operator-triggered verify receives a completed upstream response.
	updated, errVerify := m.VerifyInference(ctx, authID, "claude-test")
	if errVerify != nil {
		t.Fatalf("VerifyInference error = %v, want nil", errVerify)
	}
	if updated == nil || !updated.InferenceVerified {
		t.Fatalf("expected InferenceVerified=true after verify, got %+v", updated)
	}
	if updated.Quota.Exceeded || updated.Quota.Reason == "credential_quota" || updated.Unavailable || updated.Status != StatusActive {
		t.Fatalf("expected credential_quota cleared after verify, got unavailable=%v status=%v quota=%+v", updated.Unavailable, updated.Status, updated.Quota)
	}

	// Both the verified model and the purely-propagated sibling model must be pickable again.
	for _, model := range []string{"claude-test", "claude-sibling"} {
		picked, _, errPick := m.pickNext(ctx, "claude", model, cliproxyexecutor.Options{}, nil)
		if errPick != nil || picked == nil || picked.ID != authID {
			t.Fatalf("pickNext(%q) after verify = (%v, %v), want %q", model, picked, errPick, authID)
		}
	}

	// Cooldown store must have been updated to clear the released credential_quota record.
	if records := store.savedRecords(); len(records) != 0 {
		t.Fatalf("expected cooldown store cleared after verify release, got %+v", records)
	}
}

func TestVerifyInference_PreservesIndependentSiblingShadowAndForceCooldown_Issue8553(t *testing.T) {
	t.Run("preserves independent sibling model cooldown shadow", func(t *testing.T) {
		ctx := WithSkipPersist(context.Background())
		m := NewManager(nil, &RoundRobinSelector{}, nil)
		exec := &issue7737VerifyExecutor{id: "claude"}
		m.RegisterExecutor(exec)

		authID := "claude:apikey:issue8553-shadow"
		registerIssue8553ClaudeModels(t, authID, "claude-test", "claude-sibling")
		if _, errReg := m.Register(ctx, &Auth{ID: authID, Provider: "claude"}); errReg != nil {
			t.Fatalf("Register: %v", errReg)
		}

		// Give claude-sibling an independent model-scoped cooldown first.
		siblingRetry := 30 * time.Minute
		m.MarkResult(ctx, Result{
			AuthID:       authID,
			Provider:     "claude",
			Model:        "claude-sibling",
			Success:      false,
			Error:        &Error{Code: "rate_limit_error", Message: "model quota", HTTPStatus: http.StatusTooManyRequests},
			RetryAfter:   &siblingRetry,
			ExecutionSeq: m.nextExecutionSeq(),
		})

		// Then record a credential-scoped 429 on claude-test that wraps claude-sibling.
		credRetry := 2 * time.Hour
		m.MarkResult(ctx, Result{
			AuthID:          authID,
			Provider:        "claude",
			Model:           "claude-test",
			Success:         false,
			Error:           &Error{Code: "rate_limit_error", Message: "account quota", HTTPStatus: http.StatusTooManyRequests},
			RetryAfter:      &credRetry,
			CredentialScope: true,
			ExecutionSeq:    m.nextExecutionSeq(),
		})

		updated, errVerify := m.VerifyInference(ctx, authID, "claude-test")
		if errVerify != nil {
			t.Fatalf("VerifyInference error = %v, want nil", errVerify)
		}
		if !updated.InferenceVerified || updated.Quota.Reason == "credential_quota" {
			t.Fatalf("expected credential_quota released and InferenceVerified=true, got %+v", updated)
		}

		// Verified model is available again, while sibling retains its independent cooldown.
		if _, _, errPick := m.pickNext(ctx, "claude", "claude-test", cliproxyexecutor.Options{}, nil); errPick != nil {
			t.Fatalf("expected claude-test pickable after verify, got err=%v", errPick)
		}
		siblingState := existingModelState(updated, "claude-sibling")
		if siblingState == nil || !isModelStateActiveCooldown(siblingState, time.Now()) {
			t.Fatalf("expected claude-sibling independent cooldown preserved, got %+v", siblingState)
		}
		if siblingState.Quota.Reason == "credential_quota" {
			t.Fatalf("expected claude-sibling restored from shadow rather than credential_quota, got %+v", siblingState.Quota)
		}
	})

	t.Run("preserves independent auth-level force cooldown", func(t *testing.T) {
		ctx := WithSkipPersist(context.Background())
		m := NewManager(nil, &RoundRobinSelector{}, nil)
		exec := &issue7737VerifyExecutor{id: "claude"}
		m.RegisterExecutor(exec)

		authID := "claude:apikey:issue8553-force"
		registerIssue8553ClaudeModels(t, authID, "claude-test")
		if _, errReg := m.Register(ctx, &Auth{ID: authID, Provider: "claude"}); errReg != nil {
			t.Fatalf("Register: %v", errReg)
		}

		// Record an independent auth-level force cooldown first.
		forceRetry := 45 * time.Minute
		m.MarkResult(ctx, Result{
			AuthID:          authID,
			Provider:        "claude",
			Success:         false,
			Error:           &Error{Code: ErrorCodeForceCooldown, Message: "manual force cooldown", HTTPStatus: http.StatusServiceUnavailable},
			RetryAfter:      &forceRetry,
			CredentialScope: true,
			ExecutionSeq:    m.nextExecutionSeq(),
		})

		// Then record a credential_quota cooldown.
		credRetry := 2 * time.Hour
		m.MarkResult(ctx, Result{
			AuthID:          authID,
			Provider:        "claude",
			Model:           "claude-test",
			Success:         false,
			Error:           &Error{Code: "rate_limit_error", Message: "account quota", HTTPStatus: http.StatusTooManyRequests},
			RetryAfter:      &credRetry,
			CredentialScope: true,
			ExecutionSeq:    m.nextExecutionSeq(),
		})

		updated, errVerify := m.VerifyInference(ctx, authID, "claude-test")
		if errVerify != nil {
			t.Fatalf("VerifyInference error = %v, want nil", errVerify)
		}
		if !updated.InferenceVerified {
			t.Fatalf("expected InferenceVerified=true, got false")
		}
		if updated.Quota.Reason == "credential_quota" || updated.Quota.Exceeded {
			t.Fatalf("expected credential_quota cleared, got %+v", updated.Quota)
		}
		if !updated.Unavailable || !effectiveAuthForceUntil(updated).After(time.Now()) {
			t.Fatalf("expected independent auth force cooldown preserved, got unavailable=%v forceUntil=%v", updated.Unavailable, effectiveAuthForceUntil(updated))
		}
	})

	t.Run("preserves verified model independent force cooldown while reopening sibling model", func(t *testing.T) {
		ctx := WithSkipPersist(context.Background())
		m := NewManager(nil, &RoundRobinSelector{}, nil)
		exec := &issue7737VerifyExecutor{id: "claude"}
		m.RegisterExecutor(exec)

		authID := "claude:apikey:issue8553-verified-model-force"
		registerIssue8553ClaudeModels(t, authID, "claude-model-a", "claude-model-b")
		if _, errReg := m.Register(ctx, &Auth{ID: authID, Provider: "claude"}); errReg != nil {
			t.Fatalf("Register: %v", errReg)
		}

		// Model A has an independent model-level force cooldown.
		forceRetry := 45 * time.Minute
		m.MarkResult(ctx, Result{
			AuthID:       authID,
			Provider:     "claude",
			Model:        "claude-model-a",
			Success:      false,
			Error:        &Error{Code: ErrorCodeForceCooldown, Message: "model A force cooldown", HTTPStatus: http.StatusServiceUnavailable},
			RetryAfter:   &forceRetry,
			ExecutionSeq: m.nextExecutionSeq(),
		})

		// Model B triggers a credential-scoped 429 wrapping Model A.
		credRetry := 2 * time.Hour
		m.MarkResult(ctx, Result{
			AuthID:          authID,
			Provider:        "claude",
			Model:           "claude-model-b",
			Success:         false,
			Error:           &Error{Code: "rate_limit_error", Message: "account quota", HTTPStatus: http.StatusTooManyRequests},
			RetryAfter:      &credRetry,
			CredentialScope: true,
			ExecutionSeq:    m.nextExecutionSeq(),
		})

		// Operator verifies Model A: credential_quota is lifted, Model B becomes routable,
		// and Model A keeps its independent force cooldown.
		updated, errVerify := m.VerifyInference(ctx, authID, "claude-model-a")
		if errVerify != nil {
			t.Fatalf("VerifyInference error = %v, want nil", errVerify)
		}
		if !updated.InferenceVerified || updated.Quota.Exceeded || updated.Quota.Reason == "credential_quota" {
			t.Fatalf("expected credential_quota lifted and InferenceVerified=true, got %+v", updated)
		}

		// Model B must now be routable.
		pickedB, _, errPickB := m.pickNext(ctx, "claude", "claude-model-b", cliproxyexecutor.Options{}, nil)
		if errPickB != nil || pickedB == nil || pickedB.ID != authID {
			t.Fatalf("pickNext(claude-model-b) = (%v, %v), want %q", pickedB, errPickB, authID)
		}

		// Model A must still retain its independent force cooldown and remain unpickable.
		stateA := existingModelState(updated, "claude-model-a")
		if stateA == nil || !isModelStateActiveCooldown(stateA, time.Now()) || !stateA.forceCooldownUntil.After(time.Now()) {
			t.Fatalf("expected claude-model-a independent force cooldown preserved, got %+v", stateA)
		}
		if stateA.Quota.Reason == "credential_quota" {
			t.Fatalf("expected claude-model-a unwrapped from credential_quota, got %+v", stateA.Quota)
		}
		if pickedA, _, errPickA := m.pickNext(ctx, "claude", "claude-model-a", cliproxyexecutor.Options{}, nil); errPickA == nil || pickedA != nil {
			t.Fatalf("pickNext(claude-model-a) = (%v, %v), want cooldown error", pickedA, errPickA)
		}
	})
}

func TestVerifyInference_ClientSuccessKeepsCredentialQuota_Issue8553(t *testing.T) {
	ctx := WithSkipPersist(context.Background())
	m := NewManager(nil, &RoundRobinSelector{}, nil)
	authID := "claude:apikey:issue8553-client"
	if _, errReg := m.Register(ctx, &Auth{ID: authID, Provider: "claude"}); errReg != nil {
		t.Fatalf("Register: %v", errReg)
	}

	retry := 2 * time.Hour
	m.MarkResult(ctx, Result{
		AuthID:          authID,
		Provider:        "claude",
		Model:           "claude-test",
		Success:         false,
		Error:           &Error{Code: "rate_limit_error", Message: "rate limited", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter:      &retry,
		CredentialScope: true,
		ExecutionSeq:    m.nextExecutionSeq(),
	})

	// Ordinary client traffic success must NOT lift an active credential_quota cooldown.
	m.MarkResult(ctx, Result{
		AuthID:            authID,
		Provider:          "claude",
		Model:             "claude-test",
		Success:           true,
		CompletedResponse: true,
		ExecutionSeq:      m.nextExecutionSeq(),
	})

	got, _ := m.GetByID(authID)
	if got.Quota.Reason != "credential_quota" || !got.Quota.Exceeded || !got.Unavailable || got.InferenceVerified {
		t.Fatalf("ordinary client success altered active credential_quota: unavailable=%v verified=%v quota=%+v", got.Unavailable, got.InferenceVerified, got.Quota)
	}
}

func TestVerifyInference_RacingNewerCooldownOrDisableNotSuperseded_Issue8553(t *testing.T) {
	t.Run("same-model racing newer credential_quota keeps cooldown and returns verification_not_applied", func(t *testing.T) {
		ctx := WithSkipPersist(context.Background())
		m := NewManager(nil, &RoundRobinSelector{}, nil)
		exec := &issue7737VerifyExecutor{id: "claude"}
		m.RegisterExecutor(exec)

		authID := "claude:apikey:issue8553-race-same"
		if _, errReg := m.Register(ctx, &Auth{ID: authID, Provider: "claude"}); errReg != nil {
			t.Fatalf("Register: %v", errReg)
		}

		initialRetry := 2 * time.Hour
		m.MarkResult(ctx, Result{
			AuthID:          authID,
			Provider:        "claude",
			Model:           "claude-test",
			Success:         false,
			Error:           &Error{Code: "rate_limit_error", Message: "rate limited", HTTPStatus: http.StatusTooManyRequests},
			RetryAfter:      &initialRetry,
			CredentialScope: true,
			ExecutionSeq:    m.nextExecutionSeq(),
		})

		exec.onStream = func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			newerRetry := 3 * time.Hour
			m.MarkResult(ctx, Result{
				AuthID:          authID,
				Provider:        "claude",
				Model:           "claude-test",
				Success:         false,
				Error:           &Error{Code: "rate_limit_error", Message: "newer rate limit", HTTPStatus: http.StatusTooManyRequests},
				RetryAfter:      &newerRetry,
				CredentialScope: true,
				ExecutionSeq:    m.nextExecutionSeq(),
			})
			return &cliproxyexecutor.StreamResult{Chunks: issue7737CompletedStream()}, nil
		}

		updated, errVerify := m.VerifyInference(ctx, authID, "claude-test")
		var authErr *Error
		if !errors.As(errVerify, &authErr) || authErr.HTTPStatus != http.StatusConflict || authErr.Code != "verification_not_applied" {
			t.Fatalf("VerifyInference error = %v, want 409 verification_not_applied", errVerify)
		}
		if updated == nil || updated.InferenceVerified || updated.Quota.Reason != "credential_quota" || !updated.Quota.Exceeded {
			t.Fatalf("expected newer credential_quota preserved, got %+v", updated)
		}
	})

	t.Run("cross-model racing newer credential_quota for unseen verify model keeps cooldown", func(t *testing.T) {
		ctx := WithSkipPersist(context.Background())
		m := NewManager(nil, &RoundRobinSelector{}, nil)
		exec := &issue7737VerifyExecutor{id: "claude"}
		m.RegisterExecutor(exec)

		authID := "claude:apikey:issue8553-race-cross"
		if _, errReg := m.Register(ctx, &Auth{ID: authID, Provider: "claude"}); errReg != nil {
			t.Fatalf("Register: %v", errReg)
		}

		// Verify targets claude-model-a (not yet in auth.ModelStates) while a newer
		// credential_quota arrives on claude-model-b while verify is in flight.
		exec.onStream = func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			newerRetry := 3 * time.Hour
			m.MarkResult(ctx, Result{
				AuthID:          authID,
				Provider:        "claude",
				Model:           "claude-model-b",
				Success:         false,
				Error:           &Error{Code: "rate_limit_error", Message: "cross-model rate limit", HTTPStatus: http.StatusTooManyRequests},
				RetryAfter:      &newerRetry,
				CredentialScope: true,
				ExecutionSeq:    m.nextExecutionSeq(),
			})
			return &cliproxyexecutor.StreamResult{Chunks: issue7737CompletedStream()}, nil
		}

		updated, errVerify := m.VerifyInference(ctx, authID, "claude-model-a")
		var authErr *Error
		if !errors.As(errVerify, &authErr) || authErr.HTTPStatus != http.StatusConflict || authErr.Code != "verification_not_applied" {
			t.Fatalf("VerifyInference error = %v, want 409 verification_not_applied", errVerify)
		}
		if updated == nil || updated.InferenceVerified || updated.Quota.Reason != "credential_quota" || !updated.Quota.Exceeded || !updated.Unavailable {
			t.Fatalf("expected cross-model newer credential_quota preserved, got %+v", updated)
		}
	})

	t.Run("disabled during verify returns verification_not_applied instead of credential_superseded", func(t *testing.T) {
		ctx := WithSkipPersist(context.Background())
		m := NewManager(nil, &RoundRobinSelector{}, nil)
		exec := &issue7737VerifyExecutor{id: "claude"}
		m.RegisterExecutor(exec)

		authID := "claude:apikey:issue8553-disabled"
		if _, errReg := m.Register(ctx, &Auth{ID: authID, Provider: "claude"}); errReg != nil {
			t.Fatalf("Register: %v", errReg)
		}

		exec.onStream = func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			cur, _ := m.GetByID(authID)
			cur.Disabled = true
			cur.Status = StatusDisabled
			cur.Unavailable = true
			if _, errUpd := m.Update(ctx, cur); errUpd != nil {
				t.Fatalf("Update disable: %v", errUpd)
			}
			return &cliproxyexecutor.StreamResult{Chunks: issue7737CompletedStream()}, nil
		}

		updated, errVerify := m.VerifyInference(ctx, authID, "claude-test")
		var authErr *Error
		if !errors.As(errVerify, &authErr) || authErr.HTTPStatus != http.StatusConflict || authErr.Code != "verification_not_applied" {
			t.Fatalf("VerifyInference error = %v, want 409 verification_not_applied", errVerify)
		}
		if updated == nil || updated.InferenceVerified || !updated.Disabled || !updated.Unavailable {
			t.Fatalf("expected disabled credential preserved, got %+v", updated)
		}
	})

	t.Run("previously verified credential racing newer credential_quota returns verification_not_applied", func(t *testing.T) {
		ctx := WithSkipPersist(context.Background())
		m := NewManager(nil, &RoundRobinSelector{}, nil)
		exec := &issue7737VerifyExecutor{id: "claude"}
		m.RegisterExecutor(exec)

		authID := "claude:apikey:issue8553-prev-verified-race"
		if _, errReg := m.Register(ctx, &Auth{ID: authID, Provider: "claude"}); errReg != nil {
			t.Fatalf("Register: %v", errReg)
		}

		// Prior completed request already set InferenceVerified=true.
		m.MarkResult(ctx, Result{
			AuthID:            authID,
			Provider:          "claude",
			Model:             "claude-test",
			Success:           true,
			CompletedResponse: true,
			ExecutionSeq:      m.nextExecutionSeq(),
		})
		initialRetry := 2 * time.Hour
		m.MarkResult(ctx, Result{
			AuthID:          authID,
			Provider:        "claude",
			Model:           "claude-test",
			Success:         false,
			Error:           &Error{Code: "rate_limit_error", Message: "rate limited", HTTPStatus: http.StatusTooManyRequests},
			RetryAfter:      &initialRetry,
			CredentialScope: true,
			ExecutionSeq:    m.nextExecutionSeq(),
		})

		exec.onStream = func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			newerRetry := 3 * time.Hour
			m.MarkResult(ctx, Result{
				AuthID:          authID,
				Provider:        "claude",
				Model:           "claude-test",
				Success:         false,
				Error:           &Error{Code: "rate_limit_error", Message: "newer rate limit", HTTPStatus: http.StatusTooManyRequests},
				RetryAfter:      &newerRetry,
				CredentialScope: true,
				ExecutionSeq:    m.nextExecutionSeq(),
			})
			return &cliproxyexecutor.StreamResult{Chunks: issue7737CompletedStream()}, nil
		}

		updated, errVerify := m.VerifyInference(ctx, authID, "claude-test")
		var authErr *Error
		if !errors.As(errVerify, &authErr) || authErr.HTTPStatus != http.StatusConflict || authErr.Code != "verification_not_applied" {
			t.Fatalf("VerifyInference error = %v, want 409 verification_not_applied", errVerify)
		}
		if updated == nil || updated.Quota.Reason != "credential_quota" || !updated.Quota.Exceeded {
			t.Fatalf("expected newer credential_quota preserved, got %+v", updated)
		}
	})

	t.Run("superseded version with identical access_token keeps newer credential_quota untouched", func(t *testing.T) {
		ctx := WithSkipPersist(context.Background())
		m := NewManager(nil, &RoundRobinSelector{}, nil)
		exec := &issue7737VerifyExecutor{id: "claude"}
		m.RegisterExecutor(exec)

		authID := "claude:oauth:issue8553-superseded-same-access"
		if _, errReg := m.Register(ctx, &Auth{
			ID:       authID,
			Provider: "claude",
			Metadata: map[string]any{
				"access_token":  "shared-access-token",
				"refresh_token": "refresh-token-v1",
			},
		}); errReg != nil {
			t.Fatalf("Register: %v", errReg)
		}

		initialRetry := 2 * time.Hour
		m.MarkResult(ctx, Result{
			AuthID:          authID,
			Provider:        "claude",
			Model:           "claude-test",
			Success:         false,
			Error:           &Error{Code: "rate_limit_error", Message: "rate limited", HTTPStatus: http.StatusTooManyRequests},
			RetryAfter:      &initialRetry,
			CredentialScope: true,
			ExecutionSeq:    m.nextExecutionSeq(),
		})

		// While verify is in flight, an in-place Update rotates refresh_token while
		// keeping the same access_token (bumps CredentialVersion and preserves cooldown).
		exec.onStream = func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			cur, _ := m.GetByID(authID)
			next := cur.Clone()
			next.Metadata["refresh_token"] = "refresh-token-v2"
			if _, errUpd := m.Update(ctx, next); errUpd != nil {
				t.Fatalf("Update refresh_token: %v", errUpd)
			}
			return &cliproxyexecutor.StreamResult{Chunks: issue7737CompletedStream()}, nil
		}

		updated, errVerify := m.VerifyInference(ctx, authID, "claude-test")
		var authErr *Error
		if !errors.As(errVerify, &authErr) || authErr.HTTPStatus != http.StatusConflict || authErr.Code != "credential_superseded" {
			t.Fatalf("VerifyInference error = %v, want 409 credential_superseded", errVerify)
		}
		if updated == nil || updated.InferenceVerified || updated.Quota.Reason != "credential_quota" || !updated.Quota.Exceeded || !updated.Unavailable {
			t.Fatalf("superseded probe must leave newer version cooldown and InferenceVerified untouched, got verified=%v unavailable=%v quota=%+v",
				updated != nil && updated.InferenceVerified, updated != nil && updated.Unavailable, updated.Quota)
		}
	})
}
