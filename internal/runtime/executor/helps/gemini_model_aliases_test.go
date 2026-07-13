package helps

import "testing"

func TestCanonicalGeminiUpstreamModel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in, want string
	}{
		{"gemini-3.1-flash-lite-preview", "gemini-3.1-flash-lite"},
		{"  gemini-3.1-flash-lite-preview  ", "gemini-3.1-flash-lite"},
		{"models/gemini-3.1-flash-lite-preview", "gemini-3.1-flash-lite"},
		{"gemini-3.1-flash-lite", "gemini-3.1-flash-lite"},
		{"gemini-2.5-flash", "gemini-2.5-flash"},
		{"", ""},
		{"   ", ""},
	}
	for _, tc := range cases {
		if got := CanonicalGeminiUpstreamModel(tc.in); got != tc.want {
			t.Fatalf("CanonicalGeminiUpstreamModel(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}
