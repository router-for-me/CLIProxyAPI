package registry

import "testing"

func TestStripModelVariantSuffix(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"no variant", "gemini-2.5-pro", "gemini-2.5-pro"},
		{"terminal", "gemini-3.1-pro-preview-customtools", "gemini-3.1-pro-preview"},
		{"with thinking int", "gemini-3.1-pro-preview-customtools(8192)", "gemini-3.1-pro-preview(8192)"},
		{"with thinking level", "gemini-3.1-pro-preview-customtools(high)", "gemini-3.1-pro-preview(high)"},
		{"with thinking auto", "gemini-3.1-pro-preview-customtools(auto)", "gemini-3.1-pro-preview(auto)"},
		{"chained segment", "gemini-2.0-flash-customtools-thinking", "gemini-2.0-flash-thinking"},
		{"chained with level", "gemini-2.0-flash-customtools-thinking(4096)", "gemini-2.0-flash-thinking(4096)"},
		{"trailing after thinking suffix order", "gemini-2.0-flash-thinking-exp-customtools", "gemini-2.0-flash-thinking-exp"},
		{"trailing with thinking after customtools", "gemini-2.0-flash-thinking-exp-customtools(8192)", "gemini-2.0-flash-thinking-exp(8192)"},
		{"double customtools", "gemini-foo-customtools-customtools", "gemini-foo"},
		{"no partial match", "gemini-customtoolsextra", "gemini-customtoolsextra"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StripModelVariantSuffix(tc.in)
			if got != tc.want {
				t.Fatalf("StripModelVariantSuffix(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestStripModelVariantSuffixIdempotent(t *testing.T) {
	inputs := []string{
		"gemini-3.1-pro-preview-customtools(8192)",
		"gemini-2.0-flash-customtools-thinking",
		"gemini-2.5-pro",
	}
	for _, in := range inputs {
		once := StripModelVariantSuffix(in)
		twice := StripModelVariantSuffix(once)
		if once != twice {
			t.Fatalf("StripModelVariantSuffix not idempotent for %q: %q vs %q", in, once, twice)
		}
	}
}
