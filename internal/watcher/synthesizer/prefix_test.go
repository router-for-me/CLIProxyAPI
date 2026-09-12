package synthesizer

import "testing"

// The management API reports this value, so it must agree with what routing
// uses; the cases below are the ones where a naive trim would disagree.
func TestNormalizeCredentialPrefix(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "team", "team"},
		{"surrounding whitespace", "  team  ", "team"},
		{"surrounding slashes", "/team/", "team"},
		{"whitespace and slashes", "  /team/  ", "team"},
		{"interior slash is rejected", "team/sub", ""},
		{"interior slash after trimming", "/team/sub/", ""},
		{"empty", "", ""},
		{"only whitespace", "   ", ""},
		{"only slashes", "///", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeCredentialPrefix(tc.in); got != tc.want {
				t.Fatalf("NormalizeCredentialPrefix(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
