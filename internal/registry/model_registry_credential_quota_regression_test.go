package registry

import "testing"

func TestAvailableModelsKeepsHealthyProviderDuringCredentialQuota(t *testing.T) {
	r := newTestModelRegistry()
	model := &ModelInfo{ID: "gpt-5.6-luna", OwnedBy: "openai", Type: "openai"}
	r.RegisterClient("healthy-api-provider", "codex", []*ModelInfo{model})
	r.RegisterClient("exhausted-oauth-account", "codex", []*ModelInfo{model})
	_, epoch := r.GetModelsAndEpochForClient("exhausted-oauth-account")
	if !r.ApplyClientModelProjections("exhausted-oauth-account", epoch, 1, []ClientModelProjection{{
		ModelID: model.ID, Suspended: true, SuspendReason: "credential_quota", QuotaExceeded: true,
	}}) {
		t.Fatal("failed to apply the observed OAuth quota state")
	}
	if got := r.GetAvailableModels("openai"); len(got) != 1 {
		t.Errorf("/v1/models hid a model with a healthy API provider: got %v", got)
	}
	if got := r.GetAvailableModelsByProvider("codex"); len(got) != 1 {
		t.Errorf("provider catalog hid a model with a healthy API provider: got %v", got)
	}
	if got := r.GetAvailableModelInfos(); len(got) != 1 {
		t.Errorf("metadata catalog hid a model with a healthy API provider: got %v", got)
	}
	if got := r.GetModelCount(model.ID); got != 1 {
		t.Errorf("healthy API provider count = %d, want 1", got)
	}
	_, epoch = r.GetModelsAndEpochForClient("healthy-api-provider")
	if !r.ApplyClientModelProjections("healthy-api-provider", epoch, 1, []ClientModelProjection{{
		ModelID: model.ID, Suspended: true, SuspendReason: "manual",
	}}) {
		t.Fatal("failed to suspend the second provider")
	}
	if got := r.GetAvailableModels("openai"); len(got) != 0 {
		t.Errorf("catalog should hide a model after both providers become unavailable: %v", got)
	}
	if got := r.GetAvailableModelsByProvider("codex"); len(got) != 0 {
		t.Errorf("provider catalog should hide a model after both providers become unavailable: %v", got)
	}
	if got := r.GetModelCount(model.ID); got != 0 {
		t.Errorf("unavailable provider count = %d, want 0", got)
	}
}
