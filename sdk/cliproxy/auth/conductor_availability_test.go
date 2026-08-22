package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestUpdateAggregatedAvailability_UnavailableWithoutNextRetryDoesNotBlockAuth(t *testing.T) {
	t.Parallel()

	now := time.Now()
	model := "test-model"
	auth := &Auth{
		ID: "a",
		ModelStates: map[string]*ModelState{
			model: {
				Status:      StatusError,
				Unavailable: true,
			},
		},
	}

	updateAggregatedAvailability(auth, now)

	if auth.Unavailable {
		t.Fatalf("auth.Unavailable = true, want false")
	}
	if !auth.NextRetryAfter.IsZero() {
		t.Fatalf("auth.NextRetryAfter = %v, want zero", auth.NextRetryAfter)
	}
}

func TestUpdateAggregatedAvailability_FutureNextRetryBlocksAuth(t *testing.T) {
	t.Parallel()

	now := time.Now()
	model := "test-model"
	next := now.Add(5 * time.Minute)
	auth := &Auth{
		ID: "a",
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: next,
			},
		},
	}

	updateAggregatedAvailability(auth, now)

	if !auth.Unavailable {
		t.Fatalf("auth.Unavailable = false, want true")
	}
	if auth.NextRetryAfter.IsZero() {
		t.Fatalf("auth.NextRetryAfter = zero, want %v", next)
	}
	if auth.NextRetryAfter.Sub(next) > time.Second || next.Sub(auth.NextRetryAfter) > time.Second {
		t.Fatalf("auth.NextRetryAfter = %v, want %v", auth.NextRetryAfter, next)
	}
}

func TestManager_AvailableProvidersAndHasProviderAuth_ExcludeDisabled(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()

	if _, err := manager.Register(ctx, &Auth{ID: "active", Provider: "claude", Status: StatusActive}); err != nil {
		t.Fatalf("register active auth: %v", err)
	}
	// Provider gemini only has an auth with the Disabled flag set.
	if _, err := manager.Register(ctx, &Auth{ID: "flag-disabled", Provider: "gemini", Disabled: true}); err != nil {
		t.Fatalf("register flag-disabled auth: %v", err)
	}
	// Provider codex only has an auth whose Status is StatusDisabled.
	if _, err := manager.Register(ctx, &Auth{ID: "status-disabled", Provider: "codex", Status: StatusDisabled}); err != nil {
		t.Fatalf("register status-disabled auth: %v", err)
	}

	providers := manager.AvailableProviders()
	present := make(map[string]bool, len(providers))
	for _, p := range providers {
		present[p] = true
	}
	if !present["claude"] {
		t.Errorf("AvailableProviders() = %v, want to include active provider claude", providers)
	}
	if present["gemini"] {
		t.Errorf("AvailableProviders() = %v, want to exclude Disabled provider gemini", providers)
	}
	if present["codex"] {
		t.Errorf("AvailableProviders() = %v, want to exclude StatusDisabled provider codex", providers)
	}

	if !manager.HasProviderAuth("claude") {
		t.Errorf("HasProviderAuth(claude) = false, want true")
	}
	if manager.HasProviderAuth("gemini") {
		t.Errorf("HasProviderAuth(gemini) = true, want false (only Disabled auth registered)")
	}
	if manager.HasProviderAuth("codex") {
		t.Errorf("HasProviderAuth(codex) = true, want false (only StatusDisabled auth registered)")
	}
}

