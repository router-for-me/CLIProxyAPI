package auth

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// overageMarkedTestError is a minimal executor-shaped rejection that carries
// both a status code and the conductor-facing overage marker.
type overageMarkedTestError struct {
	status int
}

func (e overageMarkedTestError) Error() string       { return "spend cap reached" }
func (e overageMarkedTestError) StatusCode() int     { return e.status }
func (overageMarkedTestError) OverageRejected() bool { return true }

// plainThrottleTestError is a 429 without the overage marker.
type plainThrottleTestError struct{}

func (plainThrottleTestError) Error() string   { return "rate limited" }
func (plainThrottleTestError) StatusCode() int { return http.StatusTooManyRequests }

// registrySuspendReasonForTest reads the suspension reason an auth holds for
// model from the global registry. The registry exposes no exported getter for
// per-client suspension reasons, so tests read it reflectively (read-only).
func registrySuspendReasonForTest(t *testing.T, authID, model string) string {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	models := reflect.ValueOf(reg).Elem().FieldByName("models")
	if !models.IsValid() {
		t.Fatal("registry has no models field")
	}
	registration := models.MapIndex(reflect.ValueOf(model))
	if !registration.IsValid() || registration.IsNil() {
		t.Fatalf("model %q not registered", model)
	}
	suspended := registration.Elem().FieldByName("SuspendedClients")
	if !suspended.IsValid() || suspended.IsNil() {
		return ""
	}
	entry := suspended.MapIndex(reflect.ValueOf(authID))
	if !entry.IsValid() {
		return ""
	}
	return entry.String()
}

// TestResultErrorFromErrorOverageMark verifies that an executor error marked
// OverageRejected pins the stable "overage" code on the result error while the
// 429 status still flows through for classification.
func TestResultErrorFromErrorOverageMark(t *testing.T) {
	resultErr := resultErrorFromError(overageMarkedTestError{status: http.StatusTooManyRequests})
	if resultErr == nil {
		t.Fatal("resultErrorFromError() = nil, want *Error")
	}
	if resultErr.Code != "overage" {
		t.Fatalf("Code = %q, want %q", resultErr.Code, "overage")
	}
	if resultErr.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("HTTPStatus = %d, want %d", resultErr.HTTPStatus, http.StatusTooManyRequests)
	}
}

// TestResultErrorFromErrorPlainThrottleNotOverage verifies that an ordinary
// throttling error is never stamped with the overage code.
func TestResultErrorFromErrorPlainThrottleNotOverage(t *testing.T) {
	resultErr := resultErrorFromError(plainThrottleTestError{})
	if resultErr == nil {
		t.Fatal("resultErrorFromError() = nil, want *Error")
	}
	if resultErr.Code == "overage" {
		t.Fatalf("Code = %q, want anything but %q for a plain throttle", resultErr.Code, "overage")
	}
	if resultErr.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("HTTPStatus = %d, want %d", resultErr.HTTPStatus, http.StatusTooManyRequests)
	}
}

// TestMarkResultOverageScopedQuota verifies that a model-level 429 carrying
// the overage code enters the long fixed overage horizon (7 days) with
// Reason "overage" instead of the exponential throttle ladder, and suspends
// the registry model state with the parallel "overage" reason.
func TestMarkResultOverageScopedQuota(t *testing.T) {
	const (
		provider = "overage-scope-test"
		model    = "overage-scope-test-model"
		authID   = "overage-scope-test-auth"
	)

	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

	manager := NewManager(nil, &RoundRobinSelector{}, NoopHook{})
	manager.SetRetryConfig(0, 0, 0)
	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register(): %v", errRegister)
	}

	before := time.Now()
	manager.MarkResult(context.Background(), Result{
		AuthID:   authID,
		Provider: provider,
		Model:    model,
		Success:  false,
		Error:    &Error{Code: "overage", Message: "spend cap reached", HTTPStatus: http.StatusTooManyRequests},
	})

	updated := manager.auths[authID]
	if updated == nil {
		t.Fatal("auth missing after MarkResult")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("model state for %q missing after MarkResult", model)
	}
	if !state.Quota.Exceeded {
		t.Fatal("Quota.Exceeded = false, want true")
	}
	if state.Quota.Reason != "overage" {
		t.Fatalf("Quota.Reason = %q, want %q", state.Quota.Reason, "overage")
	}
	horizonStart := before.Add(7 * 24 * time.Hour).Add(-time.Minute)
	horizonEnd := before.Add(7 * 24 * time.Hour).Add(time.Minute)
	if state.Quota.NextRecoverAt.Before(horizonStart) || state.Quota.NextRecoverAt.After(horizonEnd) {
		t.Fatalf("Quota.NextRecoverAt = %v, want within a minute of %v (7-day overage horizon)", state.Quota.NextRecoverAt, before.Add(7*24*time.Hour))
	}
	if !state.NextRetryAfter.Equal(state.Quota.NextRecoverAt) {
		t.Fatalf("NextRetryAfter = %v, want = Quota.NextRecoverAt %v", state.NextRetryAfter, state.Quota.NextRecoverAt)
	}
	if state.Quota.BackoffLevel != 0 {
		t.Fatalf("Quota.BackoffLevel = %d, want 0 (overage must not escalate the throttle ladder)", state.Quota.BackoffLevel)
	}
	if reason := registrySuspendReasonForTest(t, authID, model); reason != "overage" {
		t.Fatalf("registry suspension reason = %q, want %q", reason, "overage")
	}
}

