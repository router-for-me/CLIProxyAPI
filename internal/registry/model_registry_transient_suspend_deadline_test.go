package registry

import (
	"testing"
	"time"
)

// A transient suspension with a deadline must stop hiding the model once the
// deadline passes, without any re-projection (no success, no credential
// refresh). The deadline must also expire the availability cache exactly then
// so GET /v1/models flips back on its own (#7421).
func TestModelRegistrationAvailability_TransientSuspensionDeadline(t *testing.T) {
	now := time.Now()
	deadline := now.Add(time.Minute)

	registration := &ModelRegistration{
		Count:                    1,
		QuotaExceededClients:     make(map[string]*time.Time),
		SuspendedClients:         map[string]string{"xai-auth": "xai stream error: stream disconnected"},
		SuspendedClientDeadlines: map[string]time.Time{"xai-auth": deadline},
	}

	available, expiresAt := modelRegistrationAvailability(registration, now)
	if available {
		t.Fatal("model should be hidden while the transient cooldown is live")
	}
	if !expiresAt.Equal(deadline) {
		t.Fatalf("expiresAt = %v, want the cooldown deadline %v so the listing refreshes then", expiresAt, deadline)
	}

	available, _ = modelRegistrationAvailability(registration, deadline.Add(time.Second))
	if !available {
		t.Fatal("model should be listed again once the deadline passed, without a re-projection")
	}
}

// Suspensions without a deadline (disabled credentials, manual suspensions)
// keep hiding the model indefinitely.
func TestModelRegistrationAvailability_DeadlinelessSuspensionPersists(t *testing.T) {
	registration := &ModelRegistration{
		Count:            1,
		SuspendedClients: map[string]string{"auth-a": "credential disabled"},
	}
	if available, _ := modelRegistrationAvailability(registration, time.Now().Add(time.Hour)); available {
		t.Fatal("deadline-less suspension must keep hiding the model")
	}
}

// ApplyClientModelProjections must record the projected SuspendUntil, keep it
// across repeated identical projections, replace it when it changes, and drop
// it when the suspension is lifted or becomes open-ended.
func TestApplyClientModelProjections_RecordsSuspendUntil(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("xai-auth", "xai", []*ModelInfo{{ID: "grok-4.7"}})
	epoch := r.ClientRegistrationEpoch("xai-auth")

	deadline := time.Now().Add(time.Minute)
	if !r.ApplyClientModelProjections("xai-auth", epoch, 1, []ClientModelProjection{
		{ModelID: "grok-4.7", Suspended: true, SuspendReason: "xai stream error: stream disconnected", SuspendUntil: deadline},
	}) {
		t.Fatal("initial projection rejected")
	}
	registration := r.models["grok-4.7"]
	if got := registration.SuspendedClientDeadlines["xai-auth"]; !got.Equal(deadline) {
		t.Fatalf("SuspendedClientDeadlines[xai-auth] = %v, want %v", got, deadline)
	}

	r.mutex.Lock()
	models, expiresAt := r.buildAvailableModelsLocked("openai", time.Now())
	r.mutex.Unlock()
	if len(models) != 0 {
		t.Fatalf("model listed during live transient cooldown: %+v", models)
	}
	if !expiresAt.Equal(deadline) {
		t.Fatalf("buildAvailableModelsLocked expiresAt = %v, want %v", expiresAt, deadline)
	}

	// An extended deadline replaces the previous one.
	extended := deadline.Add(5 * time.Minute)
	if !r.ApplyClientModelProjections("xai-auth", epoch, 2, []ClientModelProjection{
		{ModelID: "grok-4.7", Suspended: true, SuspendReason: "xai stream error: stream disconnected", SuspendUntil: extended},
	}) {
		t.Fatal("extended projection rejected")
	}
	if got := r.models["grok-4.7"].SuspendedClientDeadlines["xai-auth"]; !got.Equal(extended) {
		t.Fatalf("deadline was not extended: %v, want %v", got, extended)
	}

	// Lifting the suspension removes the deadline too.
	if !r.ApplyClientModelProjections("xai-auth", epoch, 3, []ClientModelProjection{
		{ModelID: "grok-4.7"},
	}) {
		t.Fatal("resuming projection rejected")
	}
	if _, ok := r.models["grok-4.7"].SuspendedClientDeadlines["xai-auth"]; ok {
		t.Fatal("deadline entry survived a lifted suspension")
	}

	// An open-ended suspension drops a previously recorded deadline.
	if !r.ApplyClientModelProjections("xai-auth", epoch, 4, []ClientModelProjection{
		{ModelID: "grok-4.7", Suspended: true, SuspendReason: "credential disabled"},
	}) {
		t.Fatal("open-ended projection rejected")
	}
	if _, ok := r.models["grok-4.7"].SuspendedClientDeadlines["xai-auth"]; ok {
		t.Fatal("deadline entry survived an open-ended suspension")
	}
	if available, _ := modelRegistrationAvailability(r.models["grok-4.7"], time.Now().Add(time.Hour)); available {
		t.Fatal("open-ended suspension must keep hiding the model")
	}
}

// After the deadline passes with no further events, the rebuilt listing
// includes the model again: this is the /v1/models view flipping back purely
// with the passage of time.
func TestBuildAvailableModels_ListsModelAfterDeadlineWithoutReprojection(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("xai-auth", "xai", []*ModelInfo{{ID: "grok-4.7"}})
	epoch := r.ClientRegistrationEpoch("xai-auth")

	now := time.Now()
	deadline := now.Add(time.Minute)
	if !r.ApplyClientModelProjections("xai-auth", epoch, 1, []ClientModelProjection{
		{ModelID: "grok-4.7", Suspended: true, SuspendReason: "xai stream error: stream disconnected", SuspendUntil: deadline},
	}) {
		t.Fatal("projection rejected")
	}

	r.mutex.Lock()
	during, _ := r.buildAvailableModelsLocked("openai", deadline.Add(-time.Second))
	after, _ := r.buildAvailableModelsLocked("openai", deadline.Add(time.Second))
	r.mutex.Unlock()

	if len(during) != 0 {
		t.Fatalf("model listed during the cooldown: %+v", during)
	}
	if len(after) != 1 || after[0]["id"] != "grok-4.7" {
		t.Fatalf("model not listed after the cooldown ended without any re-projection: %+v", after)
	}
}
