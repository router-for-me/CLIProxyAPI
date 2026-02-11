package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestGitHubCopilotNormalizeModel_StripsSuffix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		model     string
		wantModel string
	}{
		{
			name:      "suffix stripped",
			model:     "claude-opus-4.6(medium)",
			wantModel: "claude-opus-4.6",
		},
		{
			name:      "no suffix unchanged",
			model:     "claude-opus-4.6",
			wantModel: "claude-opus-4.6",
		},
		{
			name:      "different suffix stripped",
			model:     "gpt-4o(high)",
			wantModel: "gpt-4o",
		},
		{
			name:      "numeric suffix stripped",
			model:     "gemini-2.5-pro(8192)",
			wantModel: "gemini-2.5-pro",
		},
	}

	e := &GitHubCopilotExecutor{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := []byte(`{"model":"` + tt.model + `","messages":[]}`)
			got := e.normalizeModel(tt.model, body)

			gotModel := gjson.GetBytes(got, "model").String()
			if gotModel != tt.wantModel {
				t.Fatalf("normalizeModel() model = %q, want %q", gotModel, tt.wantModel)
			}
		})
	}
}
