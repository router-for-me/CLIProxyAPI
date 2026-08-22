package registry

import "testing"

// TestResumeClientModelIfReason_RacePreservesNewerSuspension is a red-proof regression test for
// the TOCTOU in the cooldown resume path. Under concurrent requests for the same credential, an
// explicit reason check (GetClientModelSuspensionReason == "invalid_api_key") followed by a
// separate ResumeClientModel transaction would delete a newer suspension recorded between the
// two. ResumeClientModelIfReason verifies and removes the suspension under one registry lock, so
// a suspension whose reason changed in the interim is left intact.
func TestResumeClientModelIfReason_RacePreservesNewerSuspension(t *testing.T) {
	r := newTestModelRegistry()
	const (
		clientID = "auth-1"
		modelID  = "provider/model-1"
	)
	r.RegisterClient(clientID, "provider", []*ModelInfo{{ID: modelID}})
	r.SuspendClientModel(clientID, modelID, "invalid_api_key")

	// A concurrent request records a model-specific failure after the initial reason read but
	// before the resume transaction, changing the suspension reason. SuspendClientModel refuses to
	// overwrite an existing suspension, so this mirrors the interleaved-failure window directly.
	r.mutex.Lock()
	r.models[modelID].SuspendedClients[clientID] = "budget_exceeded"
	r.mutex.Unlock()

	// The conditional resume must refuse: the current reason is no longer invalid_api_key.
	if resumed := r.ResumeClientModelIfReason(clientID, modelID, "invalid_api_key"); resumed {
		t.Fatalf("ResumeClientModelIfReason resumed a model whose current suspension should remain, want no-op")
	}
	if reason := r.GetClientModelSuspensionReason(clientID, modelID); reason != "budget_exceeded" {
		t.Fatalf("newer suspension reason = %q, want budget_exceeded (must survive)", reason)
	}

	// A matching reason resumes normally (after the earlier budget_exceeded suspension clears).
	r.SuspendClientModel(clientID, modelID, "budget_exceeded")
	r.mutex.Lock()
	r.models[modelID].SuspendedClients[clientID] = "invalid_api_key"
	r.mutex.Unlock()
	if resumed := r.ResumeClientModelIfReason(clientID, modelID, "invalid_api_key"); !resumed {
		t.Fatalf("ResumeClientModelIfReason with matching reason did not resume")
	}
	if reason := r.GetClientModelSuspensionReason(clientID, modelID); reason != "" {
		t.Fatalf("suspension reason after resume = %q, want empty", reason)
	}
}

func TestSuspendClientModelReplacingReasons(t *testing.T) {
	r := newTestModelRegistry()
	const (
		clientID = "auth-1"
		modelID  = "provider/model-1"
	)
	r.RegisterClient(clientID, "provider", []*ModelInfo{{ID: modelID}})

	// 1. Initial suspension inserts reason.
	r.SuspendClientModelReplacingReasons(clientID, modelID, "invalid_api_key")
	if reason := r.GetClientModelSuspensionReason(clientID, modelID); reason != "invalid_api_key" {
		t.Fatalf("initial suspension reason = %q, want invalid_api_key", reason)
	}

	// 2. Already suspended with a replaceable reason -> reason replaced.
	r.SuspendClientModelReplacingReasons(clientID, modelID, "not_found", "invalid_api_key")
	if reason := r.GetClientModelSuspensionReason(clientID, modelID); reason != "not_found" {
		t.Fatalf("suspension reason after replacement = %q, want not_found", reason)
	}

	// 3. Already suspended with a non-replaceable reason -> reason preserved.
	r.SuspendClientModelReplacingReasons(clientID, modelID, "invalid_api_key", "quota")
	if reason := r.GetClientModelSuspensionReason(clientID, modelID); reason != "not_found" {
		t.Fatalf("suspension reason after non-matching replacement = %q, want not_found (preserved)", reason)
	}

	// 4. Already suspended with no replaceable args -> reason preserved (behaves like SuspendClientModel).
	r.SuspendClientModelReplacingReasons(clientID, modelID, "invalid_api_key")
	if reason := r.GetClientModelSuspensionReason(clientID, modelID); reason != "not_found" {
		t.Fatalf("suspension reason without replaceable args = %q, want not_found (preserved)", reason)
	}

	// 5. SuspendClientModel delegation preserves existing reason.
	r.SuspendClientModel(clientID, modelID, "invalid_api_key")
	if reason := r.GetClientModelSuspensionReason(clientID, modelID); reason != "not_found" {
		t.Fatalf("suspension reason via SuspendClientModel = %q, want not_found (preserved)", reason)
	}
}
