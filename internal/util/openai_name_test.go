package util

import "testing"

func TestSanitizeOpenAICompatName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"read", "read"},
		{"read_file", "read_file"},
		{"read-file", "read-file"},
		{"read file", "read_file"},
		{"<|tool_call|>", "tool_call"},
		{"  !!!  ", "tool"},
		{"你好", "tool"},
	}

	for _, c := range cases {
		got := SanitizeOpenAICompatName(c.in)
		if got != c.want {
			t.Fatalf("SanitizeOpenAICompatName(%q)=%q want %q", c.in, got, c.want)
		}
		if len(got) > 64 {
			t.Fatalf("output too long: %d for %q", len(got), c.in)
		}
	}
}
