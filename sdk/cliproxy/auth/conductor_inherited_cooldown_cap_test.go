package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// A cascaded static api-key credential fronts another gateway that pools several
// accounts behind one key. When one downstream account is quota-exhausted, the
// upstream answers 429 with a multi-day Retry-After. The per-credential cap must bound
// the inherited cooldown so the whole credential (and the still-healthy sibling
// accounts behind it) becomes selectable again after the capped window, instead of
// staying locked for the entire downstream quota window.
func TestMarkResult_429InheritedQuotaCooldown_CappedByAuthMetadata(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-cascade-capped",
		Provider: "claude",
		Metadata: map[string]any{"max_quota_cooldown_seconds": 30},
	}

	model := "claude-opus-5-5"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	now := time.Now()
	upstreamTTL := 24 * time.Hour
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   "claude",
		Model:      model,
		Success:    false,
		RetryAfter: &upstreamTTL,
		Error: &Error{
			HTTPStatus: http.StatusTooManyRequests,
			Message:    "RESOURCE_EXHAUSTED",
		},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatal("expected model state to be present")
	}

	// The 24h upstream Retry-After must be truncated to the 30s cap, not honored in full.
	wantMin := now.Add(30*time.Second - time.Second)
	wantMax := now.Add(30*time.Second + 2*time.Second)
	if state.NextRetryAfter.Before(wantMin) || state.NextRetryAfter.After(wantMax) {
		t.Fatalf("expected capped NextRetryAfter ~ now+30s, got %v (diff %v)",
			state.NextRetryAfter, state.NextRetryAfter.Sub(now))
	}
	// Sanity check: it must be dramatically shorter than the upstream 24h window.
	if state.NextRetryAfter.After(now.Add(time.Minute)) {
		t.Fatalf("cooldown was not capped: %v exceeds one minute for a 30s cap", state.NextRetryAfter.Sub(now))
	}
}

// Without the opt-in cap the upstream Retry-After duration is preserved exactly,
// guarding the legacy behavior for credentials that do not set the field.
func TestMarkResult_429InheritedQuotaCooldown_NoCapPreservesLongTTL(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-cascade-uncapped",
		Provider: "claude",
	}

	model := "claude-opus-5-5"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	now := time.Now()
	upstreamTTL := 24 * time.Hour
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   "claude",
		Model:      model,
		Success:    false,
		RetryAfter: &upstreamTTL,
		Error: &Error{
			HTTPStatus: http.StatusTooManyRequests,
			Message:    "RESOURCE_EXHAUSTED",
		},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatal("expected model state to be present")
	}

	expected := now.Add(upstreamTTL)
	if state.NextRetryAfter.Before(expected.Add(-2*time.Second)) || state.NextRetryAfter.After(expected.Add(2*time.Second)) {
		t.Fatalf("expected uncapped NextRetryAfter ~ %v (24h preserved), got %v", expected, state.NextRetryAfter)
	}
}

// The auth-level failure path (applyAuthFailureState) applies the same cap.
func TestApplyAuthFailureState_429InheritedQuotaCooldown_Capped(t *testing.T) {
	now := time.Now()
	upstreamTTL := 12 * time.Hour
	auth := &Auth{
		ID:       "auth-level-capped",
		Metadata: map[string]any{"max_quota_cooldown_seconds": 45},
	}
	quotaErr := &Error{
		HTTPStatus: http.StatusTooManyRequests,
		Message:    "quota exhausted",
	}

	applyAuthFailureState(auth, quotaErr, &upstreamTTL, now, false)

	if auth.NextRetryAfter.IsZero() {
		t.Fatal("expected NextRetryAfter to be set")
	}
	wantMin := now.Add(45*time.Second - time.Second)
	wantMax := now.Add(45*time.Second + 2*time.Second)
	if auth.NextRetryAfter.Before(wantMin) || auth.NextRetryAfter.After(wantMax) {
		t.Fatalf("expected auth-level capped NextRetryAfter ~ now+45s, got %v (diff %v)",
			auth.NextRetryAfter, auth.NextRetryAfter.Sub(now))
	}
}

// A non-positive cap value is treated as unset and must not shorten the cooldown.
func TestApplyInheritedQuotaCooldownCap_NonPositiveIsNoop(t *testing.T) {
	cooldown := 2 * time.Hour

	for _, meta := range []map[string]any{
		nil,
		{},
		{"max_quota_cooldown_seconds": 0},
		{"max_quota_cooldown_seconds": -5},
	} {
		auth := &Auth{ID: "auth-cap-unset", Metadata: meta}
		if got := applyInheritedQuotaCooldownCap(auth, cooldown); got != cooldown {
			t.Fatalf("expected cooldown unchanged (%v) for metadata %v, got %v", cooldown, meta, got)
		}
	}
}
