package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestManager_InvalidAPIKey_PreservesLongerSiblingCooldown(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "sibling-longer-cooldown-auth"
	modelInvalid := "model-invalid"
	modelLong := "model-long"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "openai", []*registry.ModelInfo{
		{ID: modelInvalid},
		{ID: modelLong},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	now := time.Now()
	longerRecovery := now.Add(12 * time.Hour)

	auth := &Auth{
		ID:       authID,
		Provider: "openai",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			modelInvalid: {Status: StatusActive},
			modelLong: {
				Unavailable:    true,
				Status:         StatusError,
				StatusMessage:  "model_not_supported",
				NextRetryAfter: longerRecovery,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "model_not_supported",
					NextRecoverAt: longerRecovery,
				},
			},
		},
	}

	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// Fail modelInvalid with invalid_api_key error
	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "openai",
		Model:    modelInvalid,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusUnauthorized, Code: "api_key_invalid", Message: "api key not valid"},
	})

	updated, ok := manager.GetByID(authID)
	if !ok || updated == nil {
		t.Fatalf("auth not found after MarkResult")
	}

	stateLong := updated.ModelStates[modelLong]
	if stateLong == nil {
		t.Fatalf("modelState for %s missing", modelLong)
	}

	if !stateLong.Unavailable {
		t.Errorf("modelLong Unavailable = false, want true")
	}
	if stateLong.Status != StatusError {
		t.Errorf("modelLong Status = %v, want %v", stateLong.Status, StatusError)
	}
	if stateLong.StatusMessage != "invalid_api_key" {
		t.Errorf("modelLong StatusMessage = %q, want invalid_api_key", stateLong.StatusMessage)
	}

	// Sibling longer cooldown must NOT be shortened to 30 minutes.
	if !stateLong.NextRetryAfter.Equal(longerRecovery) {
		t.Errorf("modelLong NextRetryAfter = %v, want %v", stateLong.NextRetryAfter, longerRecovery)
	}
	if stateLong.Quota.Reason != "model_not_supported" {
		t.Errorf("modelLong Quota.Reason = %q, want model_not_supported", stateLong.Quota.Reason)
	}
	if !stateLong.Quota.NextRecoverAt.Equal(longerRecovery) {
		t.Errorf("modelLong Quota.NextRecoverAt = %v, want %v", stateLong.Quota.NextRecoverAt, longerRecovery)
	}

	// After the 30-minute window elapses, the sibling must STILL be blocked by its 12-hour cooldown.
	after30m := now.Add(35 * time.Minute)
	if !stateLong.NextRetryAfter.After(after30m) {
		t.Errorf("modelLong NextRetryAfter %v not after 35m mark %v (shortened cooldown)", stateLong.NextRetryAfter, after30m)
	}
}

func TestManager_InvalidAPIKey_AppliesCredentialBlockToSiblingWithNoPriorCooldown(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "sibling-no-cooldown-auth"
	modelInvalid := "model-invalid"
	modelClean := "model-clean"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "openai", []*registry.ModelInfo{
		{ID: modelInvalid},
		{ID: modelClean},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	auth := &Auth{
		ID:       authID,
		Provider: "openai",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			modelInvalid: {Status: StatusActive},
			modelClean:   {Status: StatusActive},
		},
	}

	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	now := time.Now()
	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "openai",
		Model:    modelInvalid,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusUnauthorized, Code: "api_key_invalid", Message: "api key not valid"},
	})

	updated, ok := manager.GetByID(authID)
	if !ok || updated == nil {
		t.Fatalf("auth not found after MarkResult")
	}

	stateClean := updated.ModelStates[modelClean]
	if stateClean == nil {
		t.Fatalf("modelState for %s missing", modelClean)
	}

	if !stateClean.Unavailable {
		t.Errorf("modelClean Unavailable = false, want true")
	}
	if stateClean.Status != StatusError {
		t.Errorf("modelClean Status = %v, want %v", stateClean.Status, StatusError)
	}
	if stateClean.StatusMessage != "invalid_api_key" {
		t.Errorf("modelClean StatusMessage = %q, want invalid_api_key", stateClean.StatusMessage)
	}
	if stateClean.NextRetryAfter.IsZero() || !stateClean.NextRetryAfter.After(now.Add(29*time.Minute)) {
		t.Errorf("modelClean NextRetryAfter = %v, want ~30m from now", stateClean.NextRetryAfter)
	}
	if !stateClean.Quota.Exceeded || stateClean.Quota.Reason != "credential_quota" || stateClean.Quota.NextRecoverAt.IsZero() {
		t.Errorf("modelClean Quota = %+v, want Exceeded: true, Reason: credential_quota", stateClean.Quota)
	}
}

