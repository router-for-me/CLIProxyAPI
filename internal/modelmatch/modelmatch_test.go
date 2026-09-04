package modelmatch

import "testing"

func TestMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		model   string
		want    bool
	}{
		{name: "exact", pattern: "gpt-5", model: "gpt-5", want: true},
		{name: "case sensitive exact", pattern: "GPT-5", model: "gpt-5", want: false},
		{name: "trimmed", pattern: "  gpt-5  ", model: " gpt-5 ", want: true},
		{name: "prefix wildcard", pattern: "gpt-*", model: "gpt-5", want: true},
		{name: "suffix wildcard", pattern: "*-5", model: "gpt-5", want: true},
		{name: "middle wildcard", pattern: "gemini-*-pro", model: "gemini-2.5-pro", want: true},
		{name: "wildcard matches empty", pattern: "gpt-*-mini", model: "gpt--mini", want: true},
		{name: "multiple wildcards", pattern: "*mini*-*-latest", model: "gpt-mini-fast-5-latest", want: true},
		{name: "case sensitive wildcard", pattern: "GEMINI-*-PRO", model: "gemini-2.5-pro", want: false},
		{name: "all", pattern: "*", model: "anything", want: true},
		{name: "empty pattern", pattern: "", model: "gpt-5", want: false},
		{name: "exact mismatch", pattern: "gpt-5", model: "gpt-5-mini", want: false},
		{name: "wildcard mismatch", pattern: "gemini-*-pro", model: "gemini-2.5-flash", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Match(tt.pattern, tt.model); got != tt.want {
				t.Fatalf("Match(%q, %q) = %v, want %v", tt.pattern, tt.model, got, tt.want)
			}
		})
	}
}

func TestMatchFold(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		pattern string
		model   string
	}{
		{pattern: "GPT-5", model: "gpt-5"},
		{pattern: "GEMINI-*-PRO", model: "gemini-2.5-pro"},
	} {
		if !MatchFold(tt.pattern, tt.model) {
			t.Fatalf("MatchFold(%q, %q) = false, want true", tt.pattern, tt.model)
		}
	}
}
