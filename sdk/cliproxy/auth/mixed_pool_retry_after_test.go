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

// mixedPoolAuths returns one credential in a multi-day quota cooldown and one
// blocked by a terminal 401, so no candidate is selectable.
func mixedPoolAuths(model string, now time.Time) []*Auth {
	reset := now.Add(96 * time.Hour)
	return []*Auth{
		{
			ID:       "mixed-quota",
			Provider: "codex",
			ModelStates: map[string]*ModelState{model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: reset,
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: reset},
			}},
		},
		{
			ID:          "mixed-unauthorized",
			Provider:    "codex",
			Status:      StatusError,
			Unavailable: true,
			LastError:   &Error{Code: "unauthorized", Message: "token revoked", HTTPStatus: http.StatusUnauthorized},
		},
	}
}

func assertMixedPoolUnavailable(t *testing.T, err error) {
	t.Helper()
	var cooldown *modelCooldownError
	if errors.As(err, &cooldown) {
		t.Fatalf("mixed pool must not report a model cooldown: %v", err)
	}
	var authErr *Error
	if !errors.As(err, &authErr) || authErr.Code != "auth_unavailable" || !authErr.Retryable {
		t.Fatalf("expected retryable auth_unavailable, got %T %v", err, err)
	}
	if got := statusCodeFromError(err); got != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", got)
	}
	if got := SafeResponseHeaders(err).Get("Retry-After"); got != "" {
		t.Fatalf("mixed pool must not advertise the quota reset, got Retry-After %q", got)
	}
}

func TestMixedPoolUnavailableOmitsQuotaRetryAfter(t *testing.T) {
	model := "gpt-mixed-pool"
	now := time.Now()
	auths := mixedPoolAuths(model, now)

	_, err := getAvailableAuths(auths, "codex", model, now)
	assertMixedPoolUnavailable(t, err)

	_, err = (&Manager{}).availableAuthsForRouteModelWithPriorityMode(auths, "codex", model, now, false)
	assertMixedPoolUnavailable(t, err)

	shard := &modelScheduler{entries: map[string]*scheduledAuth{
		"quota":        {auth: auths[0], state: scheduledStateCooldown, nextRetryAt: now.Add(96 * time.Hour)},
		"unauthorized": {auth: auths[1], state: scheduledStateBlocked},
	}}
	assertMixedPoolUnavailable(t, shard.unavailableErrorLocked("codex", model, nil))
}

func TestMixedPoolUnavailableUsesEarliestNonQuotaRecovery(t *testing.T) {
	model := "gpt-mixed-pool-transient"
	now := time.Now()
	auths := mixedPoolAuths(model, now)
	auths[1] = &Auth{ID: "mixed-transient", Provider: "codex", Unavailable: true, NextRetryAfter: now.Add(time.Minute)}

	for _, selectAuths := range []func() ([]*Auth, error){
		func() ([]*Auth, error) { return getAvailableAuths(auths, "codex", model, now) },
		func() ([]*Auth, error) {
			return (&Manager{}).availableAuthsForRouteModelWithPriorityMode(auths, "codex", model, now, false)
		},
	} {
		_, err := selectAuths()
		if got := SafeResponseHeaders(err).Get("Retry-After"); got != "60" {
			t.Fatalf("Retry-After = %q, want 60 (%v)", got, err)
		}
	}

	shard := &modelScheduler{entries: map[string]*scheduledAuth{
		"quota":     {auth: auths[0], state: scheduledStateCooldown, nextRetryAt: now.Add(96 * time.Hour)},
		"transient": {auth: auths[1], state: scheduledStateBlocked, nextRetryAt: now.Add(time.Minute)},
	}}
	if got := SafeResponseHeaders(shard.unavailableErrorLocked("codex", model, nil)).Get("Retry-After"); got != "60" {
		t.Fatalf("scheduler Retry-After = %q, want 60", got)
	}
}

func TestManagerExecuteMixedPoolReturns503WithoutQuotaRetryAfter(t *testing.T) {
	model := "gpt-mixed-pool-execute"
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		RebindOn: SessionAffinityRebindOnQuota,
	})
	m := NewManager(nil, selector, nil)
	m.SetRetryConfig(3, 30*time.Second, 0)
	m.RegisterExecutor(&authFallbackExecutor{id: "codex"})

	reg := registry.GetGlobalRegistry()
	for _, auth := range mixedPoolAuths(model, time.Now()) {
		if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
		reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
		id := auth.ID
		t.Cleanup(func() { reg.UnregisterClient(id) })
	}

	_, err := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	assertMixedPoolUnavailable(t, err)
}
