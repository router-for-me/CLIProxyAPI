package auth

import (
	"strings"
	"testing"
)

func TestValidateLabel(t *testing.T) {
	valid := []string{"work", "org-2", "my_team", "ABC", "a1b2", "x"}
	for _, label := range valid {
		if err := ValidateLabel(label); err != nil {
			t.Errorf("ValidateLabel(%q) unexpected error: %v", label, err)
		}
	}

	invalid := []string{"a/b", "foo bar", "../etc", "org@home", "label!", "a:b"}
	for _, label := range invalid {
		if err := ValidateLabel(label); err == nil {
			t.Errorf("ValidateLabel(%q) expected error, got nil", label)
		}
	}

	// empty string is always valid (no-label path)
	if err := ValidateLabel(""); err != nil {
		t.Errorf("ValidateLabel(\"\") unexpected error: %v", err)
	}

	// length cap
	long := strings.Repeat("a", 33)
	if err := ValidateLabel(long); err == nil {
		t.Errorf("ValidateLabel(%q) expected error for >32 chars, got nil", long)
	}
	exactly32 := strings.Repeat("a", 32)
	if err := ValidateLabel(exactly32); err != nil {
		t.Errorf("ValidateLabel(%q) unexpected error at exactly 32 chars: %v", exactly32, err)
	}
}

func TestClaudeFilenameConstruction(t *testing.T) {
	email := "user@example.com"
	cases := []struct {
		label string
		want  string
	}{
		{"", "claude-user@example.com.json"},
		{"work", "claude-user@example.com-work.json"},
		{"org-2", "claude-user@example.com-org-2.json"},
		{"my_team", "claude-user@example.com-my_team.json"},
	}
	for _, tc := range cases {
		got := claudeFileName(email, tc.label)
		if got != tc.want {
			t.Errorf("claudeFileName(%q, %q) = %q, want %q", email, tc.label, got, tc.want)
		}
	}
}
