package util

import "testing"

// TestJoinOpenAICompatUpstreamURL covers the base-URL joining helper that
// auto-inserts the conventional "/v1" version segment when the configured
// base URL does not already carry one.
//
// The regression it guards: the OpenAI-Compat executor and the manage-CPA
// "discover models" remote-probe each used to append their endpoint suffix
// verbatim, so whether the operator entered a base URL with or without
// "/v1", one of the code paths hit a 404 from the upstream (double "/v1"
// when the base already carried one, missing "/v1" when it did not).
// JoinOpenAICompatUpstreamURL makes the behaviour symmetric for both
// callers.
func TestJoinOpenAICompatUpstreamURL(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		suffix  string
		want    string
	}{
		{"no version segment inserts v1", "https://opencode.ai/zen/go", "/chat/completions", "https://opencode.ai/zen/go/v1/chat/completions"},
		{"trailing slash trimmed then v1 inserted", "https://opencode.ai/zen/go/", "/chat/completions", "https://opencode.ai/zen/go/v1/chat/completions"},
		{"trailing whitespace trimmed", "  https://api.example.com/v1  ", "/chat/completions", "https://api.example.com/v1/chat/completions"},
		{"already has v1 unchanged", "https://openrouter.ai/api/v1", "/chat/completions", "https://openrouter.ai/api/v1/chat/completions"},
		{"already has v2 unchanged", "https://example.com/api/v2", "/chat/completions", "https://example.com/api/v2/chat/completions"},
		{"v1 base + /models suffix (remote-probe)", "https://openrouter.ai/api/v1", "/models", "https://openrouter.ai/api/v1/models"},
		{"no v1 base + /models suffix (remote-probe)", "https://api.example.com", "/models", "https://api.example.com/v1/models"},
		{"images endpoint no version", "https://api.example.com", "/images/generations", "https://api.example.com/v1/images/generations"},
		{"responses compact with v1", "https://api.example.com/v1", "/responses/compact", "https://api.example.com/v1/responses/compact"},
		{"responses compact without v1", "https://api.example.com", "/responses/compact", "https://api.example.com/v1/responses/compact"},
		{"path component named like version but not numeric keeps v1", "https://api.example.com/version", "/chat/completions", "https://api.example.com/version/v1/chat/completions"},
		{"empty base returns suffix", "", "/chat/completions", "/chat/completions"},
		{"missing leading slash on suffix is added", "https://api.example.com/v1", "chat/completions", "https://api.example.com/v1/chat/completions"},
		{"empty suffix returns trimmed base", "https://api.example.com/v1/", "", "https://api.example.com/v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := JoinOpenAICompatUpstreamURL(tc.baseURL, tc.suffix)
			if got != tc.want {
				t.Fatalf("JoinOpenAICompatUpstreamURL(%q, %q) = %q, want %q", tc.baseURL, tc.suffix, got, tc.want)
			}
		})
	}
}
