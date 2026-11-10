package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// A transient failure (408) must hide the model from /v1/models only while its
// retry deadline is still live. Once NextRetryAfter is in the past, the
// projection must list the model again without requiring a successful request
// or a credential refresh, matching the selector's availabilityBlock rule
// (#7421).
func TestManager_ClientModelProjection_TransientCooldownExpires(t *testing.T) {
	m, auth := newCooldownMonotonicManager(t, "grok-4.7")

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "grok-4.7",
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusRequestTimeout, Message: "xai stream error: stream disconnected"},
	})

	snap, _ := m.GetByID(auth.ID)
	state := existingModelState(snap, canonicalModelKey("grok-4.7"))
	if state == nil || !state.Unavailable || state.NextRetryAfter.IsZero() {
		t.Fatalf("precondition failed: expected a live transient cooldown, got %+v", state)
	}

	// The transient failure suspends the model in the registry (hidden from /v1/models).
	reg := registry.GetGlobalRegistry()
	if !reg.IsModelSuspendedForClient(auth.ID, "grok-4.7") {
		t.Fatal("model should be suspended in the registry right after the transient failure")
	}

	during := state.NextRetryAfter.Add(-1 * time.Second)
	after := state.NextRetryAfter.Add(1 * time.Second)

	projDuring := m.clientModelProjectionForAuth(snap, "grok-4.7", during)
	if !projDuring.Suspended {
		t.Fatalf("projection must suspend the model while the transient cooldown is live: %+v", projDuring)
	}
	if !projDuring.SuspendUntil.Equal(state.NextRetryAfter) {
		t.Fatalf("projection SuspendUntil = %v, want the model state NextRetryAfter %v", projDuring.SuspendUntil, state.NextRetryAfter)
	}

	projAfter := m.clientModelProjectionForAuth(snap, "grok-4.7", after)
	if projAfter.Suspended {
		t.Fatalf("projection must list the model again once NextRetryAfter passed, without a success or refresh: %+v", projAfter)
	}

	// Routing already uses this rule: blocked during the cooldown, still
	// routable after it expired. The projection must agree with both sides.
	blockedDuring, _, _ := isAuthBlockedForModel(snap, "grok-4.7", during)
	if !blockedDuring {
		t.Fatal("selector should block the model during the transient cooldown")
	}
	blockedAfter, _, _ := isAuthBlockedForModel(snap, "grok-4.7", after)
	if blockedAfter {
		t.Fatal("selector should route requests to the model after the cooldown expired")
	}
}

// The registry suspension recorded by a transient failure must become
// ineffective once the cooldown deadline passes, so /v1/models lists the model
// again even though no event re-projects the credential (#7421).
func TestManager_TransientCooldownRegistryDeadlineExpiresListing(t *testing.T) {
	m, auth := newCooldownMonotonicManager(t, "grok-4.7")

	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "grok-4.7",
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusRequestTimeout, Message: "xai stream error: stream disconnected"},
	})

	snap, _ := m.GetByID(auth.ID)
	state := existingModelState(snap, canonicalModelKey("grok-4.7"))
	if state == nil || state.NextRetryAfter.IsZero() {
		t.Fatalf("precondition failed: expected a transient cooldown deadline, got %+v", state)
	}

	reg := registry.GetGlobalRegistry()
	epoch := reg.ClientRegistrationEpoch(auth.ID)

	// Simulate the /v1/models rebuild that happens after the deadline passed
	// (the availability cache expires at the recorded deadline): re-apply the
	// same suspension shape the conductor projected, with the deadline now in
	// the past, and confirm the model is listed without any new auth event.
	stale := state.NextRetryAfter.Add(-time.Minute)
	if !reg.ApplyClientModelProjections(auth.ID, epoch, snap.Generation, []registry.ClientModelProjection{
		{ModelID: "grok-4.7", Suspended: true, SuspendReason: "xai stream error: stream disconnected", SuspendUntil: stale},
	}) {
		t.Fatal("projection with stale deadline rejected")
	}

	listed := false
	for _, model := range reg.GetAvailableModels("openai") {
		if id, _ := model["id"].(string); id == "grok-4.7" {
			listed = true
		}
	}
	if !listed {
		t.Fatal("model with an expired transient deadline must be listed again in /v1/models")
	}

	// A live deadline must still hide it, proving the flip is time-driven.
	live := state.NextRetryAfter
	if !reg.ApplyClientModelProjections(auth.ID, epoch, snap.Generation+1, []registry.ClientModelProjection{
		{ModelID: "grok-4.7", Suspended: true, SuspendReason: "xai stream error: stream disconnected", SuspendUntil: live},
	}) {
		t.Fatal("projection with live deadline rejected")
	}
	for _, model := range reg.GetAvailableModels("openai") {
		if id, _ := model["id"].(string); id == "grok-4.7" {
			t.Fatal("model with a live transient deadline must stay hidden from /v1/models")
		}
	}
}
