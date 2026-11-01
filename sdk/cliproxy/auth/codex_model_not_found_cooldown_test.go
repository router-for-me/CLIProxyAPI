package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestCodexStructuredModelNotFound_ClassificationAndCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	testCases := []struct {
		name   string
		status int
		body   string
	}{
		{
			name:   "status 400 with invalid_request_error and model_not_found code",
			status: http.StatusBadRequest,
			body:   `{"error":{"type":"invalid_request_error","code":"model_not_found","message":"The model gpt-5.5 does not exist or you do not have access to it."}}`,
		},
		{
			name:   "status 404 with invalid_request_error and model_not_found code",
			status: http.StatusNotFound,
			body:   `{"error":{"type":"invalid_request_error","code":"model_not_found","message":"The model gpt-5.5 does not exist or you do not have access to it."}}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			rawErr := &statusBearingError{
				status: tc.status,
				msg:    tc.body,
			}

			if isRequestInvalidError(rawErr) {
				t.Fatalf("isRequestInvalidError(%v) = true, want false (model_not_found is not a caller request fault)", rawErr)
			}

			resultErr := resultErrorFromError(rawErr)
			if resultErr == nil {
				t.Fatal("resultErrorFromError returned nil")
			}
			if resultErr.Code != "model_not_found" {
				t.Fatalf("resultErr.Code = %q, want %q", resultErr.Code, "model_not_found")
			}
			if resultErr.IsRequestScoped() {
				t.Fatal("resultErr must not be marked request-scoped")
			}
			if shouldSkipCredentialCooldown(resultErr) {
				t.Fatalf("shouldSkipCredentialCooldown(%#v) = true, want false", resultErr)
			}

			m := NewManager(nil, nil, nil)
			auth := &Auth{ID: "auth-codex-1", Provider: "codex"}
			if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}

			model := "gpt-5.5"
			m.MarkResult(context.Background(), Result{
				AuthID:   auth.ID,
				Provider: auth.Provider,
				Model:    model,
				Success:  false,
				Error:    resultErr,
			})

			updated, ok := m.GetByID(auth.ID)
			if !ok || updated == nil {
				t.Fatal("expected auth to be registered")
			}
			state := updated.ModelStates[model]
			if state == nil || !state.Unavailable {
				t.Fatalf("expected model state to be unavailable, got %#v", state)
			}
			if state.LastError == nil || state.LastError.Code != "model_not_found" {
				t.Fatalf("state.LastError = %#v, want code model_not_found", state.LastError)
			}
			remaining := time.Until(state.NextRetryAfter)
			if remaining < 11*time.Hour || remaining > 13*time.Hour {
				t.Fatalf("expected ~12h cooldown, got remaining=%v", remaining)
			}
		})
	}
}

func TestCodexStructuredModelNotFound_SessionAffinityReleased(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	selector := NewSessionAffinitySelector(&RoundRobinSelector{})
	defer selector.Stop()

	auth1 := &Auth{ID: "auth-1", Provider: "codex"}
	auth2 := &Auth{ID: "auth-2", Provider: "codex"}
	candidates := []*Auth{auth1, auth2}

	opts := cliproxyexecutor.Options{
		Headers: http.Header{
			"X-Session-Id": []string{"session-model-not-found-test"},
		},
	}

	// 1. Initial pick binds to auth-1
	picked, errPick := selector.Pick(context.Background(), "codex", "gpt-5.5", opts, candidates)
	if errPick != nil || picked == nil {
		t.Fatalf("initial Pick failed: %v", errPick)
	}
	if picked.ID != "auth-1" {
		t.Fatalf("initial Pick = %s, want auth-1", picked.ID)
	}

	selector.OnResult(Result{
		AuthID:   picked.ID,
		Provider: "codex",
		Model:    "gpt-5.5",
		Success:  true,
		Options:  opts,
	})

	// 2. Structured model_not_found failure occurs
	rawErr := &statusBearingError{
		status: http.StatusBadRequest,
		msg:    `{"error":{"type":"invalid_request_error","code":"model_not_found","message":"The model gpt-5.5 does not exist or you do not have access to it."}}`,
	}
	resultErr := resultErrorFromError(rawErr)

	selector.OnResult(Result{
		AuthID:   picked.ID,
		Provider: "codex",
		Model:    "gpt-5.5",
		Success:  false,
		Error:    resultErr,
		Options:  opts,
	})

	// 3. Next pick with reverse candidate order must NOT return auth-1 because affinity was released
	reverseCandidates := []*Auth{auth2, auth1}
	nextPicked, errNextPick := selector.Pick(context.Background(), "codex", "gpt-5.5", opts, reverseCandidates)
	if errNextPick != nil || nextPicked == nil {
		t.Fatalf("next Pick failed: %v", errNextPick)
	}
	if nextPicked.ID != "auth-2" {
		t.Fatalf("affinity was not released: got %s, want auth-2", nextPicked.ID)
	}
}

func TestCodexModelNotFound_CallerInputErrorNotModelCooldown(t *testing.T) {
	rawErr := &statusBearingError{
		status: http.StatusBadRequest,
		msg:    `{"error":{"type":"invalid_request_error","message":"The model not found in request body"}}`,
	}
	if !isRequestInvalidError(rawErr) {
		t.Fatalf("isRequestInvalidError(%v) = false, want true for caller request fault", rawErr)
	}
	resultErr := resultErrorFromError(rawErr)
	if resultErr == nil || resultErr.Code != requestScopedErrorCode {
		t.Fatalf("resultErr = %#v, want request_scoped code", resultErr)
	}
	if !shouldSkipCredentialCooldown(resultErr) {
		t.Fatalf("shouldSkipCredentialCooldown(%#v) = false, want true for request fault", resultErr)
	}
}

func TestCodexModelNotFound_Generic404NotModelNotFound(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	rawErr := &statusBearingError{
		status: http.StatusNotFound,
		msg:    `{"error":{"message":"Not Found"}}`,
	}
	resultErr := resultErrorFromError(rawErr)
	if resultErr != nil && resultErr.Code == "model_not_found" {
		t.Fatalf("resultErr.Code = %q, want generic non-model_not_found code", resultErr.Code)
	}

	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-codex-generic-404", Provider: "codex"}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	model := "gpt-5.5"
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error:    resultErr,
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to be registered")
	}
	state := updated.ModelStates[model]
	if state != nil && state.LastError != nil && state.LastError.Code == "model_not_found" {
		t.Fatalf("generic 404 should not have model_not_found error code, got %v", state.LastError.Code)
	}
}

func TestCodexTerminalEvent_EndToEndCooldownAndAffinity(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	selector := NewSessionAffinitySelector(&RoundRobinSelector{})
	defer selector.Stop()

	auth1 := &Auth{ID: "auth-e2e-1", Provider: "codex"}
	auth2 := &Auth{ID: "auth-e2e-2", Provider: "codex"}
	candidates := []*Auth{auth1, auth2}

	opts := cliproxyexecutor.Options{
		Headers: http.Header{"X-Session-Id": []string{"session-e2e-codex-model-not-found"}},
	}

	picked, errPick := selector.Pick(context.Background(), "codex", "gpt-5.5", opts, candidates)
	if errPick != nil || picked == nil || picked.ID != "auth-e2e-1" {
		t.Fatalf("initial pick failed: %v", errPick)
	}

	selector.OnResult(Result{
		AuthID:   picked.ID,
		Provider: "codex",
		Model:    "gpt-5.5",
		Success:  true,
		Options:  opts,
	})

	m := NewManager(nil, nil, nil)
	if _, errReg := m.Register(context.Background(), auth1); errReg != nil {
		t.Fatalf("register auth1: %v", errReg)
	}
	if _, errReg := m.Register(context.Background(), auth2); errReg != nil {
		t.Fatalf("register auth2: %v", errReg)
	}

	rawErr := &statusBearingError{
		status: http.StatusNotFound,
		msg:    `{"error":{"type":"invalid_request_error","code":"model_not_found","message":"The model gpt-5.5 does not exist or you do not have access to it."}}`,
	}
	resultErr := resultErrorFromError(rawErr)
	if resultErr == nil || resultErr.Code != "model_not_found" {
		t.Fatalf("expected preserved model_not_found code, got %#v", resultErr)
	}

	res := Result{
		AuthID:   auth1.ID,
		Provider: auth1.Provider,
		Model:    "gpt-5.5",
		Success:  false,
		Error:    resultErr,
		Options:  opts,
	}

	selector.OnResult(res)
	m.MarkResult(context.Background(), res)

	updated, ok := m.GetByID(auth1.ID)
	if !ok || updated == nil {
		t.Fatal("auth1 missing")
	}
	state := updated.ModelStates["gpt-5.5"]
	if state == nil || !state.Unavailable {
		t.Fatalf("expected model gpt-5.5 to be cooling down, got %#v", state)
	}
	if state.LastError == nil || state.LastError.Code != "model_not_found" {
		t.Fatalf("expected LastError code model_not_found, got %#v", state.LastError)
	}

	reverseCandidates := []*Auth{auth2, auth1}
	nextPicked, errNextPick := selector.Pick(context.Background(), "codex", "gpt-5.5", opts, reverseCandidates)
	if errNextPick != nil || nextPicked == nil {
		t.Fatalf("second pick failed: %v", errNextPick)
	}
	if nextPicked.ID != "auth-e2e-2" {
		t.Fatalf("affinity was not released: got %s, want auth-e2e-2", nextPicked.ID)
	}
}

func TestCodexStructuredModelNotFound_DisableCooling(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-codex-disable-cooling",
		Provider: "codex",
		Metadata: map[string]any{
			"disable_cooling": true,
		},
	}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	rawErr := &statusBearingError{
		status: http.StatusBadRequest,
		msg:    `{"error":{"type":"invalid_request_error","code":"model_not_found","message":"The model gpt-5.5 does not exist or you do not have access to it."}}`,
	}
	resultErr := resultErrorFromError(rawErr)

	model := "gpt-5.5"
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error:    resultErr,
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to be registered")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatal("expected model state to be present")
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to be zero when disable_cooling=true, got %v", state.NextRetryAfter)
	}
}

// A structured 404 names the alias-resolved upstream URL model, which reaches
// neither the route key (Model) nor the final wire model (UpstreamModel) when a
// payload rule rewrote the body model in between (#5476 review on 4d654dc0).
// The alias-resolved name is part of the attempted set and must classify as an
// explicit model-not-found so the model keeps the 12-hour support cooldown.
func TestCodexStructuredModelNotFound_NamesAliasResolvedUpstreamModel(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		CodexKey: []internalconfig.CodexKey{{
			APIKey: "codex-key",
			Models: []internalconfig.CodexModel{{Name: "glm-5.2", Alias: "sonnet-route"}},
		}},
	})

	auth := &Auth{ID: "auth-codex-alias", Provider: "codex", Attributes: map[string]string{"api_key": "codex-key"}}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// Structured 404 with a not-found type but no model_not_found code: the
	// URL model name only matches from the message, so the classification must
	// come from comparing the attempted model names against that message.
	resultErr := &Error{
		HTTPStatus: http.StatusNotFound,
		Message:    `{"type":"not_found_error","message":"model glm-5.2 was not found"}`,
	}

	m.MarkResult(context.Background(), Result{
		AuthID:        auth.ID,
		Provider:      auth.Provider,
		Model:         "sonnet-route",
		UpstreamModel: "gpt-5.5-override",
		Success:       false,
		Error:         resultErr,
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to be registered")
	}
	state := updated.ModelStates["sonnet-route"]
	if state == nil || !state.Unavailable {
		t.Fatalf("expected model state to be unavailable, got %#v", state)
	}
	remaining := time.Until(state.NextRetryAfter)
	if remaining < 11*time.Hour || remaining > 13*time.Hour {
		t.Fatalf("404 naming the alias-resolved model must give ~12h cooldown, got remaining=%v", remaining)
	}
}

// The auth-selection override puts a third name in play: selection used A, the
// count-token URL carried the selection-resolved E, and a payload override
// finalized the body to X — so a structured 404 quoting E matches neither
// Model nor UpstreamModel. E rides in the result's own options metadata and
// must join the attempted-name set (#5514 review on e8b81691).
func TestCodexStructuredModelNotFound_NamesSelectionOverrideModel(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-codex-selection", Provider: "codex"}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	resultErr := &Error{
		HTTPStatus: http.StatusNotFound,
		Message:    `{"type":"not_found_error","message":"model exec-e-5.2 was not found"}`,
	}

	m.MarkResult(context.Background(), Result{
		AuthID:        auth.ID,
		Provider:      auth.Provider,
		Model:         "route-a",
		UpstreamModel: "wire-x",
		Options: cliproxyexecutor.Options{
			Metadata: map[string]any{cliproxyexecutor.AuthSelectionModelMetadataKey: "exec-e-5.2"},
		},
		Success: false,
		Error:   resultErr,
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to be registered")
	}
	state := updated.ModelStates["route-a"]
	if state == nil || !state.Unavailable {
		t.Fatalf("expected model state to be unavailable, got %#v", state)
	}
	remaining := time.Until(state.NextRetryAfter)
	if remaining < 11*time.Hour || remaining > 13*time.Hour {
		t.Fatalf("404 naming the selection-resolved model must give ~12h cooldown, got remaining=%v", remaining)
	}
}
