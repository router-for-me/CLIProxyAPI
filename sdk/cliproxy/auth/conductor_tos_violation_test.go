package auth

import (
	"context"
	"net/http"
	"testing"
)

// TestMarkResult_TOSViolation_DisablesCredential verifies that a 403
// PERMISSION_DENIED carrying the tos_violation error code (produced by the
// Antigravity executor on the verbatim issue #7515 signature) deactivates the
// credential instead of applying the generic 30-minute 403 cooldown, while a
// non-TOS 403 keeps the existing cooldown behavior.
func TestMarkResult_TOSViolation_DisablesCredential(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()

	auth := &Auth{ID: "tos-cred", Provider: "antigravity", Status: StatusActive}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// The executor carries the appeal detail on the result error via
	// Error.Metadata; the conductor persists it onto the credential record.
	manager.MarkResult(ctx, Result{
		AuthID:          auth.ID,
		Provider:        "antigravity",
		Model:           "gemini-3-pro",
		Success:         false,
		CredentialScope: true,
		Error: &Error{
			HTTPStatus: http.StatusForbidden,
			Code:       "tos_violation",
			Message:    "TOS_VIOLATION",
			Metadata:   map[string]string{"tos_appeal_url": "https://forms.gle/hGzM9MEUv2azZsrb9", "tos_appeal_url_text": "Submit Appeal"},
		},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("auth not found after MarkResult")
	}
	if updated.Status != StatusDisabled {
		t.Fatalf("auth.Status = %q, want %q (terminal TOS suspension must deactivate)", updated.Status, StatusDisabled)
	}
	if !updated.Disabled {
		t.Fatal("auth.Disabled = false, want true (terminal state must persist via the file store)")
	}
	if !updated.Unavailable {
		t.Fatal("auth.Unavailable = false, want true")
	}
	if updated.StatusMessage != "tos_violation" {
		t.Fatalf("auth.StatusMessage = %q, want %q", updated.StatusMessage, "tos_violation")
	}
	if !updated.NextRetryAfter.IsZero() {
		t.Fatalf("auth.NextRetryAfter = %v, want zero (terminal, not a timed cooldown)", updated.NextRetryAfter)
	}
	if appeal, _ := updated.Metadata["tos_appeal_url"].(string); appeal != "https://forms.gle/hGzM9MEUv2azZsrb9" {
		t.Fatalf("auth.Metadata[tos_appeal_url] = %q, want appeal URL preserved", appeal)
	}
}

// TestMarkResult_NonTOS403_StillCools confirms the generic 403 path is
// unchanged: a plain PERMISSION_DENIED without the tos_violation code must
// continue to apply the 30-minute cooldown rather than deactivating.
func TestMarkResult_NonTOS403_StillCools(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()

	auth := &Auth{ID: "plain-403", Provider: "antigravity", Status: StatusActive}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	manager.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "antigravity",
		Model:    "gemini-3-pro",
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusForbidden, Message: "permission denied"},
	})

	updated, _ := manager.GetByID(auth.ID)
	if updated.Status == StatusDisabled {
		t.Fatal("plain 403 must not deactivate the credential")
	}
	if updated.NextRetryAfter.IsZero() {
		t.Fatal("plain 403 must keep the 30-minute cooldown")
	}
}

// TestMarkResult_TOSViolation_LateResultDoesNotRevive verifies that a
// success or ordinary failure recorded after the TOS suspension does not bring
// the disabled credential back into rotation.
func TestMarkResult_TOSViolation_LateResultDoesNotRevive(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	auth := &Auth{ID: "tos-late", Provider: "antigravity", Status: StatusActive}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	manager.MarkResult(ctx, Result{
		AuthID: auth.ID, Provider: "antigravity", Model: "gemini-3-pro",
		Success: false, CredentialScope: true,
		Error: &Error{HTTPStatus: http.StatusForbidden, Code: "tos_violation"},
	})
	// A stale success arriving after the suspension must not reactivate it.
	manager.MarkResult(ctx, Result{
		AuthID: auth.ID, Provider: "antigravity", Model: "gemini-3-pro", Success: true,
	})
	updated, _ := manager.GetByID(auth.ID)
	if updated.Status != StatusDisabled || !updated.Disabled {
		t.Fatalf("late success revived credential: status=%q disabled=%v", updated.Status, updated.Disabled)
	}
}