func TestManager_ResetQuotaClearsRuntimeAndRegistryState(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "reset-quota-auth"
	model := "reset-quota-model"
	next := time.Now().Add(time.Hour)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	if _, errRegister := manager.Register(ctx, &Auth{
		ID:             authID,
		Provider:       "claude",
		Status:         StatusError,
		StatusMessage:  "quota exhausted",
		Unavailable:    true,
		NextRetryAfter: next,
		Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next, BackoffLevel: 2},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				StatusMessage:  "quota exhausted",
				Unavailable:    true,
				NextRetryAfter: next,
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next, BackoffLevel: 2},
				UpdatedAt:      next,
			},
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	reg.SetModelQuotaExceeded(authID, model)
	reg.SuspendClientModel(authID, model, "quota")
	if count := reg.GetModelCount(model); count != 0 {
		t.Fatalf("registry model count before reset = %d, want 0", count)
	}

	updated, models, errReset := manager.ResetQuota(ctx, authID)
	if errReset != nil {
		t.Fatalf("ResetQuota() error = %v", errReset)
	}
	if updated == nil {
		t.Fatalf("ResetQuota() updated auth is nil")
	}
	if len(models) != 1 || models[0] != model {
		t.Fatalf("ResetQuota() models = %v, want [%s]", models, model)
	}
	if updated.Status != StatusActive || updated.StatusMessage != "" || updated.Unavailable || !updated.NextRetryAfter.IsZero() {
		t.Fatalf("updated auth state = status %q message %q unavailable %v next %v", updated.Status, updated.StatusMessage, updated.Unavailable, updated.NextRetryAfter)
	}
	if updated.Quota.Exceeded || updated.Quota.Reason != "" || !updated.Quota.NextRecoverAt.IsZero() || updated.Quota.BackoffLevel != 0 {
		t.Fatalf("updated auth quota = %+v, want cleared", updated.Quota)
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("updated model state missing")
	}
	if state.Status != StatusActive || state.StatusMessage != "" || state.Unavailable || !state.NextRetryAfter.IsZero() {
		t.Fatalf("updated model state = status %q message %q unavailable %v next %v", state.Status, state.StatusMessage, state.Unavailable, state.NextRetryAfter)
	}
	if state.Quota.Exceeded || state.Quota.Reason != "" || !state.Quota.NextRecoverAt.IsZero() || state.Quota.BackoffLevel != 0 {
		t.Fatalf("updated model quota = %+v, want cleared", state.Quota)
	}
	if count := reg.GetModelCount(model); count != 1 {
		t.Fatalf("registry model count after reset = %d, want 1", count)
	}
}

func TestManager_ResumeEveryModelAfterCredentialRecovery(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "multi-model-auth"
	modelA := "model-a"
	modelB := "model-b"
	modelC := "model-c"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "openai", []*registry.ModelInfo{
		{ID: modelA},
		{ID: modelB},
		{ID: modelC},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	if _, errRegister := manager.Register(ctx, &Auth{
		ID:       authID,
		Provider: "openai",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			modelA: {Status: StatusActive},
			modelB: {Status: StatusActive},
			modelC: {Status: StatusActive},
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// Verify all 3 models are available before failure
	for _, m := range []string{modelA, modelB, modelC} {
		if count := reg.GetModelCount(m); count != 1 {
			t.Fatalf("registry model count for %s before failure = %d, want 1", m, count)
		}
	}

	// Fail with invalid_api_key on modelA -> should suspend all models for this auth
	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "openai",
		Model:    modelA,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusUnauthorized, Code: "invalid_api_key", Message: "API key not valid"},
	})

	// Verify all 3 models are now suspended
	for _, m := range []string{modelA, modelB, modelC} {
		if count := reg.GetModelCount(m); count != 0 {
			t.Fatalf("registry model count for %s after invalid_api_key = %d, want 0", m, count)
		}
	}

	// Fast-forward / expire the cooldown on the auth (simulating cooldown expiry or key replacement)
	manager.mu.Lock()
	auth := manager.auths[authID]
	auth.NextRetryAfter = time.Now().Add(-time.Second)
	auth.Quota.NextRecoverAt = time.Now().Add(-time.Second)
	for _, state := range auth.ModelStates {
		state.NextRetryAfter = time.Now().Add(-time.Second)
		state.Quota.NextRecoverAt = time.Now().Add(-time.Second)
	}
	manager.mu.Unlock()

	// Successful request on modelA -> proves credential recovered
	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "openai",
		Model:    modelA,
		Success:  true,
	})

	// Verify all 3 models are resumed in the registry
	for _, m := range []string{modelA, modelB, modelC} {
		if count := reg.GetModelCount(m); count != 1 {
			t.Fatalf("registry model count for %s after recovery = %d, want 1", m, count)
		}
	}
}

