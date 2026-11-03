package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
)

func TestClaudeEffortDirective(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "no messages", body: `{"output_config":{"effort":"low"}}`, want: ""},
		{name: "no directive", body: `{"messages":[{"role":"user","content":"hi"}]}`, want: ""},
		{
			name: "last directive wins",
			body: `{"messages":[{"role":"system","content":[],"output_config":{"effort":"medium"}},` +
				`{"role":"user","content":"hi"},{"role":"system","content":[],"output_config":{"effort":" HIGH "}}]}`,
			want: "high",
		},
		{
			name: "empty directive is ignored",
			body: `{"messages":[{"role":"system","content":[],"output_config":{"effort":"max"}},` +
				`{"role":"system","content":[],"output_config":{"effort":""}}]}`,
			want: "max",
		},
		{name: "non-system output_config is ignored", body: `{"messages":[{"role":"user","content":"hi","output_config":{"effort":"high"}}]}`, want: ""},
		{name: "invalid json", body: `{`, want: ""},
		{name: "disabled thinking ignores directives", body: `{"thinking":{"type":"disabled"},"messages":[{"role":"system","content":[],"output_config":{"effort":"high"}}]}`, want: ""},
		{name: "between_tools thinking ignores directives", body: `{"thinking":{"type":"between_tools"},"messages":[{"role":"system","content":[],"output_config":{"effort":"high"}}]}`, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := thinking.ClaudeEffortDirective([]byte(tc.body)); got != tc.want {
				t.Fatalf("ClaudeEffortDirective() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClaudeUsageEffortPrefersDirective(t *testing.T) {
	const directives = `"messages":[{"role":"system","content":[],"output_config":{"effort":"low"}},{"role":"user","content":"hi"},` +
		`{"role":"system","content":[],"output_config":{"effort":"high"}}]`
	for _, tc := range []struct {
		name  string
		body  string
		model string
		want  string
	}{
		{
			name:  "directive overrides top-level placeholder",
			body:  `{"thinking":{"type":"adaptive"},"output_config":{"effort":"low"},` + directives + `}`,
			model: "claude-opus-5-5",
			want:  "high",
		},
		{
			name:  "last non-empty directive wins and is normalized",
			body:  `{"thinking":{"type":"adaptive"},"output_config":{"effort":"low"},"messages":[{"role":"system","content":[],"output_config":{"effort":"medium"}},{"role":"user","content":"hi"},{"role":"system","content":[],"output_config":{"effort":" HIGH "}},{"role":"system","content":[],"output_config":{"effort":"   "}}]}`,
			model: "claude-haiku-5-5",
			want:  "high",
		},
		{
			name:  "non-system output_config does not override top-level effort",
			body:  `{"thinking":{"type":"adaptive"},"output_config":{"effort":"low"},"messages":[{"role":"user","content":"hi","output_config":{"effort":"high"}}]}`,
			model: "claude-opus-5-5",
			want:  "low",
		},
		{
			name:  "top-level effort without directives",
			body:  `{"thinking":{"type":"adaptive"},"output_config":{"effort":"medium"},"messages":[{"role":"user","content":"hi"}]}`,
			model: "claude-opus-5-5",
			want:  "medium",
		},
		{
			name:  "disabled thinking stays none",
			body:  `{"thinking":{"type":"disabled"},` + directives + `}`,
			model: "claude-opus-5-5",
			want:  "none",
		},
		{
			name:  "between_tools thinking stays none",
			body:  `{"thinking":{"type":"between_tools"},` + directives + `}`,
			model: "claude-opus-5-5",
			want:  "none",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := thinking.ExtractReasoningEffort([]byte(tc.body), "claude", tc.model); got != tc.want {
				t.Fatalf("ExtractReasoningEffort() = %q, want %q", got, tc.want)
			}
			if got := thinking.ExtractTranslatedReasoningEffort([]byte(tc.body), "claude"); got != tc.want {
				t.Fatalf("ExtractTranslatedReasoningEffort() = %q, want %q", got, tc.want)
			}
		})
	}

	// A model suffix still wins for the client-requested effort.
	body := `{"thinking":{"type":"adaptive"},"output_config":{"effort":"low"},` + directives + `}`
	if got := thinking.ExtractReasoningEffort([]byte(body), "claude", "claude-opus-5-5(medium)"); got != "medium" {
		t.Fatalf("ExtractReasoningEffort() with suffix = %q, want medium", got)
	}
}
