package registry

import (
	"testing"
	"time"
)

func listedIDs(models []map[string]any) map[string]bool {
	out := make(map[string]bool, len(models))
	for _, m := range models {
		if id, _ := m["id"].(string); id != "" {
			out[id] = true
		}
	}
	return out
}

func infoIDs(infos []*ModelInfo) map[string]bool {
	out := make(map[string]bool, len(infos))
	for _, info := range infos {
		if info != nil && info.ID != "" {
			out[info.ID] = true
		}
	}
	return out
}

func TestModelRegistry_TransientSuspendDeadline_AutoExpiresCacheAndAllViews(t *testing.T) {
	baseNow := time.Date(2026, 11, 11, 1, 5, 0, 0, time.UTC)
	mockNow := baseNow

	r := newTestModelRegistry()
	r.nowFunc = func() time.Time { return mockNow }

	r.RegisterClient("xai-auth", "xai", []*ModelInfo{
		{ID: "grok-4.7", OwnedBy: "xai", Created: baseNow.Unix()},
		{ID: "grok-4.7-cp", OwnedBy: "xai", Created: baseNow.Unix()},
		{ID: "grok-4.6", OwnedBy: "xai", Created: baseNow.Unix()},
	})
	epoch := r.ClientRegistrationEpoch("xai-auth")
	deadline := baseNow.Add(time.Minute)

	if !r.ApplyClientModelProjections("xai-auth", epoch, 1, []ClientModelProjection{
		{ModelID: "grok-4.7", Suspended: true, SuspendReason: "xai stream error: stream disconnected", SuspendUntil: deadline},
		{ModelID: "grok-4.7-cp", Suspended: true, SuspendReason: "xai stream error: stream disconnected", SuspendUntil: deadline},
		{ModelID: "grok-4.6"},
	}) {
		t.Fatal("expected initial projection to apply")
	}

	// During cooldown: grok-4.7 and grok-4.7-cp are hidden, grok-4.6 remains listed.
	duringOpenAI := listedIDs(r.GetAvailableModels("openai"))
	if duringOpenAI["grok-4.7"] || duringOpenAI["grok-4.7-cp"] || !duringOpenAI["grok-4.6"] {
		t.Fatalf("unexpected /v1/models set during transient cooldown: %v", duringOpenAI)
	}
	if cached := r.availableModelsCache["openai"]; !cached.expiresAt.Equal(deadline) {
		t.Fatalf("availableModelsCache expiresAt = %v, want %v", cached.expiresAt, deadline)
	}
	if infos := infoIDs(r.GetAvailableModelInfos()); infos["grok-4.7"] || infos["grok-4.7-cp"] || !infos["grok-4.6"] {
		t.Fatalf("unexpected GetAvailableModelInfos during transient cooldown: %v", infos)
	}
	if provInfos := infoIDs(r.GetAvailableModelsByProvider("xai")); provInfos["grok-4.7"] || provInfos["grok-4.7-cp"] || !provInfos["grok-4.6"] {
		t.Fatalf("unexpected GetAvailableModelsByProvider during transient cooldown: %v", provInfos)
	}
	if got := r.GetModelCount("grok-4.7"); got != 0 {
		t.Fatalf("GetModelCount(grok-4.7) during cooldown = %d, want 0", got)
	}
	if !r.IsModelSuspendedForClient("xai-auth", "grok-4.7") {
		t.Fatal("expected IsModelSuspendedForClient to report true during active transient cooldown")
	}

	// Still active 1ns before deadline.
	mockNow = deadline.Add(-time.Nanosecond)
	stillCooling := listedIDs(r.GetAvailableModels("openai"))
	if stillCooling["grok-4.7"] || stillCooling["grok-4.7-cp"] {
		t.Fatalf("models listed before deadline elapsed: %v", stillCooling)
	}

	// Exact deadline instant with NO re-projection, NO request, and NO refresh.
	mockNow = deadline
	afterOpenAI := listedIDs(r.GetAvailableModels("openai"))
	if !afterOpenAI["grok-4.7"] || !afterOpenAI["grok-4.7-cp"] || !afterOpenAI["grok-4.6"] {
		t.Fatalf("expected all models listed once transient cooldown expired without re-projection, got %v", afterOpenAI)
	}
	if infos := infoIDs(r.GetAvailableModelInfos()); !infos["grok-4.7"] || !infos["grok-4.7-cp"] {
		t.Fatalf("expected GetAvailableModelInfos to list models after deadline, got %v", infos)
	}
	if provInfos := infoIDs(r.GetAvailableModelsByProvider("xai")); !provInfos["grok-4.7"] || !provInfos["grok-4.7-cp"] {
		t.Fatalf("expected GetAvailableModelsByProvider to list models after deadline, got %v", provInfos)
	}
	if got := r.GetModelCount("grok-4.7"); got != 1 {
		t.Fatalf("GetModelCount(grok-4.7) after cooldown = %d, want 1", got)
	}
	if got := r.GetModelCount("grok-4.7-cp"); got != 1 {
		t.Fatalf("GetModelCount(grok-4.7-cp) after cooldown = %d, want 1", got)
	}
	if r.IsModelSuspendedForClient("xai-auth", "grok-4.7") {
		t.Fatal("expected IsModelSuspendedForClient to report false once transient cooldown expired")
	}

	r.CleanupExpiredQuotas()
	if _, exists := r.models["grok-4.7"].SuspendedClients["xai-auth"]; exists {
		t.Fatal("expected CleanupExpiredQuotas to prune expired transient suspension entry")
	}
	if _, exists := r.models["grok-4.7"].SuspendedClientDeadlines["xai-auth"]; exists {
		t.Fatal("expected CleanupExpiredQuotas to prune expired transient suspension deadline")
	}
}

