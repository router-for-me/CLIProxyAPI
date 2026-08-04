package auth

import "testing"

func TestNormalizeSessionAffinityMaxRequests(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   int
		want int
	}{
		{in: -1, want: -1},
		{in: 0, want: -1},
		{in: -99, want: -1},
		{in: 1, want: 1},
		{in: 20, want: 20},
	}
	for _, tc := range cases {
		if got := normalizeSessionAffinityMaxRequests(tc.in); got != tc.want {
			t.Fatalf("normalizeSessionAffinityMaxRequests(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestResolveSessionAffinityMaxRequests_Specificity(t *testing.T) {
	t.Parallel()

	rules := []SessionAffinityRuleLimit{
		{Provider: "xai", MaxRequests: 5},
		{Model: "grok-4.5", MaxRequests: 10},
		{Provider: "xai", Model: "grok-4.5", MaxRequests: 20},
		{Provider: "claude", Model: "claude-3", MaxRequests: 3},
	}
	compiled := compileSessionAffinityRules(rules)

	cases := []struct {
		name     string
		provider string
		model    string
		global   int
		want     int
	}{
		{name: "provider+model wins", provider: "xai", model: "grok-4.5", global: -1, want: 20},
		{name: "model-only", provider: "other", model: "grok-4.5", global: -1, want: 10},
		{name: "provider-only", provider: "xai", model: "grok-3", global: -1, want: 5},
		{name: "global fallback", provider: "gemini", model: "gemini-2.5", global: 7, want: 7},
		{name: "global unlimited", provider: "gemini", model: "gemini-2.5", global: -1, want: -1},
		{name: "thinking suffix stripped", provider: "xai", model: "grok-4.5(high)", global: -1, want: 20},
		{name: "case insensitive", provider: "XAI", model: "Grok-4.5", global: -1, want: 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := resolveSessionAffinityMaxRequests(tc.global, compiled, tc.provider, tc.model)
			if got != tc.want {
				t.Fatalf("resolve = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSessionAffinityMaxRequestsExceeded(t *testing.T) {
	t.Parallel()
	if sessionAffinityMaxRequestsExceeded(-1, 100) {
		t.Fatal("unlimited should never exceed")
	}
	if sessionAffinityMaxRequestsExceeded(3, 3) {
		t.Fatal("hits==max should still allow the Nth sticky pick")
	}
	if !sessionAffinityMaxRequestsExceeded(3, 4) {
		t.Fatal("hits>max should rebind")
	}
}