// TestMarkResult_TOSViolation_LateAuthLevelFailure verifies that an auth-level
// (model-less) failure arriving after the TOS suspension cannot overwrite the
// terminal state or arm a new cooldown.
func TestMarkResult_TOSViolation_LateAuthLevelFailure(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	auth := &Auth{ID: "tos-late-auth", Provider: "antigravity", Status: StatusActive}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	manager.MarkResult(ctx, Result{
		AuthID: auth.ID, Provider: "antigravity", Success: false, CredentialScope: true,
		Error: &Error{HTTPStatus: http.StatusForbidden, Code: "tos_violation"},
	})
	manager.MarkResult(ctx, Result{
		AuthID: auth.ID, Provider: "antigravity", Success: false,
		Error: &Error{HTTPStatus: http.StatusForbidden, Message: "plain 403"},
	})
	updated, _ := manager.GetByID(auth.ID)
	if updated.Status != StatusDisabled || !updated.Disabled {
		t.Fatalf("late auth-level failure overwrote terminal state: status=%q disabled=%v", updated.Status, updated.Disabled)
	}
	if updated.StatusMessage != "tos_violation" {
		t.Fatalf("StatusMessage = %q, want tos_violation preserved", updated.StatusMessage)
	}
}

// TestMarkResult_TOSViolation_NotDroppedByCredentialVersionBumps verifies that
// a TOS result dispatched before an OAuth token refresh is not discarded as
// stale when the refresh bumps CredentialVersion first. The suspension is
// service-scoped and applies to the account, not the token generation.
func TestMarkResult_TOSViolation_NotDroppedByCredentialVersionBumps(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	auth := &Auth{ID: "tos-stale", Provider: "antigravity", Status: StatusActive, CredentialVersion: 2}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	// A result recorded under credential version 1 (i.e. dispatched before a
	// refresh) still carries a valid terminal TOS signal.
	manager.MarkResult(ctx, Result{
		AuthID: auth.ID, Provider: "antigravity", Success: false, CredentialScope: true,
		CredentialVersion: 1,
		Error:             &Error{HTTPStatus: http.StatusForbidden, Code: "tos_violation", Metadata: map[string]string{"tos_appeal_url": "https://forms.gle/x"}},
	})
	updated, _ := manager.GetByID(auth.ID)
	if updated.Status != StatusDisabled || !updated.Disabled {
		t.Fatalf("TOS result dropped as stale after version bump: status=%q disabled=%v", updated.Status, updated.Disabled)
	}
	if got := updated.Metadata["tos_appeal_url"]; got != "https://forms.gle/x" {
		t.Fatalf("appeal metadata lost on stale-guarded path: %v", got)
	}
}