func TestModelRegistry_OpenEndedSuspensionAndManualOverridePersist(t *testing.T) {
	baseNow := time.Date(2026, 11, 11, 2, 0, 0, 0, time.UTC)
	mockNow := baseNow

	r := newTestModelRegistry()
	r.nowFunc = func() time.Time { return mockNow }

	r.RegisterClient("xai-auth", "xai", []*ModelInfo{{ID: "grok-4.7", OwnedBy: "xai"}})
	epoch := r.ClientRegistrationEpoch("xai-auth")
	deadline := baseNow.Add(time.Minute)

	// Start with a bounded transient suspension.
	if !r.ApplyClientModelProjections("xai-auth", epoch, 1, []ClientModelProjection{
		{ModelID: "grok-4.7", Suspended: true, SuspendReason: "transient 408", SuspendUntil: deadline},
	}) {
		t.Fatal("initial transient projection rejected")
	}

	// Override with manual open-ended SuspendClientModel while already in SuspendedClients.
	r.SuspendClientModel("xai-auth", "grok-4.7", "manual")

	mockNow = baseNow.Add(24 * time.Hour)
	if listed := listedIDs(r.GetAvailableModels("openai")); listed["grok-4.7"] {
		t.Fatalf("manual SuspendClientModel leaked model after earlier transient deadline: %v", listed)
	}
	if !r.IsModelSuspendedForClient("xai-auth", "grok-4.7") {
		t.Fatal("expected manual open-ended suspension to stay active")
	}

	// Resume, then apply an open-ended projection (zero SuspendUntil).
	r.ResumeClientModel("xai-auth", "grok-4.7")
	if !listedIDs(r.GetAvailableModels("openai"))["grok-4.7"] {
		t.Fatal("expected ResumeClientModel to restore listing")
	}

	if !r.ApplyClientModelProjections("xai-auth", epoch, 2, []ClientModelProjection{
		{ModelID: "grok-4.7", Suspended: true, SuspendReason: "disabled"},
	}) {
		t.Fatal("open-ended projection rejected")
	}
	mockNow = baseNow.Add(48 * time.Hour)
	if listed := listedIDs(r.GetAvailableModels("openai")); listed["grok-4.7"] {
		t.Fatalf("open-ended projection leaked model: %v", listed)
	}
}

func TestModelRegistry_ShortQuotaDeadlineRecoversWithDisabledBackup(t *testing.T) {
	baseNow := time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC)
	mockNow := baseNow

	r := newTestModelRegistry()
	r.nowFunc = func() time.Time { return mockNow }

	model := []*ModelInfo{{ID: "grok-4.7", OwnedBy: "xai"}}
	r.RegisterClient("c1-quota", "xai", model)
	r.RegisterClient("c2-disabled", "xai", model)

	epoch1 := r.ClientRegistrationEpoch("c1-quota")
	epoch2 := r.ClientRegistrationEpoch("c2-disabled")

	shortQuotaDeadline := baseNow.Add(time.Minute)
	if !r.ApplyClientModelProjections("c1-quota", epoch1, 1, []ClientModelProjection{
		{ModelID: "grok-4.7", Suspended: true, SuspendReason: "quota", SuspendUntil: shortQuotaDeadline, QuotaExceeded: true},
	}) {
		t.Fatal("quota projection rejected")
	}
	if !r.ApplyClientModelProjections("c2-disabled", epoch2, 1, []ClientModelProjection{
		{ModelID: "grok-4.7", Suspended: true, SuspendReason: "disabled"},
	}) {
		t.Fatal("disabled projection rejected")
	}

	if listedIDs(r.GetAvailableModels("openai"))["grok-4.7"] {
		t.Fatal("expected model hidden while c1 is in 1m quota cooldown and c2 is disabled")
	}

	// Advance past the 1m quota deadline (still within the default 5m modelQuotaExceededWindow).
	mockNow = shortQuotaDeadline.Add(time.Second)
	if !listedIDs(r.GetAvailableModels("openai"))["grok-4.7"] {
		t.Fatal("expected model listed once c1's 1m quota deadline passed")
	}
	if got := r.GetModelCount("grok-4.7"); got != 1 {
		t.Fatalf("GetModelCount(grok-4.7) after c1 quota recovery = %d, want 1", got)
	}
}