func TestManager_ModelSpecificSuspensionSurvivesSiblingSuccess(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "sibling-suspension-auth"
	modelA := "model-a"
	modelB := "model-b"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "openai", []*registry.ModelInfo{
		{ID: modelA},
		{ID: modelB},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	if _, errRegister := manager.Register(ctx, &Auth{
		ID:       authID,
		Provider: "openai",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			modelA: {Status: StatusActive},
			modelB: {Status: StatusActive},
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// Fail modelB with model_not_supported -> modelB should be suspended, modelA available
	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "openai",
		Model:    modelB,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusBadRequest, Code: "model_not_supported", Message: "model not supported"},
	})

	if count := reg.GetModelCount(modelB); count != 0 {
		t.Fatalf("registry model count for modelB after suspension = %d, want 0", count)
	}
	if count := reg.GetModelCount(modelA); count != 1 {
		t.Fatalf("registry model count for modelA = %d, want 1", count)
	}

	// Success on modelA -> should NOT resume modelB
	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "openai",
		Model:    modelA,
		Success:  true,
	})

	if count := reg.GetModelCount(modelA); count != 1 {
		t.Fatalf("registry model count for modelA after success = %d, want 1", count)
	}
	if count := reg.GetModelCount(modelB); count != 0 {
		t.Fatalf("registry model count for modelB after modelA success = %d, want 0 (suspension should survive)", count)
	}
}

func TestManager_ModelNotSupportedSuspensionResumesOnOwnSuccess(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "model-not-supported-resume-auth"
	model := "model-a"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "openai", []*registry.ModelInfo{
		{ID: model},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	if _, errRegister := manager.Register(ctx, &Auth{
		ID:       authID,
		Provider: "openai",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			model: {Status: StatusActive},
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// Fail model with model_not_supported -> model should be suspended in registry
	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "openai",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusBadRequest, Code: "model_not_supported", Message: "model not supported"},
	})

	if count := reg.GetModelCount(model); count != 0 {
		t.Fatalf("registry model count for model after suspension = %d, want 0", count)
	}
	if reason := reg.GetClientModelSuspensionReason(authID, model); reason != "model_not_supported" {
		t.Fatalf("suspension reason = %q, want model_not_supported", reason)
	}

	// Success on model -> should resume model in registry
	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "openai",
		Model:    model,
		Success:  true,
	})

	if count := reg.GetModelCount(model); count != 1 {
		t.Fatalf("registry model count for model after success = %d, want 1", count)
	}
	if reason := reg.GetClientModelSuspensionReason(authID, model); reason != "" {
		t.Fatalf("suspension reason after success = %q, want empty", reason)
	}
}

// TestManager_ModelSpecificResumableSiblingSuspensionSurvivesSiblingSuccess verifies that a
// sibling model suspended for a resumable model-specific reason (not_found, quota,
// payment_required) keeps its registry suspension when a different model of the same credential
// succeeds. Only credential-wide reasons like invalid_api_key justify cross-model resumption.
func TestManager_ModelSpecificResumableSiblingSuspensionSurvivesSiblingSuccess(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "sibling-resumable-suspension-auth"
	modelA := "model-a2"
	modelB := "model-b2"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "openai", []*registry.ModelInfo{
		{ID: modelA},
		{ID: modelB},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	if _, errRegister := manager.Register(ctx, &Auth{
		ID:       authID,
		Provider: "openai",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			modelA: {Status: StatusActive},
			modelB: {Status: StatusActive},
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// Fail modelB with a resumable, model-specific reason (not_found).
	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "openai",
		Model:    modelB,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusNotFound, Code: "not_found", Message: "model b not found"},
	})
	if count := reg.GetModelCount(modelB); count != 0 {
		t.Fatalf("registry model count for modelB after suspension = %d, want 0", count)
	}

	// Success on modelA -> must NOT resume modelB (model-specific reason).
	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "openai",
		Model:    modelA,
		Success:  true,
	})
	if count := reg.GetModelCount(modelB); count != 0 {
		t.Fatalf("registry model count for modelB after modelA success = %d, want 0 (model-specific suspension should survive)", count)
	}
	if count := reg.GetModelCount(modelA); count != 1 {
		t.Fatalf("registry model count for modelA after success = %d, want 1", count)
	}
}

