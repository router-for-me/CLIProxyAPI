package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type issue7814RefreshExecutor struct {
	provider  string
	refreshFn func(ctx context.Context, auth *Auth) (*Auth, error)
}

func (e *issue7814RefreshExecutor) Identifier() string { return e.provider }
func (e *issue7814RefreshExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *issue7814RefreshExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (e *issue7814RefreshExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	if e.refreshFn != nil {
		return e.refreshFn(ctx, auth)
	}
	return auth, nil
}
func (e *issue7814RefreshExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *issue7814RefreshExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestManager_Issue7814_MarkResult_Non429CredentialScopeBlocksSiblingModels(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	ctx := context.Background()
	cdsDir := t.TempDir()
	mgr := NewManager(nil, &RoundRobinSelector{}, nil)
	mgr.SetCooldownStateStore(NewFileCooldownStateStore(cdsDir))

	cred := &Auth{
		ID:       "auth-issue7814-cred-403",
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "tok-1",
			"refresh_token": "ref-1",
			"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
	registry.GetGlobalRegistry().RegisterClient(cred.ID, "antigravity", []*registry.ModelInfo{
		{ID: "gemini-3-flash"},
		{ID: "claude-sonnet-4-6"},
	})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(cred.ID)
	})
	if _, errReg := mgr.Register(WithSkipPersist(ctx), cred); errReg != nil {
		t.Fatalf("Register() error = %v", errReg)
	}

	// Warm up both scheduler shards.
	for _, model := range []string{"gemini-3-flash", "claude-sonnet-4-6"} {
		if _, errPick := mgr.scheduler.pickSingle(ctx, "antigravity", model, cliproxyexecutor.Options{}, nil); errPick != nil {
			t.Fatalf("warm pickSingle(%s) error = %v", model, errPick)
		}
	}

	before := time.Now()
	mgr.MarkResult(ctx, Result{
		AuthID:          cred.ID,
		Provider:        "antigravity",
		Model:           "gemini-3-flash",
		Success:         false,
		CredentialScope: true,
		Error: &Error{
			HTTPStatus: http.StatusForbidden,
			Message:    `{"error":{"code":403,"status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"VALIDATION_REQUIRED"}]}}`,
		},
	})

	snap, ok := mgr.GetByID(cred.ID)
	if !ok || snap == nil {
		t.Fatal("credential missing after MarkResult")
	}
	checkAt := before.Add(5 * time.Minute)
	if IsAuthAvailableForModel(snap, "gemini-3-flash", checkAt) {
		t.Fatal("expected gemini-3-flash to be blocked")
	}
	if IsAuthAvailableForModel(snap, "claude-sonnet-4-6", checkAt) {
		t.Fatal("expected sibling claude-sonnet-4-6 to be blocked after credential-scoped 403")
	}
	if _, errPick := mgr.Selector().Pick(ctx, "antigravity", "claude-sonnet-4-6", cliproxyexecutor.Options{}, []*Auth{snap}); errPick == nil {
		t.Fatal("Selector.Pick(claude-sonnet-4-6) = nil error, want blocked")
	}
	if _, errSched := mgr.scheduler.pickSingle(ctx, "antigravity", "claude-sonnet-4-6", cliproxyexecutor.Options{}, nil); errSched == nil {
		t.Fatal("scheduler.pickSingle(claude-sonnet-4-6) = nil error, want blocked")
	}
	proj := mgr.clientModelProjectionForAuth(snap, "claude-sonnet-4-6", checkAt)
	if !proj.Suspended || proj.SuspendUntil.IsZero() {
		t.Fatalf("clientModelProjectionForAuth(claude-sonnet-4-6) = %+v, want Suspended=true with SuspendUntil", proj)
	}
	afterExpiry := before.Add(31 * time.Minute)
	if !IsAuthAvailableForModel(snap, "gemini-3-flash", afterExpiry) || !IsAuthAvailableForModel(snap, "claude-sonnet-4-6", afterExpiry) {
		t.Fatal("expected both models available after 30m credential cooldown expires")
	}

	// Persisted cooldown state must preserve sibling blocking across restart.
	mgr.PersistCooldownStates(ctx)
	mgr2 := NewManager(nil, &RoundRobinSelector{}, nil)
	mgr2.SetCooldownStateStore(NewFileCooldownStateStore(cdsDir))
	cred2 := &Auth{
		ID:       cred.ID,
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "tok-1",
			"refresh_token": "ref-1",
			"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
	if _, errReg := mgr2.Register(WithSkipPersist(ctx), cred2); errReg != nil {
		t.Fatalf("Register m2 error = %v", errReg)
	}
	if errRestore := mgr2.RestoreCooldownStates(ctx); errRestore != nil {
		t.Fatalf("RestoreCooldownStates error = %v", errRestore)
	}
	restored, _ := mgr2.GetByID(cred.ID)
	if IsAuthAvailableForModel(restored, "claude-sonnet-4-6", checkAt) {
		t.Fatalf("expected sibling claude-sonnet-4-6 to stay blocked after RestoreCooldownStates: %+v", restored)
	}
}

func TestManager_Issue7814_Non429CredentialScopeDoesNotInheritModelLongCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	ctx := context.Background()
	mgr := NewManager(nil, nil, nil)
	cred := &Auth{ID: "auth-issue7814-no-inherit", Provider: "antigravity", Status: StatusActive}
	if _, errReg := mgr.Register(WithSkipPersist(ctx), cred); errReg != nil {
		t.Fatalf("Register() error = %v", errReg)
	}

	// Model-level 2h quota cooldown on gemini-3-flash.
	longRetry := 2 * time.Hour
	mgr.MarkResult(ctx, Result{
		AuthID:     cred.ID,
		Provider:   "antigravity",
		Model:      "gemini-3-flash",
		Success:    false,
		RetryAfter: &longRetry,
		Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "model quota"},
	})

	// Credential-scoped 403 VALIDATION_REQUIRED (30m) on gemini-3-flash.
	before403 := time.Now()
	mgr.MarkResult(ctx, Result{
		AuthID:          cred.ID,
		Provider:        "antigravity",
		Model:           "gemini-3-flash",
		Success:         false,
		CredentialScope: true,
		Error:           &Error{HTTPStatus: http.StatusForbidden, Message: "VALIDATION_REQUIRED"},
	})

	snap, _ := mgr.GetByID(cred.ID)
	if IsAuthAvailableForModel(snap, "claude-sonnet-4-6", before403.Add(10*time.Minute)) {
		t.Fatal("expected sibling claude-sonnet-4-6 blocked during 30m 403 cooldown")
	}
	if !IsAuthAvailableForModel(snap, "claude-sonnet-4-6", before403.Add(35*time.Minute)) {
		t.Fatalf("sibling claude-sonnet-4-6 inherited 2h model cooldown: credentialRetryAfter=%v", snap.credentialRetryAfter)
	}
	if IsAuthAvailableForModel(snap, "gemini-3-flash", before403.Add(35*time.Minute)) {
		t.Fatal("expected gemini-3-flash to retain its 2h model cooldown")
	}
}

