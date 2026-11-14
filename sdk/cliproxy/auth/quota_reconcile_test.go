package auth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type usageProbeExecutor struct {
	body   string
	status int
	calls  int
}

func (e *usageProbeExecutor) Identifier() string { return "codex" }
func (e *usageProbeExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *usageProbeExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (e *usageProbeExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) { return auth, nil }
func (e *usageProbeExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *usageProbeExecutor) HttpRequest(_ context.Context, _ *Auth, _ *http.Request) (*http.Response, error) {
	e.calls++
	return &http.Response{StatusCode: e.status, Body: io.NopCloser(strings.NewReader(e.body))}, nil
}

func newQuotaCooldownManager(t *testing.T, exec *usageProbeExecutor) (*Manager, string) {
	t.Helper()
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(exec)
	manager.SetConfig(&internalconfig.Config{QuotaReconcile: true})
	next := time.Now().Add(100 * time.Hour)
	authID := "quota-reconcile-codex"
	if _, err := manager.Register(context.Background(), &Auth{
		ID:             authID,
		Provider:       "codex",
		Status:         StatusError,
		Unavailable:    true,
		NextRetryAfter: next,
		Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	return manager, authID
}

func TestReconcileQuotaCooldownsClearsWhenProviderAllowsUsage(t *testing.T) {
	exec := &usageProbeExecutor{status: http.StatusOK, body: `{"rate_limit":{"allowed":true,"limit_reached":false}}`}
	manager, authID := newQuotaCooldownManager(t, exec)

	manager.reconcileQuotaCooldowns(context.Background())

	got, _ := manager.GetByID(authID)
	if got.Unavailable || got.Quota.Exceeded || !got.NextRetryAfter.IsZero() {
		t.Fatalf("cooldown not cleared: unavailable=%v exceeded=%v next=%v", got.Unavailable, got.Quota.Exceeded, got.NextRetryAfter)
	}
}

func TestReconcileQuotaCooldownsKeepsCooldownWhenLimitStillReached(t *testing.T) {
	exec := &usageProbeExecutor{status: http.StatusOK, body: `{"rate_limit":{"allowed":false,"limit_reached":true}}`}
	manager, authID := newQuotaCooldownManager(t, exec)

	manager.reconcileQuotaCooldowns(context.Background())

	got, _ := manager.GetByID(authID)
	if !got.Quota.Exceeded {
		t.Fatalf("cooldown cleared although provider reports limit reached")
	}
}

func TestReconcileQuotaCooldownsKeepsCooldownOnProbeFailure(t *testing.T) {
	for name, exec := range map[string]*usageProbeExecutor{
		"http error":   {status: http.StatusUnauthorized, body: `{}`},
		"missing data": {status: http.StatusOK, body: `{"plan_type":"pro"}`},
	} {
		t.Run(name, func(t *testing.T) {
			manager, authID := newQuotaCooldownManager(t, exec)
			manager.reconcileQuotaCooldowns(context.Background())
			got, _ := manager.GetByID(authID)
			if !got.Quota.Exceeded {
				t.Fatalf("cooldown cleared on unusable probe result")
			}
		})
	}
}

func TestReconcileQuotaCooldownsIgnoresHealthyAndDisabledAuths(t *testing.T) {
	exec := &usageProbeExecutor{status: http.StatusOK, body: `{"rate_limit":{"allowed":true,"limit_reached":false}}`}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(exec)
	manager.SetConfig(&internalconfig.Config{QuotaReconcile: true})
	next := time.Now().Add(time.Hour)
	if _, err := manager.Register(context.Background(), &Auth{ID: "healthy", Provider: "codex", Status: StatusActive}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Register(context.Background(), &Auth{ID: "disabled", Provider: "codex", Disabled: true, Status: StatusDisabled,
		Quota: QuotaState{Exceeded: true, NextRecoverAt: next}}); err != nil {
		t.Fatal(err)
	}

	manager.reconcileQuotaCooldowns(context.Background())

	if exec.calls != 0 {
		t.Fatalf("probed %d times, want 0", exec.calls)
	}
}

func TestReconcileQuotaCooldownsDisabledByDefault(t *testing.T) {
	exec := &usageProbeExecutor{status: http.StatusOK, body: `{"rate_limit":{"allowed":true,"limit_reached":false}}`}
	manager, authID := newQuotaCooldownManager(t, exec)
	manager.SetConfig(&internalconfig.Config{})

	manager.reconcileQuotaCooldowns(context.Background())

	got, _ := manager.GetByID(authID)
	if !got.Quota.Exceeded || exec.calls != 0 {
		t.Fatalf("reconciler ran while quota-reconcile is off: exceeded=%v calls=%d", got.Quota.Exceeded, exec.calls)
	}
}
