package codex

import (
	"testing"
)

func TestCredentialFileName(t *testing.T) {
	cases := []struct {
		email    string
		plan     string
		hashID   string
		prefix   bool
		want     string
	}{
		{"test@example.com", "", "", false, "-test@example.com.json"},
		{"test@example.com", "", "", true, "codex-test@example.com.json"},
		{"test@example.com", "plus", "", true, "codex-test@example.com-plus.json"},
		{"test@example.com", "team", "123", true, "codex-123-test@example.com-team.json"},
	}
	for _, tc := range cases {
		got := CredentialFileName(tc.email, tc.plan, tc.hashID, tc.prefix)
		if got != tc.want {
			t.Errorf("CredentialFileName(%q, %q, %q, %v) = %q, want %q", tc.email, tc.plan, tc.hashID, tc.prefix, got, tc.want)
		}
	}
}

func TestNormalizePlanTypeForFilename(t *testing.T) {
	cases := []struct {
		plan string
		want string
	}{
		{"", ""},
		{"Plus", "plus"},
		{"Team Subscription", "team-subscription"},
		{"!!!", ""},
	}
	for _, tc := range cases {
		got := normalizePlanTypeForFilename(tc.plan)
		if got != tc.want {
			t.Errorf("normalizePlanTypeForFilename(%q) = %q, want %q", tc.plan, got, tc.want)
		}
	}
}
