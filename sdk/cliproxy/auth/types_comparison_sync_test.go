package auth

import (
	"reflect"
	"testing"
	"time"
)

func TestNormalizeForComparisonClearsUpstreamRuntimeAndSnapshotState(t *testing.T) {
	now := time.Now()
	base := &Auth{ID: "comparison", Provider: "codex", Metadata: map[string]any{"access_token": "persisted-token"}}
	runtime := base.Clone()
	runtime.BindFileSnapshot("auth.json", []byte(`{"access_token":"persisted-token"}`))
	runtime.RejectedSIWCAccessToken = "rejected-access"
	runtime.RejectedRefreshToken = "rejected-refresh"
	runtime.RejectedSIWCRefreshToken = "rejected-siwc-refresh"
	runtime.RenewalWarnedRefreshToken = "warned-refresh"
	runtime.RequiresSignIn = true
	runtime.RefreshError = &Error{Message: "runtime refresh failure"}
	runtime.RenewalEventAt = now
	runtime.AdoptedAt = now
	runtime.AdoptedGrantHash = "grant-hash"
	runtime.InferenceVerified = true
	runtime.LastInferenceVerifiedAt = now
	runtime.RenewalVerified = true
	runtime.LastRenewalVerifiedAt = now
	if !reflect.DeepEqual(base.NormalizeForComparison(), runtime.NormalizeForComparison()) {
		t.Fatal("upstream runtime bookkeeping changed semantic auth comparison")
	}
	if !runtime.HasFileSnapshot() || !runtime.RequiresSignIn || runtime.RefreshError == nil {
		t.Fatal("normalization mutated original runtime state")
	}
	runtime.Metadata["access_token"] = "new-persisted-token"
	if reflect.DeepEqual(base.NormalizeForComparison(), runtime.NormalizeForComparison()) {
		t.Fatal("normalization hid a persisted credential change")
	}
}
