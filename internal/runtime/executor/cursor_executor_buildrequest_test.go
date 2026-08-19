package executor

import "testing"

func TestBuildRunRequestParams_ModelOverride(t *testing.T) {
	tests := []struct {
		name        string
		parsedModel string
		override    string
		wantModelID string
	}{
		{
			name:        "alias override preserves client model",
			parsedModel: "cursor/composer-2.5",
			override:    "composer-2.5",
			wantModelID: "composer-2.5",
		},
		{
			name:        "empty override falls back to parsed model",
			parsedModel: "composer-2.5",
			wantModelID: "composer-2.5",
		},
		{
			name:        "whitespace override falls back to parsed model",
			parsedModel: "composer-2.5",
			override:    " \t ",
			wantModelID: "composer-2.5",
		},
		{
			name:        "override supplies missing parsed model",
			override:    "composer-2.5",
			wantModelID: "composer-2.5",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parsed := &parsedOpenAIRequest{Model: tc.parsedModel}
			params := buildRunRequestParams(parsed, "conv-123", tc.override)

			if params.ModelId != tc.wantModelID {
				t.Errorf("ModelId = %q, want %q", params.ModelId, tc.wantModelID)
			}
			if params.ConversationId != "conv-123" {
				t.Errorf("ConversationId = %q, want %q", params.ConversationId, "conv-123")
			}
			if parsed.Model != tc.parsedModel {
				t.Errorf("parsed.Model = %q, want %q", parsed.Model, tc.parsedModel)
			}
		})
	}
}
