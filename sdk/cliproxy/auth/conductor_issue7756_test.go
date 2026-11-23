package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

type issue7756HookStore struct {
	onSave func(ctx context.Context, auth *Auth)
}

func (s *issue7756HookStore) List(context.Context) ([]*Auth, error) {
	return nil, nil
}

func (s *issue7756HookStore) Save(ctx context.Context, auth *Auth) (string, error) {
	if s != nil && s.onSave != nil {
		s.onSave(ctx, auth)
	}
	if auth == nil {
		return "", nil
	}
	return auth.ID, nil
}

func (s *issue7756HookStore) Delete(context.Context, string) error {
	return nil
}

type issue7756DummyExecutor struct {
	id string
}

func (e *issue7756DummyExecutor) Identifier() string { return e.id }

func (e *issue7756DummyExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *issue7756DummyExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *issue7756DummyExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *issue7756DummyExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *issue7756DummyExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestReconcileRegistryModelStates_PreservesTOSViolationDisabledStatus_Issue7756(t *testing.T) {
	ctx := context.Background()
	reg := registry.GetGlobalRegistry()

	t.Run("TOS_VIOLATION suspended auth with dropped model keeps StatusDisabled", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		authID := "issue7756-reconcile-tos-cred"
		droppedModel := "issue7756-gemini-3-pro"
		keptModel := "issue7756-gemini-3-flash"

		reg.RegisterClient(authID, "antigravity", []*registry.ModelInfo{
			{ID: droppedModel},
			{ID: keptModel},
		})
		t.Cleanup(func() { reg.UnregisterClient(authID) })

		if _, errReg := manager.Register(ctx, &Auth{
			ID:       authID,
			Provider: "antigravity",
			Status:   StatusActive,
		}); errReg != nil {
			t.Fatalf("Register error = %v", errReg)
		}

		manager.MarkResult(ctx, Result{
			AuthID:          authID,
			Provider:        "antigravity",
			Model:           droppedModel,
			Success:         false,
			CredentialScope: true,
			Error: &Error{
				HTTPStatus: http.StatusForbidden,
				Code:       "tos_violation",
				Message:    "TOS_VIOLATION",
				Metadata: map[string]string{
					"tos_appeal_url":      "https://forms.gle/example",
					"tos_appeal_url_text": "Submit Appeal",
				},
			},
		})

		before, okBefore := manager.GetByID(authID)
		if !okBefore || before == nil {
			t.Fatal("auth missing after MarkResult")
		}
		if !before.Disabled || before.Status != StatusDisabled || before.StatusMessage != "tos_violation" || before.LastError == nil || !before.Unavailable {
			t.Fatalf("unexpected precondition before reconcile: disabled=%v status=%q msg=%q err=%v unavailable=%v",
				before.Disabled, before.Status, before.StatusMessage, before.LastError, before.Unavailable)
		}

		// Drop the suspended model from the registry so reconciliation prunes its ModelState.
		reg.RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: keptModel}})
		manager.ReconcileRegistryModelStates(ctx, authID)

		after, okAfter := manager.GetByID(authID)
		if !okAfter || after == nil {
			t.Fatal("auth missing after ReconcileRegistryModelStates")
		}
		if after.Status != StatusDisabled {
			t.Fatalf("auth.Status = %q, want %q", after.Status, StatusDisabled)
		}
		if !after.Disabled {
			t.Fatal("auth.Disabled = false, want true")
		}
		if !after.Unavailable {
			t.Fatal("auth.Unavailable = false, want true")
		}
		if after.StatusMessage != "tos_violation" {
			t.Fatalf("auth.StatusMessage = %q, want %q", after.StatusMessage, "tos_violation")
		}
		if after.LastError == nil || after.LastError.Code != "tos_violation" {
			t.Fatalf("auth.LastError = %+v, want preserved tos_violation error", after.LastError)
		}
	})

	t.Run("StatusDisabled without Disabled flag also stays StatusDisabled", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		authID := "issue7756-reconcile-status-disabled-only"
		droppedModel := "issue7756-dropped-status-only"
		keptModel := "issue7756-kept-status-only"

		reg.RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: keptModel}})
		t.Cleanup(func() { reg.UnregisterClient(authID) })

		if _, errReg := manager.Register(ctx, &Auth{
			ID:            authID,
			Provider:      "antigravity",
			Disabled:      false,
			Status:        StatusDisabled,
			StatusMessage: "tos_violation",
			LastError:     &Error{HTTPStatus: http.StatusForbidden, Code: "tos_violation", Message: "TOS_VIOLATION"},
			ModelStates: map[string]*ModelState{
				droppedModel: {Status: StatusActive},
			},
		}); errReg != nil {
			t.Fatalf("Register error = %v", errReg)
		}

		manager.ReconcileRegistryModelStates(ctx, authID)

		after, okAfter := manager.GetByID(authID)
		if !okAfter || after == nil {
			t.Fatal("auth missing after ReconcileRegistryModelStates")
		}
		if after.Status != StatusDisabled {
			t.Fatalf("auth.Status = %q, want %q", after.Status, StatusDisabled)
		}
		if after.StatusMessage != "tos_violation" {
			t.Fatalf("auth.StatusMessage = %q, want %q", after.StatusMessage, "tos_violation")
		}
		if after.LastError == nil || after.LastError.Code != "tos_violation" {
			t.Fatalf("auth.LastError = %+v, want preserved tos_violation error", after.LastError)
		}
	})
}