// TestMarkResult_TOSViolation_RejectedAfterCredentialReplacement verifies that
// a TOS result from a request issued under a previous credential is dropped as
// stale after the credential is genuinely replaced (refresh token swapped),
// rather than disabling the fresh credential.
func TestMarkResult_TOSViolation_RejectedAfterCredentialReplacement(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	auth := &Auth{
		ID: "tos-replaced", Provider: "antigravity", Status: StatusActive, CredentialVersion: 2,
		Metadata: map[string]any{"access_token": "new-access", "refresh_token": "new-refresh"},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	// Result dispatched under the OLD credential: old access token, old refresh
	// token, older credential version. Refresh token differs -> different account.
	manager.MarkResult(ctx, Result{
		AuthID: auth.ID, Provider: "antigravity", Success: false, CredentialScope: true,
		AccessToken: "old-access", RefreshToken: "old-refresh", CredentialVersion: 1,
		Error: &Error{HTTPStatus: http.StatusForbidden, Code: "tos_violation"},
	})
	updated, _ := manager.GetByID(auth.ID)
	if updated.Status == StatusDisabled || updated.Disabled {
		t.Fatalf("stale TOS result disabled fresh credential: status=%q disabled=%v", updated.Status, updated.Disabled)
	}
}

// TestMarkResult_TOSViolation_SameAccountRefreshApplies verifies that a TOS
// result issued before a same-account OAuth refresh (access token rotated,
// refresh token retained) is NOT dropped as stale: the refresh token proves it
// is the same account, so the terminal state is applied.
func TestMarkResult_TOSViolation_SameAccountRefreshApplies(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	// Current credential after a same-account refresh: new access token, SAME
	// refresh token, bumped credential version.
	auth := &Auth{
		ID: "tos-same-acct", Provider: "antigravity", Status: StatusActive, CredentialVersion: 2,
		Metadata: map[string]any{"access_token": "new-access", "refresh_token": "same-refresh"},
	}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	// Old request: old access token (no longer matches), same refresh token.
	manager.MarkResult(ctx, Result{
		AuthID: auth.ID, Provider: "antigravity", Success: false, CredentialScope: true,
		AccessToken: "old-access", RefreshToken: "same-refresh", CredentialVersion: 1,
		Error: &Error{HTTPStatus: http.StatusForbidden, Code: "tos_violation"},
	})
	updated, _ := manager.GetByID(auth.ID)
	if updated.Status != StatusDisabled || !updated.Disabled {
		t.Fatalf("same-account TOS result dropped after refresh: status=%q disabled=%v", updated.Status, updated.Disabled)
	}
}

// TestMarkResult_TOSViolation_MultiModelPreservesUnavailableAndSkipsInferenceVerified
// verifies that suspending a credential with active sibling models in ModelStates
// keeps auth.Unavailable=true immediately and across late in-flight successes,
// and never marks a suspended credential InferenceVerified=true.
func TestMarkResult_TOSViolation_MultiModelPreservesUnavailableAndSkipsInferenceVerified(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	auth := &Auth{ID: "tos-multi-model", Provider: "antigravity", Status: StatusActive}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// Prior success populates an active sibling model entry in auth.ModelStates.
	manager.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "antigravity",
		Model:    "gemini-3.8-flash",
		Success:  true,
	})

	// Another model on the same credential hits 403 TOS_VIOLATION.
	manager.MarkResult(ctx, Result{
		AuthID:          auth.ID,
		Provider:        "antigravity",
		Model:           "gemini-4-neon",
		Success:         false,
		CredentialScope: true,
		Error: &Error{
			HTTPStatus: http.StatusForbidden,
			Code:       "tos_violation",
			Message:    "TOS_VIOLATION",
		},
	})

	afterTOS, ok := manager.GetByID(auth.ID)
	if !ok || afterTOS == nil {
		t.Fatal("auth not found after TOS MarkResult")
	}
	if !afterTOS.Disabled || afterTOS.Status != StatusDisabled {
		t.Fatalf("after TOS: disabled=%v status=%q, want true / %q", afterTOS.Disabled, afterTOS.Status, StatusDisabled)
	}
	if !afterTOS.Unavailable {
		t.Fatal("after TOS: auth.Unavailable = false, want true (must not be cleared by active sibling model)")
	}
	if afterTOS.InferenceVerified || !afterTOS.LastInferenceVerifiedAt.IsZero() {
		t.Fatalf("after TOS: InferenceVerified=%v LastInferenceVerifiedAt=%v, want false / zero", afterTOS.InferenceVerified, afterTOS.LastInferenceVerifiedAt)
	}

	// Late in-flight success on the sibling model must not clear Unavailable or set InferenceVerified.
	manager.MarkResult(ctx, Result{
		AuthID:            auth.ID,
		Provider:          "antigravity",
		Model:             "gemini-3.8-flash",
		Success:           true,
		CompletedResponse: true,
	})

	afterSiblingSuccess, _ := manager.GetByID(auth.ID)
	if !afterSiblingSuccess.Disabled || afterSiblingSuccess.Status != StatusDisabled {
		t.Fatalf("after late sibling success: disabled=%v status=%q, want true / %q", afterSiblingSuccess.Disabled, afterSiblingSuccess.Status, StatusDisabled)
	}
	if !afterSiblingSuccess.Unavailable {
		t.Fatal("after late sibling success: auth.Unavailable = false, want true")
	}
	if afterSiblingSuccess.InferenceVerified || !afterSiblingSuccess.LastInferenceVerifiedAt.IsZero() {
		t.Fatalf("after late sibling success: InferenceVerified=%v LastInferenceVerifiedAt=%v, want false / zero", afterSiblingSuccess.InferenceVerified, afterSiblingSuccess.LastInferenceVerifiedAt)
	}

	// Late in-flight success on the suspended model itself must also preserve its disabled model state.
	manager.MarkResult(ctx, Result{
		AuthID:            auth.ID,
		Provider:          "antigravity",
		Model:             "gemini-4-neon",
		Success:           true,
		CompletedResponse: true,
	})

	afterSameModelSuccess, _ := manager.GetByID(auth.ID)
	if !afterSameModelSuccess.Unavailable || afterSameModelSuccess.InferenceVerified {
		t.Fatalf("after late same-model success: unavailable=%v inferenceVerified=%v, want true / false", afterSameModelSuccess.Unavailable, afterSameModelSuccess.InferenceVerified)
	}
	neonState := afterSameModelSuccess.ModelStates["gemini-4-neon"]
	if neonState == nil || neonState.Status != StatusDisabled || !neonState.Unavailable {
		t.Fatalf("after late same-model success: gemini-4-neon state = %+v, want StatusDisabled and Unavailable=true", neonState)
	}
}

