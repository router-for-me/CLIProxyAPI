package registry

import (
	"testing"
	"time"
)

// Healthy providers remain discoverable throughout quota windows and refreshes.
func TestCredentialQuotaKeepsHealthyCatalogAcrossRefresh(t *testing.T) {
	base := time.Date(2026, 9, 22, 8, 53, 19, 0, time.UTC)
	registration := &ModelRegistration{
		Count:                2,
		QuotaExceededClients: map[string]*time.Time{"oauth": &base},
		SuspendedClients:     map[string]string{"oauth": "credential_quota"},
	}
	for _, tc := range []struct {
		offset time.Duration
		want   bool
	}{
		{0, true}, {5*time.Minute - time.Nanosecond, true},
		{5 * time.Minute, true}, {6 * time.Minute, true},
	} {
		available, _ := modelRegistrationAvailability(registration, base.Add(tc.offset))
		t.Logf("since projection=%v listed=%v; OAuth suspension unchanged", tc.offset, available)
		if available != tc.want {
			t.Fatalf("at %v: listed=%v, want %v", tc.offset, available, tc.want)
		}
	}
	// A registry refresh clears the old quota timestamp; reapplying an active
	// cooldown starts the five-minute window again without another upstream 429.
	r := newTestModelRegistry()
	models := []*ModelInfo{{ID: "audit-luna", OwnedBy: "openai"}}
	r.RegisterClient("healthy", "codex", models)
	r.RegisterClient("oauth", "codex", models)
	_, epoch := r.GetModelsAndEpochForClient("oauth")
	projection := []ClientModelProjection{{ModelID: "audit-luna", Suspended: true, SuspendReason: "credential_quota", QuotaExceeded: true}}
	if !r.ApplyClientModelProjections("oauth", epoch, 1, projection) {
		t.Fatal("initial projection rejected")
	}
	if got := len(r.GetAvailableModels("openai")); got != 1 {
		t.Fatalf("initial cached catalog size=%d, want 1", got)
	}
	cached := r.availableModelsCache["openai"]
	if cached.expiresAt.IsZero() {
		t.Fatal("quota-sensitive catalog has no automatic expiration")
	}
	old := time.Now().Add(-6 * time.Minute)
	r.models["audit-luna"].QuotaExceededClients["oauth"] = &old
	// Advance the observed timestamps instead of sleeping. Let the actual
	// public listing path detect the expired cache without manual invalidation.
	cached.expiresAt = time.Now().Add(-time.Second)
	r.availableModelsCache["openai"] = cached
	if got := len(r.GetAvailableModels("openai")); got != 1 {
		t.Fatalf("after five-minute window: models=%d, want 1", got)
	}
	r.RegisterClient("oauth", "codex", models)
	_, epoch = r.GetModelsAndEpochForClient("oauth")
	if !r.ApplyClientModelProjections("oauth", epoch, 2, projection) {
		t.Fatal("refreshed projection rejected")
	}
	if got := len(r.GetAvailableModels("openai")); got != 1 {
		t.Fatalf("after refresh with unchanged OAuth cooldown: models=%d, want 1", got)
	}
	t.Log("healthy catalog stays visible before expiration, after expiration, and after re-registration")
}