func TestReconcileRegistryModelStates_PreservesTerminal401Status_Issue7756(t *testing.T) {
	ctx := context.Background()
	reg := registry.GetGlobalRegistry()
	manager := NewManager(nil, nil, nil)
	authID := "issue7756-reconcile-terminal-401"
	droppedModel := "issue7756-401-dropped"
	keptModel := "issue7756-401-kept"

	reg.RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: keptModel}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	if _, errReg := manager.Register(ctx, &Auth{
		ID:            authID,
		Provider:      "antigravity",
		Status:        StatusError,
		StatusMessage: "unauthorized",
		Unavailable:   true,
		LastError:     &Error{HTTPStatus: http.StatusUnauthorized, Code: "unauthorized", Message: "token revoked"},
		ModelStates: map[string]*ModelState{
			droppedModel: {Status: StatusActive},
			keptModel:    {Status: StatusActive},
		},
	}); errReg != nil {
		t.Fatalf("Register error = %v", errReg)
	}

	before, _ := manager.GetByID(authID)
	if !hasUnauthorizedAuthFailure(before) {
		t.Fatalf("precondition failed: expected hasUnauthorizedAuthFailure=true for %+v", before)
	}

	manager.ReconcileRegistryModelStates(ctx, authID)

	after, okAfter := manager.GetByID(authID)
	if !okAfter || after == nil {
		t.Fatal("auth missing after ReconcileRegistryModelStates")
	}
	if !hasUnauthorizedAuthFailure(after) {
		t.Fatalf("hasUnauthorizedAuthFailure = false after reconcile: status=%q msg=%q err=%+v unavailable=%v",
			after.Status, after.StatusMessage, after.LastError, after.Unavailable)
	}
	if after.Status != StatusError {
		t.Fatalf("auth.Status = %q, want %q", after.Status, StatusError)
	}
	if after.StatusMessage != "unauthorized" {
		t.Fatalf("auth.StatusMessage = %q, want %q", after.StatusMessage, "unauthorized")
	}
	if after.LastError == nil || after.LastError.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("auth.LastError = %+v, want preserved 401 error", after.LastError)
	}
}

