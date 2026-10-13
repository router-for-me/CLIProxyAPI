package registry

import "testing"

func TestResolveAntigravityUpstreamModel(t *testing.T) {
	for _, tc := range []struct {
		name    string
		model   string
		catalog map[string]struct{}
		want    string
	}{
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
