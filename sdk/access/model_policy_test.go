package access

import "testing"

func TestModelPolicyAllows(t *testing.T) {
	tests := []struct {
		name            string
		allowed, denied []string
		model           string
		want            bool
	}{
		{"nil-like empty policy", nil, nil, "gpt-5", true},
		{"allow exact", []string{"gpt-5"}, nil, "gpt-5", true},
		{"allow miss", []string{"gpt-5"}, nil, "claude-x", false},
		{"allow wildcard", []string{"claude-*"}, nil, "Claude-Opus", true},
		{"allow question mark", []string{"gpt-?"}, nil, "gpt-5", true},
		{"question mark needs one char", []string{"gpt-?"}, nil, "gpt-55", false},
		{"deny only", nil, []string{"gpt-5.6-*"}, "gpt-5.6-mini", false},
		{"deny only passes others", nil, []string{"gpt-5.6-*"}, "gpt-5-codex", true},
		{"deny beats allow", []string{"*"}, []string{"gpt-5.6-*"}, "gpt-5.6-mini", false},
		{"star crosses slash", []string{"team/*"}, nil, "team/gemini-3", true},
		{"middle wildcard", []string{"a*c*e"}, nil, "abcde", true},
		{"whitespace trimmed", []string{" gpt-5 "}, nil, " gpt-5", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NewModelPolicy(tc.allowed, tc.denied).Allows(tc.model); got != tc.want {
				t.Fatalf("Allows(%q) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}

func TestNewModelPolicyEmptyIsNil(t *testing.T) {
	if NewModelPolicy([]string{" "}, nil) != nil {
		t.Fatal("blank patterns must yield a nil (unrestricted) policy")
	}
	if !(*ModelPolicy)(nil).Allows("anything") {
		t.Fatal("nil policy must allow everything")
	}
}