func TestPublishOAuthModelAliasAndReconcileStates_PreservesTOSViolationDisabledStatus_Issue7756(t *testing.T) {
	ctx := context.Background()
	reg := registry.GetGlobalRegistry()

	t.Run("first reconciliation pass preserves StatusDisabled and TOS metadata", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		authID := "issue7756-alias-pass1-tos"
		upstreamModel := "issue7756-alias-upstream"
		aliasModel := "issue7756-alias-legacy"

		reg.RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: upstreamModel}})
		t.Cleanup(func() { reg.UnregisterClient(authID) })

		if _, errReg := manager.Register(ctx, &Auth{
			ID:       authID,
			Provider: "antigravity",
			Status:   StatusActive,
		}); errReg != nil {
			t.Fatalf("Register error = %v", errReg)
		}
		manager.MarkResult(ctx, Result{
			AuthID:   authID,
			Provider: "antigravity",
			Model:    aliasModel,
			Success:  true,
		})
		manager.MarkResult(ctx, Result{
			AuthID:          authID,
			Provider:        "antigravity",
			Success:         false,
			CredentialScope: true,
			Error:           &Error{HTTPStatus: http.StatusForbidden, Code: "tos_violation", Message: "TOS_VIOLATION"},
		})
		before, _ := manager.GetByID(authID)
		if before == nil || !before.Disabled || before.Status != StatusDisabled || before.StatusMessage != "tos_violation" || before.LastError == nil {
			t.Fatalf("unexpected precondition before alias reload: %+v", before)
		}

		manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
			"antigravity": {{Name: upstreamModel, Alias: aliasModel}},
		})

		after, okAfter := manager.GetByID(authID)
		if !okAfter || after == nil {
			t.Fatal("auth missing after SetOAuthModelAlias")
		}
		if after.Status != StatusDisabled {
			t.Fatalf("auth.Status = %q, want %q", after.Status, StatusDisabled)
		}
		if !after.Disabled {
			t.Fatal("auth.Disabled = false, want true")
		}
		if after.Unavailable != before.Unavailable {
			t.Fatalf("auth.Unavailable = %v, want preserved %v", after.Unavailable, before.Unavailable)
		}
		if after.StatusMessage != "tos_violation" {
			t.Fatalf("auth.StatusMessage = %q, want %q", after.StatusMessage, "tos_violation")
		}
		if after.LastError == nil || after.LastError.Code != "tos_violation" {
			t.Fatalf("auth.LastError = %+v, want preserved tos_violation error", after.LastError)
		}
	})

	t.Run("post-persist second reconciliation pass preserves StatusDisabled and TOS metadata", func(t *testing.T) {
		authID := "issue7756-alias-pass2-tos"
		upstreamModel := "issue7756-alias-pass2-upstream"
		aliasModel1 := "issue7756-alias-pass2-legacy1"
		aliasModel2 := "issue7756-alias-pass2-legacy2"

		var manager *Manager
		var injected atomic.Bool
		var armed atomic.Bool
		store := &issue7756HookStore{
			onSave: func(_ context.Context, saved *Auth) {
				if saved == nil || saved.ID != authID || !armed.Load() {
					return
				}
				// Persist hook runs inside publishOAuthModelAliasAndReconcileStates while m.mu is unlocked.
				if injected.CompareAndSwap(false, true) {
					manager.mu.Lock()
					if current := manager.auths[authID]; current != nil {
						current.ModelStates = map[string]*ModelState{
							aliasModel2: {Status: StatusActive},
						}
					}
					manager.mu.Unlock()
				}
			},
		}
		manager = NewManager(store, nil, nil)

		reg.RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: upstreamModel}})
		t.Cleanup(func() { reg.UnregisterClient(authID) })

		if _, errReg := manager.Register(ctx, &Auth{
			ID:       authID,
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{"type": "antigravity"},
		}); errReg != nil {
			t.Fatalf("Register error = %v", errReg)
		}
		manager.MarkResult(ctx, Result{
			AuthID:   authID,
			Provider: "antigravity",
			Model:    aliasModel1,
			Success:  true,
		})
		manager.MarkResult(ctx, Result{
			AuthID:          authID,
			Provider:        "antigravity",
			Success:         false,
			CredentialScope: true,
			Error:           &Error{HTTPStatus: http.StatusForbidden, Code: "tos_violation", Message: "TOS_VIOLATION"},
		})
		before, _ := manager.GetByID(authID)
		if before == nil || !before.Disabled || before.Status != StatusDisabled || before.StatusMessage != "tos_violation" || before.LastError == nil {
			t.Fatalf("unexpected precondition before alias reload: %+v", before)
		}
		armed.Store(true)

		manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
			"antigravity": {
				{Name: upstreamModel, Alias: aliasModel1},
				{Name: upstreamModel, Alias: aliasModel2},
			},
		})

		if !injected.Load() {
			t.Fatal("test setup failed: second-pass alias state was not injected during persistLocked")
		}

		after, okAfter := manager.GetByID(authID)
		if !okAfter || after == nil {
			t.Fatal("auth missing after SetOAuthModelAlias")
		}
		if len(after.ModelStates) != 0 {
			t.Fatalf("expected second-pass alias migration to prune %q, got ModelStates=%+v", aliasModel2, after.ModelStates)
		}
		if after.Status != StatusDisabled {
			t.Fatalf("auth.Status = %q, want %q", after.Status, StatusDisabled)
		}
		if !after.Disabled {
			t.Fatal("auth.Disabled = false, want true")
		}
		if after.Unavailable != before.Unavailable {
			t.Fatalf("auth.Unavailable = %v, want preserved %v", after.Unavailable, before.Unavailable)
		}
		if after.StatusMessage != "tos_violation" {
			t.Fatalf("auth.StatusMessage = %q, want %q", after.StatusMessage, "tos_violation")
		}
		if after.LastError == nil || after.LastError.Code != "tos_violation" {
			t.Fatalf("auth.LastError = %+v, want preserved tos_violation error", after.LastError)
		}
	})
}

