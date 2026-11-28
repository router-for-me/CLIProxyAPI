package modelconfig

import "testing"

func TestIsModelExcluded(t *testing.T) {
	for _, tc := range []struct {
		name     string
		model    string
		prefix   string
		patterns []string
		want     bool
	}{
		{name: "canonical", model: "gemini-review-low", prefix: "team/", patterns: []string{"gemini-review-low"}, want: true},
		{name: "published prefix", model: "gemini-review-low", prefix: "team/", patterns: []string{"team//gemini-review-low"}, want: true},
		{name: "normalized prefix", model: "gemini-review-low", prefix: " /TEAM/ ", patterns: []string{"team/gemini-review-low"}, want: true},
		{name: "leading and trailing slashes", model: "gemini-review-low", prefix: " /TEAM/ ", patterns: []string{"/team//gemini-review-low"}, want: true},
		{name: "published wildcard", model: "gemini-review-low", prefix: "team/", patterns: []string{"team//*"}, want: true},
		{name: "canonical wildcard", model: "gemini-review-low", prefix: "team/", patterns: []string{" GEMINI-*-LOW "}, want: true},
		{name: "case and whitespace", model: " GEMINI-REVIEW-LOW ", prefix: " TEAM/ ", patterns: []string{" TEAM//GEMINI-REVIEW-LOW "}, want: true},
		{name: "slash-only prefix", model: "model", prefix: "/", patterns: []string{"//model"}, want: true},
		{name: "different credential prefix", model: "gemini-review-low", prefix: "other", patterns: []string{"team/gemini-review-low"}},
		{name: "different published prefix", model: "gemini-review-low", prefix: "team", patterns: []string{"team//gemini-review-low"}},
		{name: "no prefix", model: "gemini-review-low", patterns: []string{"team/gemini-review-low"}},
		{name: "no rules", model: "gemini-review-low", prefix: "team/"},
		{name: "blank rules", model: "gemini-review-low", prefix: "team/", patterns: []string{"", " "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsModelExcluded(tc.model, tc.patterns, tc.prefix); got != tc.want {
				t.Fatalf("IsModelExcluded(%q, %v, %q) = %v, want %v", tc.model, tc.patterns, tc.prefix, got, tc.want)
			}
		})
	}
}