func TestManager_Issue7814_RefreshClears401CredentialRetryAfterButPreserves403ValidationRequired(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	t.Run("refresh clears 401 credentialRetryAfter", func(t *testing.T) {
		ctx := context.Background()
		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr.RegisterExecutor(&issue7814RefreshExecutor{
			provider: "antigravity",
			refreshFn: func(_ context.Context, auth *Auth) (*Auth, error) {
				next := auth.Clone()
				if next.Metadata == nil {
					next.Metadata = map[string]any{}
				}
				next.Metadata["access_token"] = "tok-fresh-401"
				next.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
				return next, nil
			},
		})
		cred := &Auth{
			ID:       "auth-issue7814-refresh-401",
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{
				"access_token":  "tok-stale-401",
				"refresh_token": "ref-401",
				"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		}
		if _, errReg := mgr.Register(WithSkipPersist(ctx), cred); errReg != nil {
			t.Fatalf("Register() error = %v", errReg)
		}

		mgr.MarkResult(ctx, Result{
			AuthID:          cred.ID,
			Provider:        "antigravity",
			Model:           "gemini-3-flash",
			Success:         false,
			CredentialScope: true,
			AccessToken:     "tok-stale-401",
			Error:           &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
		})

		beforeRefresh, _ := mgr.GetByID(cred.ID)
		if IsAuthAvailableForModel(beforeRefresh, "claude-sonnet-4-6", time.Now()) {
			t.Fatal("expected sibling model blocked before refresh after credential-scoped 401")
		}

		if _, errRefresh := mgr.refreshAuthForRequest(ctx, cred.ID, "tok-stale-401"); errRefresh != nil {
			t.Fatalf("refreshAuthForRequest() error = %v", errRefresh)
		}

		afterRefresh, _ := mgr.GetByID(cred.ID)
		now := time.Now()
		if !IsAuthAvailableForModel(afterRefresh, "gemini-3-flash", now) || !IsAuthAvailableForModel(afterRefresh, "claude-sonnet-4-6", now) {
			t.Fatalf("expected credential available for all models after 401 refresh recovery: credRetry=%v nextRetry=%v unavail=%v",
				afterRefresh.credentialRetryAfter, afterRefresh.NextRetryAfter, afterRefresh.Unavailable)
		}
	})

	t.Run("refresh preserves 403 VALIDATION_REQUIRED credentialRetryAfter", func(t *testing.T) {
		ctx := context.Background()
		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr.RegisterExecutor(&issue7814RefreshExecutor{
			provider: "antigravity",
			refreshFn: func(_ context.Context, auth *Auth) (*Auth, error) {
				next := auth.Clone()
				if next.Metadata == nil {
					next.Metadata = map[string]any{}
				}
				next.Metadata["access_token"] = "tok-fresh-403"
				next.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
				return next, nil
			},
		})
		cred := &Auth{
			ID:       "auth-issue7814-refresh-403",
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{
				"access_token":  "tok-old-403",
				"refresh_token": "ref-403",
				"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		}
		if _, errReg := mgr.Register(WithSkipPersist(ctx), cred); errReg != nil {
			t.Fatalf("Register() error = %v", errReg)
		}

		mgr.MarkResult(ctx, Result{
			AuthID:          cred.ID,
			Provider:        "antigravity",
			Model:           "gemini-3-flash",
			Success:         false,
			CredentialScope: true,
			Error:           &Error{HTTPStatus: http.StatusForbidden, Message: "VALIDATION_REQUIRED"},
		})

		if _, errRefresh := mgr.refreshAuthForRequest(ctx, cred.ID, ""); errRefresh != nil {
			t.Fatalf("refreshAuthForRequest() error = %v", errRefresh)
		}

		afterRefresh, _ := mgr.GetByID(cred.ID)
		now := time.Now()
		if IsAuthAvailableForModel(afterRefresh, "claude-sonnet-4-6", now) {
			t.Fatalf("routine token refresh cleared 403 VALIDATION_REQUIRED credential cooldown: %+v", afterRefresh)
		}
		if !afterRefresh.Unavailable {
			t.Fatalf("expected auth.Unavailable=true to remain set after routine refresh while 403 VALIDATION_REQUIRED cooldown is active")
		}
	})

	t.Run("refresh clears 401 credentialRetryAfter without clearing shorter active credential_quota", func(t *testing.T) {
		ctx := context.Background()
		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr.RegisterExecutor(&issue7814RefreshExecutor{
			provider: "antigravity",
			refreshFn: func(_ context.Context, auth *Auth) (*Auth, error) {
				next := auth.Clone()
				if next.Metadata == nil {
					next.Metadata = map[string]any{}
				}
				next.Metadata["access_token"] = "tok-fresh-401-quota"
				next.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
				return next, nil
			},
		})
		cred := &Auth{
			ID:       "auth-issue7814-refresh-401-quota",
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{
				"access_token":  "tok-stale-401-quota",
				"refresh_token": "ref-401-quota",
				"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		}
		if _, errReg := mgr.Register(WithSkipPersist(ctx), cred); errReg != nil {
			t.Fatalf("Register() error = %v", errReg)
		}

		before := time.Now()
		shortQuota := 5 * time.Minute
		mgr.MarkResult(ctx, Result{
			AuthID:          cred.ID,
			Provider:        "antigravity",
			Model:           "gemini-3-flash",
			Success:         false,
			CredentialScope: true,
			RetryAfter:      &shortQuota,
			Error:           &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
		})
		mgr.MarkResult(ctx, Result{
			AuthID:          cred.ID,
			Provider:        "antigravity",
			Model:           "gemini-3-flash",
			Success:         false,
			CredentialScope: true,
			AccessToken:     "tok-stale-401-quota",
			Error:           &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
		})

		if _, errRefresh := mgr.refreshAuthForRequest(ctx, cred.ID, "tok-stale-401-quota"); errRefresh != nil {
			t.Fatalf("refreshAuthForRequest() error = %v", errRefresh)
		}

		afterRefresh, _ := mgr.GetByID(cred.ID)
		if IsAuthAvailableForModel(afterRefresh, "claude-sonnet-4-6", before.Add(2*time.Minute)) {
			t.Fatal("expected credential_quota to keep blocking sibling at +2m")
		}
		if IsAuthAvailableForModel(afterRefresh, "gemini-3-flash", before.Add(2*time.Minute)) {
			t.Fatal("expected credential_quota to keep blocking reporting model at +2m")
		}
		if !IsAuthAvailableForModel(afterRefresh, "claude-sonnet-4-6", before.Add(6*time.Minute)) {
			t.Fatalf("stale 401 credentialRetryAfter survived refresh past 5m credential_quota: credRetry=%v nextRetry=%v",
				afterRefresh.credentialRetryAfter, afterRefresh.NextRetryAfter)
		}
		if !IsAuthAvailableForModel(afterRefresh, "gemini-3-flash", before.Add(6*time.Minute)) {
			t.Fatalf("stale 401 model cooldown survived refresh past 5m credential_quota: modelState=%+v",
				afterRefresh.ModelStates["gemini-3-flash"])
		}
	})

	t.Run("refresh clears 401 shadow on wrapped sibling model without clearing shorter active credential_quota", func(t *testing.T) {
		ctx := context.Background()
		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr.RegisterExecutor(&issue7814RefreshExecutor{
			provider: "antigravity",
			refreshFn: func(_ context.Context, auth *Auth) (*Auth, error) {
				next := auth.Clone()
				if next.Metadata == nil {
					next.Metadata = map[string]any{}
				}
				next.Metadata["access_token"] = "tok-fresh-wrapped-shadow"
				next.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
				return next, nil
			},
		})
		cred := &Auth{
			ID:       "auth-issue7814-refresh-401-shadow",
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{
				"access_token":  "tok-stale-wrapped-shadow",
				"refresh_token": "ref-wrapped-shadow",
				"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		}
		if _, errReg := mgr.Register(WithSkipPersist(ctx), cred); errReg != nil {
			t.Fatalf("Register() error = %v", errReg)
		}

		before := time.Now()
		// Pre-seed gemini-3-pro ModelState so credential_quota wraps it, then record an
		// ordinary model-level 401 on gemini-3-pro so credentialQuotaShadow holds the 401.
		mgr.MarkResult(ctx, Result{
			AuthID:            cred.ID,
			Provider:          "antigravity",
			Model:             "gemini-3-pro",
			Success:           true,
			CompletedResponse: true,
		})
		shortQuota := 5 * time.Minute
		mgr.MarkResult(ctx, Result{
			AuthID:          cred.ID,
			Provider:        "antigravity",
			Model:           "gemini-3-flash",
			Success:         false,
			CredentialScope: true,
			RetryAfter:      &shortQuota,
			Error:           &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
		})
		mgr.MarkResult(ctx, Result{
			AuthID:          cred.ID,
			Provider:        "antigravity",
			Model:           "gemini-3-pro",
			Success:         false,
			CredentialScope: false,
			AccessToken:     "tok-stale-wrapped-shadow",
			Error:           &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
		})

		beforeRefresh, _ := mgr.GetByID(cred.ID)
		proBefore := beforeRefresh.ModelStates["gemini-3-pro"]
		if proBefore == nil || proBefore.credentialQuotaShadow == nil || !proBefore.credentialQuotaShadow.NextRetryAfter.After(before.Add(29*time.Minute)) {
			t.Fatalf("expected wrapped 30m 401 shadow on gemini-3-pro before refresh, got %+v", proBefore)
		}

		if _, errRefresh := mgr.refreshAuthForRequest(ctx, cred.ID, "tok-stale-wrapped-shadow"); errRefresh != nil {
			t.Fatalf("refreshAuthForRequest() error = %v", errRefresh)
		}

		afterRefresh, _ := mgr.GetByID(cred.ID)
		for _, model := range []string{"gemini-3-flash", "gemini-3-pro", "claude-sonnet-4-6"} {
			if IsAuthAvailableForModel(afterRefresh, model, before.Add(2*time.Minute)) {
				t.Fatalf("expected %s to remain blocked by 5m credential_quota at +2m", model)
			}
			if !IsAuthAvailableForModel(afterRefresh, model, before.Add(6*time.Minute)) {
				t.Fatalf("expected %s to recover at +6m once 5m credential_quota expired; state=%+v", model, afterRefresh.ModelStates[model])
			}
		}
	})

	t.Run("refresh clearing 401 shadow preserves active independent model force cooldown", func(t *testing.T) {
		ctx := context.Background()
		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr.RegisterExecutor(&issue7814RefreshExecutor{
			provider: "antigravity",
			refreshFn: func(_ context.Context, auth *Auth) (*Auth, error) {
				next := auth.Clone()
				if next.Metadata == nil {
					next.Metadata = map[string]any{}
				}
				next.Metadata["access_token"] = "tok-fresh-force-shadow"
				next.Metadata["expired"] = time.Now().Add(2 * time.Hour).Format(time.RFC3339)
				return next, nil
			},
		})
		cred := &Auth{
			ID:       "auth-issue7814-refresh-force-shadow",
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{
				"access_token":  "tok-stale-force-shadow",
				"refresh_token": "ref-force-shadow",
				"expired":       time.Now().Add(2 * time.Hour).Format(time.RFC3339),
			},
		}
		if _, errReg := mgr.Register(WithSkipPersist(ctx), cred); errReg != nil {
			t.Fatalf("Register() error = %v", errReg)
		}

		before := time.Now()
		// Pre-seed gemini-3-pro, apply 5m credential_quota, then 1h model-level forced 404,
		// then ordinary model-level 401 overwriting shadow.LastError.
		mgr.MarkResult(ctx, Result{
			AuthID:            cred.ID,
			Provider:          "antigravity",
			Model:             "gemini-3-pro",
			Success:           true,
			CompletedResponse: true,
		})
		shortQuota := 5 * time.Minute
		mgr.MarkResult(ctx, Result{
			AuthID:          cred.ID,
			Provider:        "antigravity",
			Model:           "gemini-3-flash",
			Success:         false,
			CredentialScope: true,
			RetryAfter:      &shortQuota,
			Error:           &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
		})
		forceDur := time.Hour
		mgr.MarkResult(ctx, Result{
			AuthID:          cred.ID,
			Provider:        "antigravity",
			Model:           "gemini-3-pro",
			Success:         false,
			CredentialScope: false,
			RetryAfter:      &forceDur,
			Error:           &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusNotFound, Message: "forced 404"},
		})
		mgr.MarkResult(ctx, Result{
			AuthID:          cred.ID,
			Provider:        "antigravity",
			Model:           "gemini-3-pro",
			Success:         false,
			CredentialScope: false,
			AccessToken:     "tok-stale-force-shadow",
			Error:           &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
		})

		beforeRefresh, _ := mgr.GetByID(cred.ID)
		proBefore := beforeRefresh.ModelStates["gemini-3-pro"]
		if proBefore == nil || proBefore.credentialQuotaShadow == nil || !proBefore.credentialQuotaShadow.forceCooldownUntil.After(before.Add(59*time.Minute)) {
			t.Fatalf("expected forced cooldown inside shadow before refresh, got %+v", proBefore)
		}
		expectedForceUntil := proBefore.credentialQuotaShadow.forceCooldownUntil

		if _, errRefresh := mgr.refreshAuthForRequest(ctx, cred.ID, "tok-stale-force-shadow"); errRefresh != nil {
			t.Fatalf("refreshAuthForRequest() error = %v", errRefresh)
		}

		afterRefresh, _ := mgr.GetByID(cred.ID)
		proAfter := afterRefresh.ModelStates["gemini-3-pro"]
		if proAfter == nil || proAfter.credentialQuotaShadow == nil || !proAfter.credentialQuotaShadow.forceCooldownUntil.Equal(expectedForceUntil) {
			t.Fatalf("expected shadow forceCooldownUntil=%v preserved after 401 refresh, got %+v", expectedForceUntil, proAfter)
		}
		if IsAuthAvailableForModel(afterRefresh, "gemini-3-pro", before.Add(6*time.Minute)) {
			t.Fatalf("expected gemini-3-pro forced shadow cooldown to survive 401 refresh at +6m; state=%+v", proAfter)
		}
		if IsAuthAvailableForModel(afterRefresh, "gemini-3-pro", expectedForceUntil.Add(-time.Minute)) {
			t.Fatalf("expected gemini-3-pro forced shadow cooldown to survive until %v; state=%+v", expectedForceUntil, proAfter)
		}
		if !IsAuthAvailableForModel(afterRefresh, "gemini-3-pro", expectedForceUntil.Add(time.Minute)) {
			t.Fatalf("expected gemini-3-pro to recover after forced cooldown %v", expectedForceUntil)
		}
		if !IsAuthAvailableForModel(afterRefresh, "claude-sonnet-4-6", before.Add(6*time.Minute)) {
			t.Fatalf("expected unrelated sibling claude-sonnet-4-6 to recover after 5m credential_quota at +6m")
		}
	})

	t.Run("refresh after outer credential_quota expired still preserves active shadow model force cooldown", func(t *testing.T) {
		ctx := context.Background()
		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr.RegisterExecutor(&issue7814RefreshExecutor{
			provider: "antigravity",
			refreshFn: func(_ context.Context, auth *Auth) (*Auth, error) {
				next := auth.Clone()
				if next.Metadata == nil {
					next.Metadata = map[string]any{}
				}
				next.Metadata["access_token"] = "tok-fresh-expired-wrap-force"
				next.Metadata["expired"] = time.Now().Add(2 * time.Hour).Format(time.RFC3339)
				return next, nil
			},
		})
		cred := &Auth{
			ID:       "auth-issue7814-refresh-expired-wrap-force",
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{
				"access_token":  "tok-stale-expired-wrap-force",
				"refresh_token": "ref-expired-wrap-force",
				"expired":       time.Now().Add(2 * time.Hour).Format(time.RFC3339),
			},
		}
		if _, errReg := mgr.Register(WithSkipPersist(ctx), cred); errReg != nil {
			t.Fatalf("Register() error = %v", errReg)
		}

		before := time.Now()
		mgr.MarkResult(ctx, Result{
			AuthID:            cred.ID,
			Provider:          "antigravity",
			Model:             "gemini-3-pro",
			Success:           true,
			CompletedResponse: true,
		})
		shortQuota := 5 * time.Minute
		mgr.MarkResult(ctx, Result{
			AuthID:          cred.ID,
			Provider:        "antigravity",
			Model:           "gemini-3-flash",
			Success:         false,
			CredentialScope: true,
			RetryAfter:      &shortQuota,
			Error:           &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
		})
		forceDur := time.Hour
		mgr.MarkResult(ctx, Result{
			AuthID:          cred.ID,
			Provider:        "antigravity",
			Model:           "gemini-3-pro",
			Success:         false,
			CredentialScope: false,
			RetryAfter:      &forceDur,
			Error:           &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusNotFound, Message: "forced 404"},
		})
		mgr.MarkResult(ctx, Result{
			AuthID:          cred.ID,
			Provider:        "antigravity",
			Model:           "gemini-3-pro",
			Success:         false,
			CredentialScope: false,
			AccessToken:     "tok-stale-expired-wrap-force",
			Error:           &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
		})

		// Advance the outer 5m credential_quota into the past so refresh runs at "+6m"
		// after the outer quota wrap has expired while the 1h forced 404 shadow is still live.
		expiredQuotaAt := before.Add(-time.Minute)
		var expectedForceUntil time.Time
		mgr.mu.Lock()
		live := mgr.auths[cred.ID]
		live.Quota.NextRecoverAt = expiredQuotaAt
		for _, st := range live.ModelStates {
			if st != nil && st.Quota.Exceeded {
				st.Quota.NextRecoverAt = expiredQuotaAt
			}
		}
		if st := live.ModelStates["gemini-3-pro"]; st != nil && st.credentialQuotaShadow != nil {
			expectedForceUntil = st.credentialQuotaShadow.forceCooldownUntil
		}
		mgr.mu.Unlock()
		if expectedForceUntil.IsZero() {
			t.Fatal("expected shadow forceCooldownUntil before refresh")
		}

		if _, errRefresh := mgr.refreshAuthForRequest(ctx, cred.ID, "tok-stale-expired-wrap-force"); errRefresh != nil {
			t.Fatalf("refreshAuthForRequest() error = %v", errRefresh)
		}

		afterRefresh, _ := mgr.GetByID(cred.ID)
		if IsAuthAvailableForModel(afterRefresh, "gemini-3-pro", time.Now()) {
			t.Fatalf("expected gemini-3-pro 1h forced cooldown to survive refresh after outer quota expired; state=%+v", afterRefresh.ModelStates["gemini-3-pro"])
		}
		if IsAuthAvailableForModel(afterRefresh, "gemini-3-pro", expectedForceUntil.Add(-time.Minute)) {
			t.Fatalf("expected gemini-3-pro to remain blocked until %v; state=%+v", expectedForceUntil, afterRefresh.ModelStates["gemini-3-pro"])
		}
		if !IsAuthAvailableForModel(afterRefresh, "gemini-3-pro", expectedForceUntil.Add(time.Minute)) {
			t.Fatalf("expected gemini-3-pro to recover after forced cooldown %v", expectedForceUntil)
		}
		if !IsAuthAvailableForModel(afterRefresh, "claude-sonnet-4-6", time.Now()) {
			t.Fatalf("expected sibling claude-sonnet-4-6 to be available once outer quota expired")
		}
	})

	t.Run("interleaved 403 VALIDATION_REQUIRED then 401 then refresh keeps account blocked for all models", func(t *testing.T) {
		ctx := context.Background()
		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr.RegisterExecutor(&issue7814RefreshExecutor{
			provider: "antigravity",
			refreshFn: func(_ context.Context, auth *Auth) (*Auth, error) {
				next := auth.Clone()
				if next.Metadata == nil {
					next.Metadata = map[string]any{}
				}
				next.Metadata["access_token"] = "tok-fresh-interleaved"
				next.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
				return next, nil
			},
		})
		cred := &Auth{
			ID:       "auth-issue7814-refresh-403-then-401",
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{
				"access_token":  "tok-stale-interleaved",
				"refresh_token": "ref-interleaved",
				"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		}
		if _, errReg := mgr.Register(WithSkipPersist(ctx), cred); errReg != nil {
			t.Fatalf("Register() error = %v", errReg)
		}

		before := time.Now()
		mgr.MarkResult(ctx, Result{
			AuthID:          cred.ID,
			Provider:        "antigravity",
			Model:           "gemini-3-flash",
			Success:         false,
			CredentialScope: true,
			AccessToken:     "tok-stale-interleaved",
			RefreshToken:    "ref-interleaved",
			Error:           &Error{HTTPStatus: http.StatusForbidden, Message: `{"error":{"code":403,"details":[{"reason":"VALIDATION_REQUIRED"}]}}`},
		})
		// Simulate 10m elapsed before the late 401 arrives: 403 deadline is +20m while 401 sets +30m.
		validationDeadline := before.Add(20 * time.Minute)
		mgr.mu.Lock()
		authLive := mgr.auths[cred.ID]
		authLive.credentialRetryAfter = validationDeadline
		authLive.nonAuthCredentialRetryAfter = validationDeadline
		authLive.nonQuotaCooldownUntil = validationDeadline
		authLive.NextRetryAfter = validationDeadline
		if st := authLive.ModelStates["gemini-3-flash"]; st != nil {
			st.NextRetryAfter = validationDeadline
			st.nonQuotaCooldownUntil = validationDeadline
		}
		mgr.mu.Unlock()

		mgr.MarkResult(ctx, Result{
			AuthID:          cred.ID,
			Provider:        "antigravity",
			Model:           "gemini-3-pro",
			Success:         false,
			CredentialScope: true,
			AccessToken:     "tok-stale-interleaved",
			RefreshToken:    "ref-interleaved",
			Error:           &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
		})

		beforeRefresh, _ := mgr.GetByID(cred.ID)
		if !beforeRefresh.credentialRetryAfter.After(validationDeadline.Add(9 * time.Minute)) {
			t.Fatalf("expected 401 to extend credentialRetryAfter (~+30m) beyond 403 (+20m), got credRetry=%v val=%v",
				beforeRefresh.credentialRetryAfter, validationDeadline)
		}

		if _, errRefresh := mgr.refreshAuthForRequest(ctx, cred.ID, "tok-stale-interleaved"); errRefresh != nil {
			t.Fatalf("refreshAuthForRequest() error = %v", errRefresh)
		}

		afterRefresh, _ := mgr.GetByID(cred.ID)
		if !IsAntigravityValidationRequiredError(afterRefresh.LastError) {
			t.Fatalf("expected auth.LastError restored to 403 VALIDATION_REQUIRED after clearing 401, got %+v", afterRefresh.LastError)
		}
		if !afterRefresh.credentialRetryAfter.Equal(validationDeadline) {
			t.Fatalf("expected credentialRetryAfter shrunk to exact 403 deadline %v after refresh, got %v",
				validationDeadline, afterRefresh.credentialRetryAfter)
		}
		for _, model := range []string{"gemini-3-flash", "gemini-3-pro", "claude-sonnet-4-6"} {
			if IsAuthAvailableForModel(afterRefresh, model, before.Add(15*time.Minute)) {
				t.Fatalf("expected %s to remain blocked at +15m after refresh (403 VALIDATION_REQUIRED still active); credRetry=%v nextRetry=%v",
					model, afterRefresh.credentialRetryAfter, afterRefresh.NextRetryAfter)
			}
		}
		// A subsequent routine refresh must also keep the surviving 403 VALIDATION_REQUIRED intact.
		if _, errRefresh2 := mgr.refreshAuthForRequest(ctx, cred.ID, "tok-fresh-interleaved"); errRefresh2 != nil {
			t.Fatalf("second refreshAuthForRequest() error = %v", errRefresh2)
		}
		afterSecondRefresh, _ := mgr.GetByID(cred.ID)
		if !afterSecondRefresh.credentialRetryAfter.Equal(validationDeadline) {
			t.Fatalf("expected credentialRetryAfter to remain %v after second routine refresh, got %v",
				validationDeadline, afterSecondRefresh.credentialRetryAfter)
		}
		for _, model := range []string{"gemini-3-flash", "gemini-3-pro", "claude-sonnet-4-6"} {
			if IsAuthAvailableForModel(afterSecondRefresh, model, before.Add(15*time.Minute)) {
				t.Fatalf("expected %s to remain blocked at +15m after second routine refresh; credRetry=%v", model, afterSecondRefresh.credentialRetryAfter)
			}
			if !IsAuthAvailableForModel(afterSecondRefresh, model, before.Add(21*time.Minute)) {
				t.Fatalf("expected %s to become available at +21m once +20m 403 expired (cleared +30m 401 must not linger); credRetry=%v nextRetry=%v",
					model, afterSecondRefresh.credentialRetryAfter, afterSecondRefresh.NextRetryAfter)
			}
		}
	})

	t.Run("interleaved 401 then 403 VALIDATION_REQUIRED then refresh shrinks deadline to 403", func(t *testing.T) {
		ctx := context.Background()
		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		mgr.RegisterExecutor(&issue7814RefreshExecutor{
			provider: "antigravity",
			refreshFn: func(_ context.Context, auth *Auth) (*Auth, error) {
				next := auth.Clone()
				if next.Metadata == nil {
					next.Metadata = map[string]any{}
				}
				next.Metadata["access_token"] = "tok-fresh-401-then-403"
				next.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
				return next, nil
			},
		})
		cred := &Auth{
			ID:       "auth-issue7814-refresh-401-then-403",
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{
				"access_token":  "tok-stale-401-then-403",
				"refresh_token": "ref-401-then-403",
				"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		}
		if _, errReg := mgr.Register(WithSkipPersist(ctx), cred); errReg != nil {
			t.Fatalf("Register() error = %v", errReg)
		}

		before := time.Now()
		mgr.MarkResult(ctx, Result{
			AuthID:          cred.ID,
			Provider:        "antigravity",
			Model:           "gemini-3-pro",
			Success:         false,
			CredentialScope: true,
			AccessToken:     "tok-stale-401-then-403",
			RefreshToken:    "ref-401-then-403",
			Error:           &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
		})
		// Set a genuinely longer 401 deadline (+40m) before the 30m 403 arrives.
		longer401Deadline := before.Add(40 * time.Minute)
		mgr.mu.Lock()
		authLive := mgr.auths[cred.ID]
		authLive.credentialRetryAfter = longer401Deadline
		authLive.nonQuotaCooldownUntil = longer401Deadline
		authLive.NextRetryAfter = longer401Deadline
		if st := authLive.ModelStates["gemini-3-pro"]; st != nil {
			st.NextRetryAfter = longer401Deadline
			st.nonQuotaCooldownUntil = longer401Deadline
		}
		mgr.mu.Unlock()

		mgr.MarkResult(ctx, Result{
			AuthID:          cred.ID,
			Provider:        "antigravity",
			Model:           "gemini-3-flash",
			Success:         false,
			CredentialScope: true,
			AccessToken:     "tok-stale-401-then-403",
			RefreshToken:    "ref-401-then-403",
			Error:           &Error{HTTPStatus: http.StatusForbidden, Message: `{"error":{"code":403,"details":[{"reason":"VALIDATION_REQUIRED"}]}}`},
		})

		beforeRefresh, _ := mgr.GetByID(cred.ID)
		expected403Deadline := beforeRefresh.nonAuthCredentialRetryAfter
		if !beforeRefresh.credentialRetryAfter.Equal(longer401Deadline) || !expected403Deadline.Before(before.Add(31*time.Minute)) {
			t.Fatalf("expected 401 deadline (+40m) strictly longer than 403 (~+30m) before refresh; credRetry=%v nonAuth=%v",
				beforeRefresh.credentialRetryAfter, expected403Deadline)
		}

		if _, errRefresh := mgr.refreshAuthForRequest(ctx, cred.ID, "tok-stale-401-then-403"); errRefresh != nil {
			t.Fatalf("refreshAuthForRequest() error = %v", errRefresh)
		}

		afterRefresh, _ := mgr.GetByID(cred.ID)
		if !afterRefresh.credentialRetryAfter.Equal(expected403Deadline) {
			t.Fatalf("expected credentialRetryAfter shrunk from +40m to exact 403 deadline %v, got %v",
				expected403Deadline, afterRefresh.credentialRetryAfter)
		}
		for _, model := range []string{"gemini-3-flash", "gemini-3-pro", "claude-sonnet-4-6"} {
			if IsAuthAvailableForModel(afterRefresh, model, before.Add(25*time.Minute)) {
				t.Fatalf("expected %s to remain blocked at +25m by 30m 403", model)
			}
			if !IsAuthAvailableForModel(afterRefresh, model, before.Add(31*time.Minute)) {
				t.Fatalf("expected %s to recover at +31m after cleared +40m 401; credRetry=%v", model, afterRefresh.credentialRetryAfter)
			}
		}
	})

	t.Run("401 without refresh or access token blocks sibling model via credentialRetryAfter", func(t *testing.T) {
		ctx := context.Background()
		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		cred := &Auth{
			ID:       "auth-issue7814-401-notoken",
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{},
		}
		registry.GetGlobalRegistry().RegisterClient(cred.ID, "antigravity", []*registry.ModelInfo{
			{ID: "gemini-3-flash"},
			{ID: "claude-sonnet-4-6"},
		})
		t.Cleanup(func() {
			registry.GetGlobalRegistry().UnregisterClient(cred.ID)
		})
		if _, errReg := mgr.Register(WithSkipPersist(ctx), cred); errReg != nil {
			t.Fatalf("Register() error = %v", errReg)
		}

		mgr.MarkResult(ctx, Result{
			AuthID:          cred.ID,
			Provider:        "antigravity",
			Model:           "gemini-3-flash",
			Success:         false,
			CredentialScope: true,
			Error:           &Error{HTTPStatus: http.StatusUnauthorized, Message: "missing refresh token"},
		})

		snap, _ := mgr.GetByID(cred.ID)
		if IsAuthAvailableForModel(snap, "claude-sonnet-4-6", time.Now()) {
			t.Fatal("expected sibling model blocked after credential-scoped 401 without token")
		}
		if _, errSched := mgr.scheduler.pickSingle(ctx, "antigravity", "claude-sonnet-4-6", cliproxyexecutor.Options{}, nil); errSched == nil {
			t.Fatal("expected scheduler to block sibling model after credential-scoped 401 without refresh token")
		}
	})
}

func TestManager_Issue7814_StaleInFlightSiblingSuccessDoesNotClearCredentialRetryAfter(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	ctx := context.Background()
	mgr := NewManager(nil, nil, nil)
	cred := &Auth{ID: "auth-issue7814-stale-seq", Provider: "antigravity", Status: StatusActive}
	if _, errReg := mgr.Register(WithSkipPersist(ctx), cred); errReg != nil {
		t.Fatalf("Register() error = %v", errReg)
	}

	inFlightSeq := mgr.nextExecutionSeq()
	mgr.MarkResult(ctx, Result{
		AuthID:          cred.ID,
		Provider:        "antigravity",
		Model:           "gemini-3-flash",
		Success:         false,
		CredentialScope: true,
		Error:           &Error{HTTPStatus: http.StatusForbidden, Message: "VALIDATION_REQUIRED"},
	})

	// Stale in-flight sibling success dispatched before the 403 must not clear credentialRetryAfter.
	mgr.MarkResult(ctx, Result{
		AuthID:            cred.ID,
		Provider:          "antigravity",
		Model:             "claude-sonnet-4-6",
		Success:           true,
		CompletedResponse: true,
		ExecutionSeq:      inFlightSeq,
	})

	snap, _ := mgr.GetByID(cred.ID)
	if IsAuthAvailableForModel(snap, "claude-sonnet-4-6", time.Now()) {
		t.Fatalf("stale sibling success cleared credentialRetryAfter: %+v", snap)
	}
}

func TestManager_Issue7814_ForceAndDisableCoolingBehavior(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	ctx := context.Background()

	t.Run("ordinary credential-scoped 403 clears on DisableCooling", func(t *testing.T) {
		mgr := NewManager(nil, nil, nil)
		cred := &Auth{ID: "auth-issue7814-disable-ordinary", Provider: "antigravity", Status: StatusActive}
		if _, errReg := mgr.Register(WithSkipPersist(ctx), cred); errReg != nil {
			t.Fatalf("Register() error = %v", errReg)
		}
		mgr.MarkResult(ctx, Result{
			AuthID:          cred.ID,
			Provider:        "antigravity",
			Model:           "gemini-3-flash",
			Success:         false,
			CredentialScope: true,
			Error:           &Error{HTTPStatus: http.StatusForbidden, Message: "VALIDATION_REQUIRED"},
		})
		mgr.SetConfig(&internalconfig.Config{DisableCooling: true})
		snap, _ := mgr.GetByID(cred.ID)
		if !IsAuthAvailableForModel(snap, "claude-sonnet-4-6", time.Now()) {
			t.Fatalf("expected DisableCooling to clear ordinary credentialRetryAfter: %+v", snap)
		}
	})

	t.Run("forced credential-scoped 403 survives DisableCooling", func(t *testing.T) {
		mgr := NewManager(nil, nil, nil)
		cred := &Auth{ID: "auth-issue7814-disable-force", Provider: "antigravity", Status: StatusActive}
		if _, errReg := mgr.Register(WithSkipPersist(ctx), cred); errReg != nil {
			t.Fatalf("Register() error = %v", errReg)
		}
		mgr.MarkResult(ctx, Result{
			AuthID:          cred.ID,
			Provider:        "antigravity",
			Model:           "gemini-3-flash",
			Success:         false,
			CredentialScope: true,
			Error:           &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusForbidden, Message: "VALIDATION_REQUIRED"},
		})
		mgr.SetConfig(&internalconfig.Config{DisableCooling: true})
		snap, _ := mgr.GetByID(cred.ID)
		if IsAuthAvailableForModel(snap, "claude-sonnet-4-6", time.Now()) {
			t.Fatalf("expected forced credential-scoped 403 to survive DisableCooling for sibling model: %+v", snap)
		}
	})

	t.Run("request-scoped stop skips credential cooldown", func(t *testing.T) {
		mgr := NewManager(nil, nil, nil)
		cred := &Auth{ID: "auth-issue7814-req-scoped-stop", Provider: "antigravity", Status: StatusActive}
		if _, errReg := mgr.Register(WithSkipPersist(ctx), cred); errReg != nil {
			t.Fatalf("Register() error = %v", errReg)
		}
		mgr.MarkResult(ctx, Result{
			AuthID:          cred.ID,
			Provider:        "antigravity",
			Model:           "gemini-3-flash",
			Success:         false,
			CredentialScope: true,
			Error:           &Error{Code: ErrorCodeRequestScoped, HTTPStatus: http.StatusForbidden, Message: "VALIDATION_REQUIRED"},
		})
		snap, _ := mgr.GetByID(cred.ID)
		if !IsAuthAvailableForModel(snap, "claude-sonnet-4-6", time.Now()) || !IsAuthAvailableForModel(snap, "gemini-3-flash", time.Now()) {
			t.Fatalf("expected ErrorCodeRequestScoped to skip cooldown: %+v", snap)
		}
	})
}

func TestManager_Issue7814_CredentialQuotaPlusValidationRequiredSurvivesRestart(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	ctx := context.Background()
	mgr := NewManager(nil, &RoundRobinSelector{}, nil)
	cred := &Auth{ID: "auth-issue7814-quota-plus-403-restore", Provider: "antigravity", Status: StatusActive}
	if _, errReg := mgr.Register(WithSkipPersist(ctx), cred); errReg != nil {
		t.Fatalf("Register() error = %v", errReg)
	}

	before := time.Now()
	shortQuota := 5 * time.Minute
	longValidation := 30 * time.Minute
	mgr.MarkResult(ctx, Result{
		AuthID:          cred.ID,
		Provider:        "antigravity",
		Model:           "gemini-3-flash",
		Success:         false,
		CredentialScope: true,
		RetryAfter:      &shortQuota,
		Error:           &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
	})
	mgr.MarkResult(ctx, Result{
		AuthID:          cred.ID,
		Provider:        "antigravity",
		Model:           "gemini-3-flash",
		Success:         false,
		CredentialScope: true,
		RetryAfter:      &longValidation,
		Error:           &Error{HTTPStatus: http.StatusForbidden, Message: `{"error":{"code":403,"details":[{"reason":"VALIDATION_REQUIRED"}]}}`},
	})

	snap, _ := mgr.GetByID(cred.ID)
	if snap.Quota.NextRecoverAt.After(before.Add(6 * time.Minute)) {
		t.Fatalf("expected Quota.NextRecoverAt to remain independent at ~5m, got %v", snap.Quota.NextRecoverAt)
	}
	if !snap.NextRetryAfter.After(before.Add(29 * time.Minute)) {
		t.Fatalf("expected auth.NextRetryAfter to cover 30m validation cooldown, got %v", snap.NextRetryAfter)
	}

	authRec, ok := authCooldownStateRecord(snap, before)
	if !ok {
		t.Fatal("expected authCooldownStateRecord to emit persisted record")
	}

	// Simulate restart at +6m (after the 5m credential_quota expired, while 30m 403 is still active).
	restoreMgr := NewManager(nil, &RoundRobinSelector{}, nil)
	if _, errReg := restoreMgr.Register(WithSkipPersist(ctx), &Auth{
		ID:       cred.ID,
		Provider: "antigravity",
		Status:   StatusActive,
	}); errReg != nil {
		t.Fatalf("restore Register() error = %v", errReg)
	}
	restoreAt := before.Add(6 * time.Minute)
	restoreMgr.mu.Lock()
	restored := restoreMgr.restoreCooldownRecordLocked(authRec, restoreAt)
	restoreMgr.mu.Unlock()
	if !restored {
		t.Fatalf("expected restoreCooldownRecordLocked to keep 30m validation cooldown at +6m; record=%+v", authRec)
	}

	restoredSnap, _ := restoreMgr.GetByID(cred.ID)
	if IsAuthAvailableForModel(restoredSnap, "claude-sonnet-4-6", restoreAt) {
		t.Fatalf("expected unreported sibling claude-sonnet-4-6 to stay blocked at +6m after restore: %+v", restoredSnap)
	}
	if IsAuthAvailableForModel(restoredSnap, "gemini-3-flash", restoreAt) {
		t.Fatalf("expected gemini-3-flash to stay blocked at +6m after restore: %+v", restoredSnap)
	}
	if !IsAuthAvailableForModel(restoredSnap, "claude-sonnet-4-6", before.Add(31*time.Minute)) {
		t.Fatalf("expected sibling claude-sonnet-4-6 to recover at +31m after restore")
	}
}

func TestManager_Issue7814_ValidationRequiredSurvivesSameAccountTokenRotation(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	ctx := context.Background()
	validationBody := `{"error":{"code":403,"message":"Verify your account","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"VALIDATION_REQUIRED","domain":"cloudaicompanion.googleapis.com"}]}}`

	t.Run("same-account OAuth token rotation does not discard 403 VALIDATION_REQUIRED", func(t *testing.T) {
		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		cred := &Auth{
			ID:       "auth-issue7814-rotate-same",
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{
				"access_token":  "tok-v1",
				"refresh_token": "rt-same",
				"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		}
		registered, errReg := mgr.Register(WithSkipPersist(ctx), cred)
		if errReg != nil {
			t.Fatalf("Register() error = %v", errReg)
		}

		// Simulate OAuth access-token refresh rotating access_token (CredentialVersion 1 -> 2) with same refresh_token.
		rotated := registered.Clone()
		rotated.Metadata["access_token"] = "tok-v2"
		if _, errUpd := mgr.Update(WithSkipPersist(ctx), rotated); errUpd != nil {
			t.Fatalf("Update() error = %v", errUpd)
		}

		// In-flight request started before rotation completes with 403 VALIDATION_REQUIRED.
		mgr.MarkResult(ctx, Result{
			AuthID:            cred.ID,
			Provider:          "antigravity",
			Model:             "gemini-3-flash",
			Success:           false,
			CredentialScope:   true,
			AccessToken:       "tok-v1",
			RefreshToken:      "rt-same",
			CredentialVersion: registered.CredentialVersion,
			RegistrationEpoch: registered.RegistrationEpoch,
			Error:             &Error{HTTPStatus: http.StatusForbidden, Message: validationBody},
		})

		snap, _ := mgr.GetByID(cred.ID)
		if IsAuthAvailableForModel(snap, "gemini-3-flash", time.Now()) {
			t.Fatalf("expected gemini-3-flash blocked after 403 VALIDATION_REQUIRED across same-account token rotation: %+v", snap)
		}
		if IsAuthAvailableForModel(snap, "claude-sonnet-4-6", time.Now()) {
			t.Fatalf("expected sibling claude-sonnet-4-6 blocked after 403 VALIDATION_REQUIRED across same-account token rotation: %+v", snap)
		}
	})

	t.Run("old-token 401 is still discarded after same-account token rotation", func(t *testing.T) {
		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		cred := &Auth{
			ID:       "auth-issue7814-rotate-401-stale",
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{
				"access_token":  "tok-v1",
				"refresh_token": "rt-same",
				"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		}
		registered, errReg := mgr.Register(WithSkipPersist(ctx), cred)
		if errReg != nil {
			t.Fatalf("Register() error = %v", errReg)
		}

		rotated := registered.Clone()
		rotated.Metadata["access_token"] = "tok-v2"
		if _, errUpd := mgr.Update(WithSkipPersist(ctx), rotated); errUpd != nil {
			t.Fatalf("Update() error = %v", errUpd)
		}

		mgr.MarkResult(ctx, Result{
			AuthID:            cred.ID,
			Provider:          "antigravity",
			Model:             "gemini-3-flash",
			Success:           false,
			CredentialScope:   true,
			AccessToken:       "tok-v1",
			RefreshToken:      "rt-same",
			CredentialVersion: registered.CredentialVersion,
			RegistrationEpoch: registered.RegistrationEpoch,
			Error:             &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
		})

		snap, _ := mgr.GetByID(cred.ID)
		if !IsAuthAvailableForModel(snap, "gemini-3-flash", time.Now()) || !IsAuthAvailableForModel(snap, "claude-sonnet-4-6", time.Now()) {
			t.Fatalf("expected stale old-token 401 to be discarded after token rotation: %+v", snap)
		}
	})

	t.Run("credential replacement with different account still discards old 403 VALIDATION_REQUIRED", func(t *testing.T) {
		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		cred := &Auth{
			ID:       "auth-issue7814-rotate-diff-acct",
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{
				"access_token":  "tok-acct1",
				"refresh_token": "rt-acct1",
				"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		}
		registered, errReg := mgr.Register(WithSkipPersist(ctx), cred)
		if errReg != nil {
			t.Fatalf("Register() error = %v", errReg)
		}

		replaced := registered.Clone()
		replaced.Metadata["access_token"] = "tok-acct2"
		replaced.Metadata["refresh_token"] = "rt-acct2"
		if _, errUpd := mgr.Update(WithSkipPersist(ctx), replaced); errUpd != nil {
			t.Fatalf("Update() error = %v", errUpd)
		}

		mgr.MarkResult(ctx, Result{
			AuthID:            cred.ID,
			Provider:          "antigravity",
			Model:             "gemini-3-flash",
			Success:           false,
			CredentialScope:   true,
			AccessToken:       "tok-acct1",
			RefreshToken:      "rt-acct1",
			CredentialVersion: registered.CredentialVersion,
			RegistrationEpoch: registered.RegistrationEpoch,
			Error:             &Error{HTTPStatus: http.StatusForbidden, Message: validationBody},
		})

		snap, _ := mgr.GetByID(cred.ID)
		if !IsAuthAvailableForModel(snap, "gemini-3-flash", time.Now()) || !IsAuthAvailableForModel(snap, "claude-sonnet-4-6", time.Now()) {
			t.Fatalf("expected old-account 403 VALIDATION_REQUIRED to be discarded after credential replacement: %+v", snap)
		}
	})

	t.Run("credential replacement with empty AccessToken and different RefreshToken discards old 403 VALIDATION_REQUIRED", func(t *testing.T) {
		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		cred := &Auth{
			ID:       "auth-issue7814-rotate-empty-access-diff-rt",
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{
				"access_token":  "tok-acct1",
				"refresh_token": "rt-acct1",
				"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		}
		registered, errReg := mgr.Register(WithSkipPersist(ctx), cred)
		if errReg != nil {
			t.Fatalf("Register() error = %v", errReg)
		}

		replaced := registered.Clone()
		replaced.Metadata["access_token"] = "tok-acct2"
		replaced.Metadata["refresh_token"] = "rt-acct2"
		if _, errUpd := mgr.Update(WithSkipPersist(ctx), replaced); errUpd != nil {
			t.Fatalf("Update() error = %v", errUpd)
		}

		mgr.MarkResult(ctx, Result{
			AuthID:            cred.ID,
			Provider:          "antigravity",
			Model:             "gemini-3-flash",
			Success:           false,
			CredentialScope:   true,
			AccessToken:       "",
			RefreshToken:      "rt-acct1",
			CredentialVersion: registered.CredentialVersion,
			RegistrationEpoch: registered.RegistrationEpoch,
			Error:             &Error{HTTPStatus: http.StatusForbidden, Message: validationBody},
		})

		snap, _ := mgr.GetByID(cred.ID)
		if !IsAuthAvailableForModel(snap, "gemini-3-flash", time.Now()) || !IsAuthAvailableForModel(snap, "claude-sonnet-4-6", time.Now()) {
			t.Fatalf("expected old-account 403 VALIDATION_REQUIRED with empty AccessToken and mismatched RefreshToken to be discarded: %+v", snap)
		}
	})

	t.Run("credential replacement with same AccessToken and different RefreshToken discards old 403 VALIDATION_REQUIRED", func(t *testing.T) {
		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		cred := &Auth{
			ID:       "auth-issue7814-rotate-same-access-diff-rt",
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{
				"access_token":  "tok-shared",
				"refresh_token": "rt-acct1",
				"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		}
		registered, errReg := mgr.Register(WithSkipPersist(ctx), cred)
		if errReg != nil {
			t.Fatalf("Register() error = %v", errReg)
		}

		// Replace refresh_token while temporarily keeping the same access_token (CredentialVersion 1 -> 2).
		replaced := registered.Clone()
		replaced.Metadata["refresh_token"] = "rt-acct2"
		if _, errUpd := mgr.Update(WithSkipPersist(ctx), replaced); errUpd != nil {
			t.Fatalf("Update() error = %v", errUpd)
		}

		mgr.MarkResult(ctx, Result{
			AuthID:            cred.ID,
			Provider:          "antigravity",
			Model:             "gemini-3-flash",
			Success:           false,
			CredentialScope:   true,
			AccessToken:       "tok-shared",
			RefreshToken:      "rt-acct1",
			CredentialVersion: registered.CredentialVersion,
			RegistrationEpoch: registered.RegistrationEpoch,
			Error:             &Error{HTTPStatus: http.StatusForbidden, Message: validationBody},
		})

		snap, _ := mgr.GetByID(cred.ID)
		if !IsAuthAvailableForModel(snap, "gemini-3-flash", time.Now()) || !IsAuthAvailableForModel(snap, "claude-sonnet-4-6", time.Now()) {
			t.Fatalf("expected old-version 403 VALIDATION_REQUIRED with matching AccessToken but mismatched RefreshToken to be discarded: %+v", snap)
		}
	})
}

func TestManager_Issue7814_RestartClassificationAndErrorPreservation(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	ctx := context.Background()

	t.Run("401 then 5m credential_quota then restart then refresh clears 401 layer", func(t *testing.T) {
		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		cred := &Auth{
			ID:       "auth-issue7814-401-429-restart-refresh",
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{
				"access_token":  "tok-old-401-429",
				"refresh_token": "rt-401-429",
				"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		}
		if _, errReg := mgr.Register(WithSkipPersist(ctx), cred); errReg != nil {
			t.Fatalf("Register() error = %v", errReg)
		}

		before := time.Now()
		mgr.MarkResult(ctx, Result{
			AuthID:          cred.ID,
			Provider:        "antigravity",
			Model:           "gemini-3-flash",
			Success:         false,
			CredentialScope: true,
			AccessToken:     "tok-old-401-429",
			Error:           &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
		})
		shortQuota := 5 * time.Minute
		mgr.MarkResult(ctx, Result{
			AuthID:          cred.ID,
			Provider:        "antigravity",
			Model:           "gemini-3-pro",
			Success:         false,
			CredentialScope: true,
			RetryAfter:      &shortQuota,
			Error:           &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
		})

		snap, _ := mgr.GetByID(cred.ID)
		authRec, ok := authCooldownStateRecord(snap, before)
		if !ok {
			t.Fatal("expected authCooldownStateRecord to emit record")
		}
		flashRec, okFlash := modelCooldownStateRecord(snap, "gemini-3-flash", snap.ModelStates["gemini-3-flash"], before)
		if !okFlash {
			t.Fatal("expected modelCooldownStateRecord for gemini-3-flash")
		}

		// Simulate restart at +1m, then successful OAuth refresh.
		restoreMgr := NewManager(nil, &RoundRobinSelector{}, nil)
		restoreMgr.RegisterExecutor(&issue7814RefreshExecutor{
			provider: "antigravity",
			refreshFn: func(_ context.Context, auth *Auth) (*Auth, error) {
				next := auth.Clone()
				next.Metadata["access_token"] = "tok-fresh-401-429"
				next.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
				return next, nil
			},
		})
		if _, errReg := restoreMgr.Register(WithSkipPersist(ctx), cred.Clone()); errReg != nil {
			t.Fatalf("restore Register() error = %v", errReg)
		}
		restoreAt := before.Add(time.Minute)
		restoreMgr.mu.Lock()
		restoreMgr.restoreCooldownRecordLocked(flashRec, restoreAt)
		restoreMgr.restoreCooldownRecordLocked(authRec, restoreAt)
		restoreMgr.mu.Unlock()

		if _, errRefresh := restoreMgr.refreshAuthForRequest(ctx, cred.ID, "tok-old-401-429"); errRefresh != nil {
			t.Fatalf("refreshAuthForRequest() error = %v", errRefresh)
		}

		afterRefresh, _ := restoreMgr.GetByID(cred.ID)
		for _, model := range []string{"gemini-3-flash", "gemini-3-pro", "claude-sonnet-4-6"} {
			if IsAuthAvailableForModel(afterRefresh, model, before.Add(2*time.Minute)) {
				t.Fatalf("expected %s to stay blocked by 5m credential_quota at +2m", model)
			}
			if !IsAuthAvailableForModel(afterRefresh, model, before.Add(6*time.Minute)) {
				t.Fatalf("expected %s to recover at +6m after restart+refresh cleared 401; credRetry=%v nonAuth=%v nextRetry=%v",
					model, afterRefresh.credentialRetryAfter, afterRefresh.nonAuthCredentialRetryAfter, afterRefresh.NextRetryAfter)
			}
		}
	})

	t.Run("403 VALIDATION_REQUIRED then 401 then restart then refresh preserves validation error and cooldown", func(t *testing.T) {
		mgr := NewManager(nil, &RoundRobinSelector{}, nil)
		cred := &Auth{
			ID:       "auth-issue7814-403-401-restart-refresh",
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{
				"access_token":  "tok-old-403-401",
				"refresh_token": "rt-403-401",
				"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
			},
		}
		if _, errReg := mgr.Register(WithSkipPersist(ctx), cred); errReg != nil {
			t.Fatalf("Register() error = %v", errReg)
		}

		before := time.Now()
		mgr.MarkResult(ctx, Result{
			AuthID:          cred.ID,
			Provider:        "antigravity",
			Model:           "gemini-3-flash",
			Success:         false,
			CredentialScope: true,
			AccessToken:     "tok-old-403-401",
			Error:           &Error{HTTPStatus: http.StatusForbidden, Message: `{"error":{"code":403,"details":[{"reason":"VALIDATION_REQUIRED"}]}}`},
		})
		mgr.MarkResult(ctx, Result{
			AuthID:          cred.ID,
			Provider:        "antigravity",
			Model:           "gemini-3-pro",
			Success:         false,
			CredentialScope: true,
			AccessToken:     "tok-old-403-401",
			Error:           &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
		})

		snap, _ := mgr.GetByID(cred.ID)
		authRec, ok := authCooldownStateRecord(snap, before)
		if !ok {
			t.Fatal("expected authCooldownStateRecord to emit record")
		}

		restoreMgr := NewManager(nil, &RoundRobinSelector{}, nil)
		restoreMgr.RegisterExecutor(&issue7814RefreshExecutor{
			provider: "antigravity",
			refreshFn: func(_ context.Context, auth *Auth) (*Auth, error) {
				next := auth.Clone()
				next.Metadata["access_token"] = "tok-fresh-403-401"
				next.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
				return next, nil
			},
		})
		if _, errReg := restoreMgr.Register(WithSkipPersist(ctx), cred.Clone()); errReg != nil {
			t.Fatalf("restore Register() error = %v", errReg)
		}
		restoreAt := before.Add(time.Minute)
		restoreMgr.mu.Lock()
		restoreMgr.restoreCooldownRecordLocked(authRec, restoreAt)
		restoreMgr.mu.Unlock()

		if _, errRefresh := restoreMgr.refreshAuthForRequest(ctx, cred.ID, "tok-old-403-401"); errRefresh != nil {
			t.Fatalf("refreshAuthForRequest() error = %v", errRefresh)
		}

		afterRefresh, _ := restoreMgr.GetByID(cred.ID)
		if !IsAntigravityValidationRequiredError(afterRefresh.LastError) {
			t.Fatalf("expected 403 VALIDATION_REQUIRED LastError preserved across restart+refresh, got %+v", afterRefresh.LastError)
		}
		if IsAuthAvailableForModel(afterRefresh, "claude-sonnet-4-6", before.Add(2*time.Minute)) {
			t.Fatalf("expected sibling claude-sonnet-4-6 blocked after restart+refresh")
		}
	})
}
