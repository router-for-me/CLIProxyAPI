package registry

import "testing"

func TestResolveAntigravityUpstreamModel(t *testing.T) {
	for _, tc := range []struct {
		name    string
		model   string
		catalog map[string]struct{}
		want    string
	}{
		{"argon unknown catalog retains fallback", "gemini-4-argon", nil, "gemini-4-argon-high"},
		{"argon high is entitled", "gemini-4-argon", map[string]struct{}{"gemini-4-argon-high": {}}, "gemini-4-argon-high"},
		{"argon low only", "gemini-4-argon", map[string]struct{}{"gemini-4-argon-low": {}}, "gemini-4-argon-low"},
		{"argon exact clean ID", "gemini-4-argon", map[string]struct{}{"gemini-4-argon": {}, "gemini-4-argon-high": {}}, "gemini-4-argon"},
		{"argon empty catalog grants nothing", "gemini-4-argon", map[string]struct{}{}, ""},
		{"argon unrelated entitlement grants nothing", "gemini-4-argon", map[string]struct{}{"gemini-3.8-flash-high": {}}, ""},
		{"unknown catalog retains fallback", "gemini-3.8-flash", nil, "gemini-3.8-flash-high"},
		{"empty catalog grants nothing", "gemini-3.8-flash", map[string]struct{}{}, ""},
		{"low only", "gemini-3.8-flash", map[string]struct{}{"gemini-3.8-flash-low": {}}, "gemini-3.8-flash-low"},
		{"tiered only", "gemini-3.8-flash", map[string]struct{}{"gemini-3.8-flash-tiered": {}}, "gemini-3.8-flash-tiered"},
		{"pro without agent", "gemini-3.1-pro", map[string]struct{}{"gemini-3.1-pro-high": {}}, "gemini-3.1-pro-high"},
		{"preferred high is entitled", "gemini-3.8-flash", map[string]struct{}{"gemini-3.8-flash-high": {}, "gemini-3.8-flash-low": {}}, "gemini-3.8-flash-high"},
		{"preferred agent is entitled", "gemini-3.1-pro", map[string]struct{}{"gemini-pro-agent": {}, "gemini-3.1-pro-high": {}}, "gemini-pro-agent"},
		{"exact clean ID is entitled", "gemini-3.8-flash", map[string]struct{}{"gemini-3.8-flash": {}, "gemini-3.8-flash-low": {}}, "gemini-3.8-flash"},
		{"exact raw ID remains raw", "gemini-3.8-flash-low", map[string]struct{}{"gemini-3.8-flash-high": {}, "gemini-3.8-flash-low": {}}, "gemini-3.8-flash-low"},
		{"fallback is deterministic", "gemini-3.8-flash", map[string]struct{}{"gemini-3.8-flash-medium": {}, "gemini-3.8-flash-low": {}}, "gemini-3.8-flash-low"},
		{"other model grants nothing", "gemini-3.8-flash", map[string]struct{}{"gemini-3.7-flash-low": {}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveAntigravityUpstreamModel(tc.model, tc.catalog); got != tc.want {
				t.Fatalf("model=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestAntigravityArgonCanonicalCatalog(t *testing.T) {
	models := WithAntigravityBuiltins([]*ModelInfo{
		{ID: "gemini-4-argon-high"},
		{ID: "gemini-4-argon-low"},
		{ID: "gemini-4-argon"},
	})
	count := 0
	for _, model := range models {
		if model.ID != "gemini-4-argon" {
			if NormalizeAntigravityModelID(model.ID) == "gemini-4-argon" {
				t.Fatalf("raw Argon variant exposed in catalog: %q", model.ID)
			}
			continue
		}
		count++
		if model.Name != model.ID || model.Description != "Gemini 4 Argon" || model.ContextLength != 1048576 || model.MaxCompletionTokens != 1048576 {
			t.Fatalf("canonical Argon metadata was lost: %+v", model)
		}
		if model.Thinking == nil || model.Thinking.Min != 1 || model.Thinking.Max != 65535 || len(model.Thinking.Levels) != 3 {
			t.Fatalf("Argon thinking capabilities were lost: %+v", model.Thinking)
		}
		if !model.SupportsWebSearch || len(model.SupportedInputModalities) != 4 || len(model.SupportedOutputModalities) != 1 {
			t.Fatal("Argon search or modality capabilities were lost")
		}
	}
	if count != 1 {
		t.Fatalf("canonical Argon count = %d, want 1", count)
	}
	for _, raw := range []string{"gemini-4-argon-high", "gemini-4-argon-medium", "gemini-4-argon-low", "gemini-4-argon-tiered"} {
		if got := NormalizeAntigravityModelID(raw); got != "gemini-4-argon" {
			t.Errorf("normalize %q = %q", raw, got)
		}
		if info := LookupModelInfo(raw, "antigravity"); info == nil || info.ID != "gemini-4-argon" {
			t.Errorf("raw variant %q did not resolve to canonical metadata", raw)
		}
	}
	if got := NormalizeAntigravityModelID("custom-model-high"); got != "custom-model-high" {
		t.Fatalf("unknown model must not be normalized: %q", got)
	}
}
