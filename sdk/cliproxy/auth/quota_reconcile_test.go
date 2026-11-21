package auth

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func reconcileFixture() *Auth {
	until := time.Now().Add(time.Hour)
	return &Auth{
		ID: "quota-reconcile-test", Provider: "claude", Status: StatusError,
		Metadata:    map[string]any{"access_token": "synthetic", "expired": until.Format(time.RFC3339)},
		Unavailable: true, NextRetryAfter: until,
		Quota:     QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: until},
		LastError: &Error{Code: ErrorCodeCredentialQuota, HTTPStatus: 429},
		ModelStates: map[string]*ModelState{
			"claude-test": {
				Status: StatusError, Unavailable: true, NextRetryAfter: until,
				Quota:     QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: until},
				LastError: &Error{Code: ErrorCodeCredentialQuota, HTTPStatus: 429},
			},
		},
	}
}

func TestReconcileQuotaSuccessAndNoInference(t *testing.T) {
	m := NewManager(nil, nil, nil)
	a := reconcileFixture()
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(a.ID, "claude", []*registry.ModelInfo{{ID: "claude-test"}})
	t.Cleanup(func() { reg.UnregisterClient(a.ID) })
	if _, err := m.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	reg.SetModelQuotaExceeded(a.ID, "claude-test")
	reg.SuspendClientModel(a.ID, "claude-test", "quota")
	calls := 0
	updated, _, err := m.ReconcileQuota(context.Background(), a.ID, func(_ context.Context, snapshot *Auth) (bool, error) {
		calls++
		if snapshot.Metadata["access_token"] != "synthetic" {
			t.Fatal("wrong credential snapshot")
		}
		return true, nil
	})
	if err != nil || updated == nil || updated.Quota.Exceeded || updated.Unavailable ||
		calls != 1 || !reflect.DeepEqual(a.Metadata, updated.Metadata) {
		t.Fatalf("reconciliation failed: updated=%v err=%v calls=%d", updated != nil, err, calls)
	}
	if reg.GetModelCount("claude-test") != 1 {
		t.Fatal("registry did not resume model")
	}
	if len(m.executors) != 0 {
		t.Fatal("test unexpectedly has inference executors")
	}
}

func TestReconcileQuotaFailClosed(t *testing.T) {
	for name, mutate := range map[string]func(*Auth){
		"disabled":          func(a *Auth) { a.Disabled = true },
		"auth":              func(a *Auth) { a.RequiresSignIn = true },
		"expired":           func(a *Auth) { a.Metadata["expired"] = time.Now().Add(-time.Hour).Format(time.RFC3339) },
		"unknown-expiry":    func(a *Auth) { delete(a.Metadata, "expired") },
		"api-key":           func(a *Auth) { delete(a.Metadata, "access_token") },
		"wrong-provider":    func(a *Auth) { a.Provider = "" },
		"overload":          func(a *Auth) { a.LastError = &Error{HTTPStatus: 503} },
		"request-scoped":    func(a *Auth) { a.LastError.Code = ErrorCodeRequestScoped },
		"paid-credit":       func(a *Auth) { a.LastError.Message = "credit balance is too low" },
		"transient-429":     func(a *Auth) { a.LastError.Code = ErrorCodeTransientRateLimit },
		"independent-retry": func(a *Auth) { a.credentialRetryAfter = time.Now().Add(time.Hour) },
		"model-404":         func(a *Auth) { a.ModelStates["claude-test"].LastError = &Error{HTTPStatus: 404} },
		"hidden-403": func(a *Auth) {
			a.ModelStates["claude-test"].credentialQuotaShadow = &ModelState{LastError: &Error{HTTPStatus: 403}}
		},
		"model-denied": func(a *Auth) { a.ModelStates["claude-test"].Status = StatusDisabled },
	} {
		t.Run(name, func(t *testing.T) {
			a := reconcileFixture()
			mutate(a)
			if CanReconcileQuota(a, time.Now()) {
				t.Fatal("unsafe candidate accepted")
			}
		})
	}
}

func TestReconcileQuotaRejectsConcurrentChange(t *testing.T) {
	for name, mutate := range map[string]func(*Auth){
		"new-same-deadline-error": func(a *Auth) { a.Generation++ },
		"replacement":             func(a *Auth) { a.RegistrationEpoch++ },
		"refresh":                 func(a *Auth) { a.Metadata["access_token"] = "new-synthetic-token" },
		"disable":                 func(a *Auth) { a.Disabled = true },
		"new-model-error":         func(a *Auth) { a.ModelStates["claude-test"].LastError.HTTPStatus = 403 },
	} {
		t.Run(name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			a := reconcileFixture()
			if _, err := m.Register(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			entered, resume := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			go func() {
				_, _, err := m.ReconcileQuota(context.Background(), a.ID, func(context.Context, *Auth) (bool, error) {
					close(entered)
					<-resume
					return true, nil
				})
				done <- err
			}()
			<-entered
			m.mu.Lock()
			mutate(m.auths[a.ID])
			m.mu.Unlock()
			expected, _ := m.GetByID(a.ID)
			close(resume)
			if err := <-done; !errors.Is(err, ErrQuotaReconcileChanged) {
				t.Fatalf("expected conflict, got %v", err)
			}
			after, _ := m.GetByID(a.ID)
			if !reflect.DeepEqual(after, expected) {
				t.Fatal("concurrent state overwritten")
			}
		})
	}
}