// TestMarkResult_StatusDisabledOnly_LateSuccessDoesNotReviveOrVerify verifies that
// a credential marked StatusDisabled (even if Disabled is false) rejects late
// model-scoped and model-less successes from reviving or verifying it.
func TestMarkResult_StatusDisabledOnly_LateSuccessDoesNotReviveOrVerify(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	auth := &Auth{
		ID:          "status-disabled-only",
		Provider:    "antigravity",
		Status:      StatusDisabled,
		Unavailable: true,
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.MarkResult(ctx, Result{
		AuthID:            auth.ID,
		Provider:          "antigravity",
		Model:             "gemini-3.8-flash",
		Success:           true,
		CompletedResponse: true,
	})
	manager.MarkResult(ctx, Result{
		AuthID:            auth.ID,
		Provider:          "antigravity",
		Success:           true,
		CompletedResponse: true,
	})

	updated, _ := manager.GetByID(auth.ID)
	if updated.Status != StatusDisabled {
		t.Fatalf("auth.Status = %q, want %q", updated.Status, StatusDisabled)
	}
	if !updated.Unavailable {
		t.Fatal("auth.Unavailable = false, want true")
	}
	if updated.InferenceVerified || !updated.LastInferenceVerifiedAt.IsZero() {
		t.Fatalf("InferenceVerified=%v LastInferenceVerifiedAt=%v, want false / zero", updated.InferenceVerified, updated.LastInferenceVerifiedAt)
	}
}

// TestMarkResult_TOSViolation_ModelLessWithExistingModelStatesPreservesUnavailable
// verifies that a credential-scoped model-less TOS_VIOLATION keeps Unavailable=true
// even when auth.ModelStates already contains active models.
func TestMarkResult_TOSViolation_ModelLessWithExistingModelStatesPreservesUnavailable(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	auth := &Auth{ID: "tos-modelless-multi", Provider: "antigravity", Status: StatusActive}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "antigravity",
		Model:    "gemini-3.8-flash",
		Success:  true,
	})

	manager.MarkResult(ctx, Result{
		AuthID:          auth.ID,
		Provider:        "antigravity",
		Success:         false,
		CredentialScope: true,
		Error: &Error{
			HTTPStatus: http.StatusForbidden,
			Code:       "tos_violation",
			Message:    "TOS_VIOLATION",
		},
	})

	updated, _ := manager.GetByID(auth.ID)
	if !updated.Disabled || updated.Status != StatusDisabled {
		t.Fatalf("disabled=%v status=%q, want true / %q", updated.Disabled, updated.Status, StatusDisabled)
	}
	if !updated.Unavailable {
		t.Fatal("auth.Unavailable = false, want true")
	}
}

// TestMarkResult_TOSViolation_DisableCoolingPreservesUnavailable verifies that
// disable_cooling does not clear Unavailable on the credential or model state
// after a terminal TOS_VIOLATION.
func TestMarkResult_TOSViolation_DisableCoolingPreservesUnavailable(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	auth := &Auth{
		ID:       "tos-disable-cooling",
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{"disable_cooling": true},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.MarkResult(ctx, Result{
		AuthID:   auth.ID,
		Provider: "antigravity",
		Model:    "gemini-3.8-flash",
		Success:  true,
	})
	manager.MarkResult(ctx, Result{
		AuthID:          auth.ID,
		Provider:        "antigravity",
		Model:           "gemini-4-neon",
		Success:         false,
		CredentialScope: true,
		Error: &Error{
			HTTPStatus: http.StatusForbidden,
			Code:       "tos_violation",
			Message:    "TOS_VIOLATION",
		},
	})

	updated, _ := manager.GetByID(auth.ID)
	if !updated.Disabled || updated.Status != StatusDisabled || !updated.Unavailable {
		t.Fatalf("after TOS with disable_cooling: disabled=%v status=%q unavailable=%v", updated.Disabled, updated.Status, updated.Unavailable)
	}
	if state := updated.ModelStates["gemini-4-neon"]; state == nil || state.Status != StatusDisabled || !state.Unavailable {
		t.Fatalf("gemini-4-neon state = %+v, want StatusDisabled and Unavailable=true", state)
	}
}