// TestMarkResultThrottleStillQuotaReason pins the unchanged ordinary-throttle
// path: a 429 without the overage code keeps the quota ladder (Reason "quota",
// short backoff, registry suspension reason "quota").
func TestMarkResultThrottleStillQuotaReason(t *testing.T) {
	const (
		provider = "overage-throttle-test"
		model    = "overage-throttle-test-model"
		authID   = "overage-throttle-test-auth"
	)

	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

	manager := NewManager(nil, &RoundRobinSelector{}, NoopHook{})
	manager.SetRetryConfig(0, 0, 0)
	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register(): %v", errRegister)
	}

	before := time.Now()
	manager.MarkResult(context.Background(), Result{
		AuthID:   authID,
		Provider: provider,
		Model:    model,
		Success:  false,
		Error:    &Error{Code: "rate_limit_error", Message: "rate limited", HTTPStatus: http.StatusTooManyRequests},
	})

	updated := manager.auths[authID]
	if updated == nil {
		t.Fatal("auth missing after MarkResult")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("model state for %q missing after MarkResult", model)
	}
	if !state.Quota.Exceeded {
		t.Fatal("Quota.Exceeded = false, want true")
	}
	if state.Quota.Reason != "quota" {
		t.Fatalf("Quota.Reason = %q, want %q (ordinary throttle unchanged)", state.Quota.Reason, "quota")
	}
	// Ladder base is 1s; anything near the 7-day horizon means the throttle
	// path was contaminated by the overage horizon.
	if state.Quota.NextRecoverAt.IsZero() || state.Quota.NextRecoverAt.After(before.Add(5*time.Second)) {
		t.Fatalf("Quota.NextRecoverAt = %v, want within ~1s of %v (quota ladder)", state.Quota.NextRecoverAt, before)
	}
	if !state.NextRetryAfter.Equal(state.Quota.NextRecoverAt) {
		t.Fatalf("NextRetryAfter = %v, want = Quota.NextRecoverAt %v", state.NextRetryAfter, state.Quota.NextRecoverAt)
	}
	if reason := registrySuspendReasonForTest(t, authID, model); reason != "quota" {
		t.Fatalf("registry suspension reason = %q, want %q", reason, "quota")
	}
}

// TestMarkResultOverageDisableCoolingKeepsAvailable mirrors the ordinary 429
// disable-cooling contract: a disable-cooling credential hit by an overage
// rejection must never carry a residual cooldown or registry suspension.
func TestMarkResultOverageDisableCoolingKeepsAvailable(t *testing.T) {
	const (
		provider = "overage-cooling-test"
		model    = "overage-cooling-test-model"
		authID   = "overage-cooling-test-auth"
	)

	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

	manager := NewManager(nil, &RoundRobinSelector{}, NoopHook{})
	manager.SetRetryConfig(0, 0, 0)
	auth := &Auth{
		ID:       authID,
		Provider: provider,
		Status:   StatusActive,
		Metadata: map[string]any{"disable_cooling": true},
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register(): %v", errRegister)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   authID,
		Provider: provider,
		Model:    model,
		Success:  false,
		Error:    &Error{Code: "overage", Message: "spend cap reached", HTTPStatus: http.StatusTooManyRequests},
	})

	updated := manager.auths[authID]
	if updated == nil {
		t.Fatal("auth missing after MarkResult")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("model state for %q missing after MarkResult", model)
	}
	if state.Unavailable {
		t.Fatal("state.Unavailable = true, want false (disable-cooling)")
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("NextRetryAfter = %v, want zero (disable-cooling)", state.NextRetryAfter)
	}
	if state.Quota.Exceeded {
		t.Fatal("Quota.Exceeded = true, want false (disable-cooling clears it)")
	}
	if !state.Quota.NextRecoverAt.IsZero() {
		t.Fatalf("Quota.NextRecoverAt = %v, want zero (disable-cooling)", state.Quota.NextRecoverAt)
	}
	if reason := registrySuspendReasonForTest(t, authID, model); reason != "" {
		t.Fatalf("registry suspension reason = %q, want none (disable-cooling)", reason)
	}
}