func TestQuotaReconcileEvidenceFreshness(t *testing.T) {
	now := time.Now()
	a := reconcileFixture()
	for _, started := range []time.Time{time.Time{}, now.Add(-2 * time.Minute), now.Add(time.Minute)} {
		if quotaReconcileMatches(a, a.Clone(), started, now) {
			t.Fatal("stale or future evidence accepted")
		}
	}
}

func TestReconcileQuotaProviderNeutralModelOnly(t *testing.T) {
	for _, provider := range []string{"claude", "codex", "antigravity", "kimi", "xai", "custom"} {
		t.Run(provider, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			a := reconcileFixture()
			a.Provider = provider
			a.Quota = QuotaState{}
			a.LastError = nil
			a.ModelStates["claude-test"].LastError.Code = ""
			if _, errRegister := manager.Register(context.Background(), a); errRegister != nil {
				t.Fatal(errRegister)
			}
			// Core is provider-neutral. Only the management adapter decides
			// whether authoritative non-spending evidence exists.
			got, _, err := manager.ReconcileQuota(context.Background(), a.ID, func(_ context.Context, snapshot *Auth) (bool, error) {
				return snapshot.Provider == provider, nil
			})
			if err != nil || got == nil || got.ModelStates["claude-test"].Quota.Exceeded {
				t.Fatalf("model-only recovery failed for %s: %v", provider, err)
			}
		})
	}
}

func TestReconcileQuotaRejectsHomeSwitchDuringProbe(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	a := reconcileFixture()
	_, _ = manager.Register(context.Background(), a)
	var afterModeSwitch *Auth
	got, _, err := manager.ReconcileQuota(context.Background(), a.ID, func(context.Context, *Auth) (bool, error) {
		cfg := &config.Config{}
		cfg.Home.Enabled = true
		manager.SetConfig(cfg)
		afterModeSwitch, _ = manager.GetByID(a.ID)
		return true, nil
	})
	if got != nil || !errors.Is(err, ErrQuotaReconcileChanged) {
		t.Fatal("Home change did not abort local reset")
	}
	after, _ := manager.GetByID(a.ID)
	// SetConfig itself discards local cooldowns when handing authority to Home.
	// The reconciler must make no additional changes after that transition.
	if !reflect.DeepEqual(after, afterModeSwitch) {
		t.Fatal("reconciler changed Home-owned state")
	}
}

func TestReconcileQuotaStockMarkResult(t *testing.T) {
	m := NewManager(nil, nil, nil)
	a := reconcileFixture()
	a.Status, a.Unavailable, a.NextRetryAfter = StatusActive, false, time.Time{}
	a.Quota, a.LastError, a.ModelStates = QuotaState{}, nil, nil
	if _, err := m.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	retry := time.Hour
	m.MarkResult(context.Background(), Result{
		AuthID: a.ID, Provider: "claude", Model: "claude-test",
		Success: false, CredentialScope: true, RetryAfter: &retry,
		Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"rate_limit_error","message":"Weekly rate limit exceeded"}}`},
	})
	before, _ := m.GetByID(a.ID)
	if !CanReconcileQuota(before, time.Now()) {
		t.Fatal("stock credential-scoped 429 was not a reconciliation candidate")
	}
	got, _, err := m.ReconcileQuota(context.Background(), a.ID, func(context.Context, *Auth) (bool, error) {
		return true, nil
	})
	if err != nil || got == nil || got.Quota.Exceeded {
		t.Fatalf("stock cooldown was not reconciled: updated=%v err=%v", got != nil, err)
	}
}

func TestReconcileQuotaNoRecoveryOrCancellation(t *testing.T) {
	for _, mode := range []string{"limited", "error", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			a := reconcileFixture()
			_, _ = m.Register(context.Background(), a)
			before, _ := m.GetByID(a.ID)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			got, _, _ := m.ReconcileQuota(ctx, a.ID, func(context.Context, *Auth) (bool, error) {
				switch mode {
				case "limited":
					return false, nil
				case "error":
					return false, errors.New("usage unavailable")
				default:
					cancel()
					return true, nil
				}
			})
			after, _ := m.GetByID(a.ID)
			if got != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("unverified state changed")
			}
		})
	}
}