func TestManager_InvalidAPIKey_ExtendsShorterSiblingCooldown(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "sibling-short-cooldown-auth"
	modelInvalid := "model-invalid"
	modelShort := "model-short"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "openai", []*registry.ModelInfo{
		{ID: modelInvalid},
		{ID: modelShort},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	now := time.Now()
	shortRecovery := now.Add(5 * time.Minute)

	auth := &Auth{
		ID:       authID,
		Provider: "openai",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			modelInvalid: {Status: StatusActive},
			modelShort: {
				Unavailable:    true,
				Status:         StatusError,
				StatusMessage:  "short_error",
				NextRetryAfter: shortRecovery,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: shortRecovery,
				},
			},
		},
	}

	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "openai",
		Model:    modelInvalid,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusUnauthorized, Code: "api_key_invalid", Message: "api key not valid"},
	})

	updated, ok := manager.GetByID(authID)
	if !ok || updated == nil {
		t.Fatalf("auth not found after MarkResult")
	}

	stateShort := updated.ModelStates[modelShort]
	if stateShort == nil {
		t.Fatalf("modelState for %s missing", modelShort)
	}

	if !stateShort.Unavailable {
		t.Errorf("modelShort Unavailable = false, want true")
	}
	if stateShort.Status != StatusError {
		t.Errorf("modelShort Status = %v, want %v", stateShort.Status, StatusError)
	}
	if stateShort.StatusMessage != "invalid_api_key" {
		t.Errorf("modelShort StatusMessage = %q, want invalid_api_key", stateShort.StatusMessage)
	}
	if !stateShort.NextRetryAfter.After(shortRecovery) || !stateShort.NextRetryAfter.After(now.Add(29*time.Minute)) {
		t.Errorf("modelShort NextRetryAfter = %v, want extended to ~30m (> %v)", stateShort.NextRetryAfter, shortRecovery)
	}
	if !stateShort.Quota.Exceeded || stateShort.Quota.Reason != "credential_quota" || !stateShort.Quota.NextRecoverAt.After(shortRecovery) {
		t.Errorf("modelShort Quota = %+v, want extended credential_quota (> %v)", stateShort.Quota, shortRecovery)
	}
}

func TestManager_InvalidAPIKey_PreservesLongerAuthLevelCooldown(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	authID := "auth-longer-cooldown"
	modelInvalid := "model-invalid"

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "openai", []*registry.ModelInfo{
		{ID: modelInvalid},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	now := time.Now()
	longerRecovery := now.Add(12 * time.Hour)

	auth := &Auth{
		ID:             authID,
		Provider:       "openai",
		Status:         StatusError,
		Unavailable:    true,
		NextRetryAfter: longerRecovery,
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "credential_quota",
			NextRecoverAt: longerRecovery,
		},
		ModelStates: map[string]*ModelState{
			modelInvalid: {Status: StatusActive},
		},
	}

	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	manager.MarkResult(ctx, Result{
		AuthID:   authID,
		Provider: "openai",
		Model:    modelInvalid,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusUnauthorized, Code: "api_key_invalid", Message: "api key not valid"},
	})

	updated, ok := manager.GetByID(authID)
	if !ok || updated == nil {
		t.Fatalf("auth not found after MarkResult")
	}

	if !updated.NextRetryAfter.Equal(longerRecovery) {
		t.Errorf("auth NextRetryAfter = %v, want %v", updated.NextRetryAfter, longerRecovery)
	}
	if updated.Quota.Reason != "credential_quota" {
		t.Errorf("auth Quota.Reason = %q, want credential_quota", updated.Quota.Reason)
	}
	if !updated.Quota.NextRecoverAt.Equal(longerRecovery) {
		t.Errorf("auth Quota.NextRecoverAt = %v, want %v", updated.Quota.NextRecoverAt, longerRecovery)
	}
}
