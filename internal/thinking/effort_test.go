package thinking

import "testing"

func TestExtractTranslatedReasoningEffortConfigurationUpdate(t *testing.T) {
	const original = `{"model":"gpt-6-astra","reasoning":{"effort":"xhigh"},"input":[{"role":"user","content":"Earlier turn"},{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"Reply OK"}]}`
	body := []byte(original)
	if got := ExtractTranslatedReasoningEffort(body, "codex"); got != "low" {
		t.Fatalf("reasoning effort = %q, want low", got)
	}
	if got := ExtractReasoningEffort(body, "codex", "gpt-6-astra"); got != "xhigh" {
		t.Fatalf("request baseline effort = %q, want xhigh", got)
	}
	if string(body) != original {
		t.Fatal("usage extraction changed the request payload")
	}
}

func TestExtractTranslatedReasoningEffortUpdatePrecedence(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		body     string
		want     string
	}{
		{
			name: "responses format", provider: "openai-response",
			body: `{"reasoning":{"effort":"xhigh"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}}]}`,
			want: "low",
		},
		{
			name: "latest update wins", provider: "codex",
			body: `{"reasoning":{"effort":"xhigh"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"Earlier turn"},{"type":"configuration_update","reasoning":{"effort":"medium"}},{"role":"user","content":"Reply OK"}]}`,
			want: "medium",
		},
		{
			name: "summary-only update preserves effort", provider: "codex",
			body: `{"reasoning":{"effort":"xhigh"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"Earlier turn"},{"type":"configuration_update","reasoning":{"summary":"auto"}}]}`,
			want: "low",
		},
		{
			name: "invalid effort fields preserve prior update", provider: "codex",
			body: `{"reasoning":{"effort":"xhigh"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"type":"configuration_update","reasoning":{"effort":null}},{"type":"configuration_update","reasoning":{"effort":12}},{"type":"configuration_update","reasoning":{"effort":" "}}]}`,
			want: "low",
		},
		{
			name: "none is an explicit update", provider: "codex",
			body: `{"reasoning":{"effort":"xhigh"},"input":[{"type":"configuration_update","reasoning":{"effort":"none"}}]}`,
			want: "none",
		},
		{
			name: "other input items cannot override effort", provider: "codex",
			body: `{"reasoning":{"effort":"xhigh"},"input":[{"type":"message","role":"user","reasoning":{"effort":"low"},"content":"Reply OK"}]}`,
			want: "xhigh",
		},
		{
			name: "text input retains baseline", provider: "openai-response",
			body: `{"reasoning":{"effort":"high"},"input":"Reply OK"}`,
			want: "high",
		},
		{
			name: "update without explicit baseline", provider: "codex",
			body: `{"input":[{"type":"configuration_update","reasoning":{"effort":"low"}}]}`,
			want: "low",
		},
		{
			name: "chat format retains its own effort", provider: "openai",
			body: `{"reasoning_effort":"high","input":[{"type":"configuration_update","reasoning":{"effort":"low"}}]}`,
			want: "high",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.body)
			if got := ExtractTranslatedReasoningEffort(body, tt.provider); got != tt.want {
				t.Fatalf("reasoning effort = %q, want %q", got, tt.want)
			}
			if string(body) != tt.body {
				t.Fatal("usage extraction changed the request payload")
			}
		})
	}
}