func TestSelectionLoops_ExcludeStatusDisabledWithoutDisabledFlag_Issue7756(t *testing.T) {
	ctx := context.Background()
	reg := registry.GetGlobalRegistry()

	t.Run("pickNextLegacy and pickNextMixedLegacy ignore StatusDisabled for error classification", func(t *testing.T) {
		manager := NewManager(nil, &RoundRobinSelector{}, nil)
		provider := "issue7756-legacy-provider"
		model := "issue7756-legacy-model"
		manager.RegisterExecutor(&issue7756DummyExecutor{id: provider})

		disabledID := "issue7756-legacy-status-disabled"
		coolingID := "issue7756-legacy-cooling"
		reg.RegisterClient(disabledID, provider, []*registry.ModelInfo{{ID: model}})
		reg.RegisterClient(coolingID, provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() {
			reg.UnregisterClient(disabledID)
			reg.UnregisterClient(coolingID)
		})

		if _, errReg := manager.Register(ctx, &Auth{
			ID:       disabledID,
			Provider: provider,
			Disabled: false,
			Status:   StatusDisabled,
		}); errReg != nil {
			t.Fatalf("Register disabled auth error = %v", errReg)
		}

		// With only a StatusDisabled (Disabled: false) auth, both legacy pickers must return auth_not_found.
		if _, _, errPick := manager.pickNextLegacy(ctx, provider, model, cliproxyexecutor.Options{}, nil); !isAuthNotFoundError(errPick) {
			t.Fatalf("pickNextLegacy with only StatusDisabled auth = %v, want auth_not_found", errPick)
		}
		if _, _, _, errPick := manager.pickNextMixedLegacy(ctx, []string{provider}, model, cliproxyexecutor.Options{}, nil); !isAuthNotFoundError(errPick) {
			t.Fatalf("pickNextMixedLegacy with only StatusDisabled auth = %v, want auth_not_found", errPick)
		}

		// Pair with a cooling-down active auth: both legacy pickers must return *modelCooldownError (not downgraded to generic auth_unavailable).
		recoverAt := time.Now().Add(10 * time.Minute)
		if _, errReg := manager.Register(ctx, &Auth{
			ID:       coolingID,
			Provider: provider,
			Status:   StatusActive,
			ModelStates: map[string]*ModelState{
				model: {
					Status:         StatusError,
					Unavailable:    true,
					NextRetryAfter: recoverAt,
					Quota: QuotaState{
						Exceeded:      true,
						Reason:        "quota",
						NextRecoverAt: recoverAt,
					},
				},
			},
		}); errReg != nil {
			t.Fatalf("Register cooling auth error = %v", errReg)
		}

		_, _, errSingle := manager.pickNextLegacy(ctx, provider, model, cliproxyexecutor.Options{}, nil)
		var singleCooldown *modelCooldownError
		if !errors.As(errSingle, &singleCooldown) {
			t.Fatalf("pickNextLegacy error = %T (%v), want *modelCooldownError", errSingle, errSingle)
		}

		_, _, _, errMixed := manager.pickNextMixedLegacy(ctx, []string{provider}, model, cliproxyexecutor.Options{}, nil)
		var mixedCooldown *modelCooldownError
		if !errors.As(errMixed, &mixedCooldown) {
			t.Fatalf("pickNextMixedLegacy error = %T (%v), want *modelCooldownError", errMixed, errMixed)
		}
	})

	t.Run("pickNext and pickNextMixed skip StatusDisabled when checking fast-path bailout", func(t *testing.T) {
		rr := &RoundRobinSelector{}
		manager := NewManager(nil, rr, nil)
		provider := "issue7756-fastpath-provider"
		otherProvider := "issue7756-fastpath-other"
		model := "issue7756-fastpath-model"
		manager.RegisterExecutor(&issue7756DummyExecutor{id: provider})
		manager.RegisterExecutor(&issue7756DummyExecutor{id: otherProvider})

		activeID := "issue7756-fastpath-active"
		disabledID := "issue7756-fastpath-status-disabled"
		reg.RegisterClient(activeID, provider, []*registry.ModelInfo{{ID: model}})
		reg.RegisterClient(disabledID, provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() {
			reg.UnregisterClient(activeID)
			reg.UnregisterClient(disabledID)
		})

		if _, errReg := manager.Register(ctx, &Auth{
			ID:       activeID,
			Provider: provider,
			Status:   StatusActive,
		}); errReg != nil {
			t.Fatalf("Register active auth error = %v", errReg)
		}

		disabledAuth := &Auth{
			ID:       disabledID,
			Provider: provider,
			Disabled: false,
			Status:   StatusDisabled,
		}
		// Give the StatusDisabled auth a per-auth alias so routeAwareSelectionRequired
		// returns true if pickNext/pickNextMixed fail to skip it.
		SetOAuthModelAliasesAttribute(disabledAuth, []internalconfig.OAuthModelAlias{
			{Name: "issue7756-upstream-only", Alias: model},
		})
		if _, errReg := manager.Register(ctx, disabledAuth); errReg != nil {
			t.Fatalf("Register disabled auth error = %v", errReg)
		}
		manager.RefreshSchedulerAll()

		selectedSingle, _, errSingle := manager.pickNext(ctx, provider, model, cliproxyexecutor.Options{}, nil)
		if errSingle != nil || selectedSingle == nil || selectedSingle.ID != activeID {
			t.Fatalf("pickNext = (%v, %v), want auth %q", selectedSingle, errSingle, activeID)
		}
		rr.mu.Lock()
		singleFallbackCalls := len(rr.lastPicked)
		rr.mu.Unlock()
		if singleFallbackCalls != 0 {
			t.Fatalf("pickNext unexpectedly fell back to pickNextLegacy due to StatusDisabled auth (rr.lastPicked=%d)", singleFallbackCalls)
		}

		selectedMixed, _, _, errMixed := manager.pickNextMixed(ctx, []string{provider, otherProvider}, model, cliproxyexecutor.Options{}, nil)
		if errMixed != nil || selectedMixed == nil || selectedMixed.ID != activeID {
			t.Fatalf("pickNextMixed = (%v, %v), want auth %q", selectedMixed, errMixed, activeID)
		}
		rr.mu.Lock()
		mixedFallbackCalls := len(rr.lastPicked)
		rr.mu.Unlock()
		if mixedFallbackCalls != 0 {
			t.Fatalf("pickNextMixed unexpectedly fell back to pickNextMixedLegacy due to StatusDisabled auth (rr.lastPicked=%d)", mixedFallbackCalls)
		}
	})

	t.Run("HasAlternativeTargetForModel, earliestPoolCooldownForModelAt, and warnLogAuthUnavailable exclude StatusDisabled", func(t *testing.T) {
		hook := setupTestLoggerHook(t)
		manager := NewManager(nil, &RoundRobinSelector{}, nil)
		provider := "issue7756-inspect-provider"
		model := "issue7756-inspect-model"
		manager.RegisterExecutor(&issue7756DummyExecutor{id: provider})

		excludedID := "issue7756-inspect-excluded"
		disabledID := "issue7756-inspect-status-disabled"
		reg.RegisterClient(excludedID, provider, []*registry.ModelInfo{{ID: model}})
		reg.RegisterClient(disabledID, provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() {
			reg.UnregisterClient(excludedID)
			reg.UnregisterClient(disabledID)
		})

		now := time.Now()
		recoverAt := now.Add(5 * time.Minute)
		if _, errReg := manager.Register(ctx, &Auth{
			ID:       excludedID,
			Provider: provider,
			Status:   StatusActive,
			ModelStates: map[string]*ModelState{
				model: {
					Status:         StatusError,
					Unavailable:    true,
					NextRetryAfter: recoverAt,
					Quota: QuotaState{
						Exceeded:      true,
						Reason:        "quota",
						NextRecoverAt: recoverAt,
					},
				},
			},
		}); errReg != nil {
			t.Fatalf("Register cooling auth error = %v", errReg)
		}

		if _, errReg := manager.Register(ctx, &Auth{
			ID:             disabledID,
			Provider:       provider,
			Disabled:       false,
			Status:         StatusDisabled,
			Unavailable:    true,
			NextRetryAfter: now.Add(time.Minute),
		}); errReg != nil {
			t.Fatalf("Register disabled auth error = %v", errReg)
		}

		if manager.HasAlternativeTargetForModel(ctx, []string{provider}, model, cliproxyexecutor.Options{}, excludedID) {
			t.Fatal("HasAlternativeTargetForModel = true, want false when only alternative is StatusDisabled")
		}

		earliest, hasUnblocked, okCooldown := manager.earliestPoolCooldownForModelAt(ctx, []string{provider}, model, cliproxyexecutor.Options{}, now)
		if !okCooldown || hasUnblocked || !earliest.Equal(recoverAt) {
			t.Fatalf("earliestPoolCooldownForModelAt = (%v, %v, %v), want (%v, false, true)", earliest, hasUnblocked, okCooldown, recoverAt)
		}

		manager.warnLogAuthUnavailable(ctx, []string{provider}, model, cliproxyexecutor.Options{}, nil, &Error{Code: "auth_unavailable"})
		foundWarn := false
		for _, entry := range hook.AllEntries() {
			if entry.Level == log.WarnLevel && strings.Contains(entry.Message, model) {
				foundWarn = true
				if !strings.Contains(entry.Message, "1 of 1 candidate(s)") {
					t.Fatalf("warnLogAuthUnavailable counted StatusDisabled auth as candidate: %q", entry.Message)
				}
			}
		}
		if !foundWarn {
			t.Fatalf("expected warnLogAuthUnavailable log entry for model %q, got %#v", model, hook.AllEntries())
		}
	})
}