// TestManager_CredentialWideSiblingSuspensionResumesOnSiblingSuccess verifies that a sibling
// suspended for a credential-wide reason (invalid_api_key) is resumed by a successful request on
// another model of the same credential.
func TestManager_CredentialWideSiblingSuspensionResumesOnSiblingSuccess(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "sibling-credentialwide-resume-auth"
	modelA := "model-a3"
	modelB := "model-b3"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "openai", []*registry.ModelInfo{
		{ID: modelA},
		{ID: modelB},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	if _, errRegister := manager.Register(ctx, &Auth{
		ID:       authID,
		Provider: "openai",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			modelA: {Status: StatusActive},
			modelB: {Status: StatusActive},
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// Suspend sibling modelB with a genuine credential-wide reason (invalid_api_key) directly in
	// the registry. A live invalid_api_key failure would also mark the credential with an active
	// credential_quota cooldown that legitimately suppresses the resume path; suspending the sibling
	// directly isolates the sibling-resume loop's reason scoping.
	reg.SuspendClientModel(authID, modelB, "invalid_api_key")
	if count := reg.GetModelCount(modelB); count != 0 {
		t.Fatalf("registry model count for modelB after suspension = %d, want 0", count)
	}

	// Success on modelA -> resumes modelB (credential-wide reason).
	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "openai",
		Model:    modelA,
		Success:  true,
	})
	if count := reg.GetModelCount(modelB); count != 1 {
		t.Fatalf("registry model count for modelB after modelA success = %d, want 1 (credential-wide suspension should resume)", count)
	}
}

// TestManager_ModelSpecificFailureOverwritesCredentialWideSuspension reproduces the scenario
// where a credential-wide invalid_api_key fanout suspends all models, and model B later encounters
// a model-specific failure (e.g. not_found). Model B's suspension reason must be updated to the
// model-specific reason so that a subsequent success on model A does not erroneously resume model B.
func TestManager_ModelSpecificFailureOverwritesCredentialWideSuspension(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "sibling-overwrite-suspension-auth"
	modelA := "model-a4"
	modelB := "model-b4"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "openai", []*registry.ModelInfo{
		{ID: modelA},
		{ID: modelB},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	if _, errRegister := manager.Register(ctx, &Auth{
		ID:       authID,
		Provider: "openai",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			modelA: {Status: StatusActive},
			modelB: {Status: StatusActive},
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// 1. Initial credential-wide suspension on all models (invalid_api_key).
	// To isolate the suspension reason overwrite and sibling-resume behavior without active
	// credential_quota cooldown gating, suspend modelB directly with invalid_api_key as the
	// fanout produces.
	reg.SuspendClientModel(authID, modelB, "invalid_api_key")
	if reason := reg.GetClientModelSuspensionReason(authID, modelB); reason != "invalid_api_key" {
		t.Fatalf("modelB initial suspension reason = %q, want invalid_api_key", reason)
	}

	// 2. Model B records a model-specific failure (404 not_found).
	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "openai",
		Model:    modelB,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusNotFound, Code: "not_found", Message: "model b not found"},
	})
	if reason := reg.GetClientModelSuspensionReason(authID, modelB); reason != "not_found" {
		t.Fatalf("modelB suspension reason after 404 = %q, want not_found", reason)
	}
	if count := reg.GetModelCount(modelB); count != 0 {
		t.Fatalf("registry model count for modelB after 404 = %d, want 0", count)
	}

	// 3. Model A succeeds -> sibling-resume loop runs. Model B must STAY suspended.
	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "openai",
		Model:    modelA,
		Success:  true,
	})
	if reason := reg.GetClientModelSuspensionReason(authID, modelB); reason != "not_found" {
		t.Fatalf("modelB suspension reason after modelA success = %q, want not_found (must survive)", reason)
	}
	if count := reg.GetModelCount(modelB); count != 0 {
		t.Fatalf("registry model count for modelB after modelA success = %d, want 0 (must stay suspended)", count)
	}
	if count := reg.GetModelCount(modelA); count != 1 {
		t.Fatalf("registry model count for modelA after success = %d, want 1", count)
	}
}
